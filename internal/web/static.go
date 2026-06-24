package web

import (
	"errors"
	"io"
	"net/http"
)

// handleStatic serves the embedded web client, or a placeholder when no assets
// are bundled. It enforces the loopback Host/Origin allowlist but not the token:
// the page itself carries no data and reads the token from the URL fragment.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if reason := s.allow.checkHostOrigin(r); reason != "" {
		http.Error(w, reason, http.StatusForbidden)
		return
	}
	if s.devProxy != nil {
		s.devProxy.ServeHTTP(w, r)
		return
	}
	if s.assets == nil {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(placeholderHTML))
		return
	}
	http.FileServer(http.FS(s.assets)).ServeHTTP(w, r)
}

const placeholderHTML = `<!doctype html>
<title>claude-code-linux-ui</title>
<body style="font-family:system-ui;max-width:40rem;margin:3rem auto;color:#ddd;background:#1a1a1a">
<h1>claude-code-linux-ui</h1>
<p>Сервер запущен. Веб-клиент ещё не собран в этот бинарь.</p>
<p>Соберите фронтенд (web/) и пересоберите с встроенными ассетами.</p>
</body>`

// errFileTooLarge is returned by copyLimited when the source exceeds the cap.
var errFileTooLarge = errors.New("файл слишком большой")

// copyLimited copies at most max bytes, erroring if the source is larger.
func copyLimited(dst io.Writer, src io.Reader, max int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, max+1))
	if err != nil {
		return n, err
	}
	if n > max {
		return max, errFileTooLarge
	}
	return n, nil
}
