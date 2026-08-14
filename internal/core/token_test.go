package core

import (
	"os"
	"strings"
	"testing"
)

// The point of persisting the token: restarting the server must not invalidate
// links that are already open.
func TestTokenSurvivesRestart(t *testing.T) {
	s := newTestStore(t)

	first, fresh, err := s.LoadOrCreateToken()
	if err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Error("the very first token should be reported as newly minted")
	}
	if !validToken(first) {
		t.Fatalf("minted an invalid token: %q", first)
	}

	second, fresh, err := s.LoadOrCreateToken()
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Error("token changed across restarts — open tabs would break, which is the bug being fixed")
	}
	if fresh {
		t.Error("a reused token must not be reported as newly minted")
	}
}

// The token is a secret sitting in the config directory; it must never be
// readable by anyone but its owner.
func TestTokenFileIsOwnerOnly(t *testing.T) {
	s := newTestStore(t)
	if _, _, err := s.LoadOrCreateToken(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.TokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file mode = %04o, want 0600", perm)
	}
}

// A token others could read has to be assumed seen. Carrying on with it would
// be the convenient answer and the wrong one.
func TestTokenReplacedWhenWorldReadable(t *testing.T) {
	s := newTestStore(t)
	original, _, err := s.LoadOrCreateToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.TokenPath(), 0o644); err != nil {
		t.Fatal(err)
	}

	replaced, fresh, err := s.LoadOrCreateToken()
	if err == nil {
		t.Error("replacing an exposed token should be reported, not silent")
	} else if !strings.Contains(err.Error(), "заменён") {
		t.Errorf("unhelpful explanation: %v", err)
	}
	if replaced == original {
		t.Error("an exposed token was reused")
	}
	if !fresh {
		t.Error("replacement was not reported as a new token")
	}
	info, _ := os.Stat(s.TokenPath())
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("replacement mode = %04o, want 0600", perm)
	}
}

// A truncated or hand-edited file must never be accepted: a short token is a
// weak secret, and silently serving with one is worse than issuing a new link.
func TestTokenReplacedWhenCorrupt(t *testing.T) {
	for _, bad := range []string{"", "   ", "abc", strings.Repeat("z", 64)} {
		t.Run("value_"+bad, func(t *testing.T) {
			s := newTestStore(t)
			if _, _, err := s.LoadOrCreateToken(); err != nil {
				t.Fatal(err)
			}
			if err := writeFileMode(s.TokenPath(), []byte(bad), 0o600); err != nil {
				t.Fatal(err)
			}
			tok, fresh, err := s.LoadOrCreateToken()
			if err != nil {
				t.Fatal(err)
			}
			if !validToken(tok) {
				t.Errorf("accepted a corrupt token: %q", tok)
			}
			if !fresh {
				t.Error("a replacement was not reported as new")
			}
		})
	}
}

func TestRotateTokenInvalidatesTheOldOne(t *testing.T) {
	s := newTestStore(t)
	before, _, err := s.LoadOrCreateToken()
	if err != nil {
		t.Fatal(err)
	}
	after, err := s.RotateToken()
	if err != nil {
		t.Fatal(err)
	}
	if after == before {
		t.Fatal("rotation returned the same token")
	}
	if !validToken(after) {
		t.Fatalf("rotation produced an invalid token: %q", after)
	}
	// And the new one is what a subsequent start picks up.
	reloaded, fresh, err := s.LoadOrCreateToken()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded != after {
		t.Error("rotation was not persisted")
	}
	if fresh {
		t.Error("reading back a rotated token should not count as minting another")
	}
}

func TestValidToken(t *testing.T) {
	good := strings.Repeat("ab", tokenBytes)
	if !validToken(good) {
		t.Errorf("rejected a well-formed token")
	}
	for _, bad := range []string{"", "abcd", strings.Repeat("ab", tokenBytes-1), strings.Repeat("gg", tokenBytes)} {
		if validToken(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

// The secret must never exist on disk in a readable-by-others state, not even
// momentarily between creation and chmod.
func TestWriteFileModeNeverWidensPermissions(t *testing.T) {
	path := t.TempDir() + "/secret"
	if err := writeFileMode(path, []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %04o, want 0600", perm)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "s3cret" {
		t.Errorf("content = %q", b)
	}
	// Overwriting keeps the mode rather than inheriting a looser umask.
	if err := writeFileMode(path, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(path)
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode after overwrite = %04o, want 0600", perm)
	}
}
