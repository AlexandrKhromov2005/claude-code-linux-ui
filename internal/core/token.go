package core

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The bearer token the local server authenticates with is kept on disk rather
// than regenerated per start.
//
// It began as a per-session secret, which is the stricter choice, but it made
// restarting the server break every open tab: the token travels in the page's
// URL fragment, so a tab opened before a restart holds a dead link and cannot
// recover by reloading. Restarting is routine here — every rebuild does it — so
// the strict version mostly produced confusing failures rather than security.
//
// Persisting it trades one property for that: a link that leaks stays valid
// until the file is replaced, instead of until the next restart. The server
// binds loopback only and the file is owner-only, so the exposure is to someone
// who already has an account on this machine. RotateToken exists for when that
// trade needs undoing.

// tokenFile is the on-disk name of the bearer token.
const tokenFile = "token"

// tokenBytes is the token's entropy; hex-encoded it is twice this long.
const tokenBytes = 32

// TokenPath returns the file holding the local server's bearer token.
func (s *Store) TokenPath() string { return filepath.Join(s.ConfigDir, tokenFile) }

// LoadOrCreateToken returns the persisted bearer token, creating one on first
// use. It also reports whether a new token was minted, so the caller can say why
// existing links stopped working.
//
// A token is replaced rather than reused when the file is readable by anyone
// but its owner: at that point it has to be assumed seen, and silently carrying
// on with it would be the wrong kind of convenient.
func (s *Store) LoadOrCreateToken() (token string, fresh bool, err error) {
	path := s.TokenPath()
	info, statErr := os.Stat(path)
	switch {
	case statErr == nil:
		if info.Mode().Perm()&0o077 != 0 {
			// Group or world can read it. Treat it as spent.
			tok, err := s.writeNewToken()
			if err != nil {
				return "", false, err
			}
			return tok, true, fmt.Errorf("токен %s был доступен другим пользователям (права %04o) и заменён", path, info.Mode().Perm())
		}
		b, readErr := os.ReadFile(path)
		if readErr != nil {
			return "", false, readErr
		}
		if tok := strings.TrimSpace(string(b)); validToken(tok) {
			return tok, false, nil
		}
		// Corrupt or truncated: a short token is worse than none, so replace it.
		tok, err := s.writeNewToken()
		return tok, true, err

	case errors.Is(statErr, os.ErrNotExist):
		tok, err := s.writeNewToken()
		return tok, true, err

	default:
		return "", false, statErr
	}
}

// RotateToken discards the stored token and issues a new one, invalidating every
// link handed out so far.
func (s *Store) RotateToken() (string, error) { return s.writeNewToken() }

// writeNewToken mints a token and stores it owner-readable only.
func (s *Store) writeNewToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("нет источника случайности для токена: %w", err)
	}
	tok := hex.EncodeToString(b)
	if err := writeFileMode(s.TokenPath(), []byte(tok), 0o600); err != nil {
		return "", err
	}
	return tok, nil
}

// validToken reports whether a stored value is a full-length hex token, so a
// truncated or hand-edited file is never accepted as a secret.
func validToken(tok string) bool {
	if len(tok) != tokenBytes*2 {
		return false
	}
	_, err := hex.DecodeString(tok)
	return err == nil
}

// writeFileMode writes atomically at an exact permission mode. The temp file is
// chmodded before the rename so the secret is never briefly world-readable.
func writeFileMode(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	_, werr := tmp.Write(data)
	cerr := tmp.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmpName)
		if werr != nil {
			return werr
		}
		return cerr
	}
	return os.Rename(tmpName, path)
}
