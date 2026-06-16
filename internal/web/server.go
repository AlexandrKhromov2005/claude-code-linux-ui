// Package web serves a local HTTP/WebSocket UI over the core App. It binds
// loopback only and authenticates every API request and WebSocket upgrade with
// a per-session token plus a strict Host/Origin allowlist.
package web

import (
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// wsSubprotocol is the negotiated subprotocol; the client also offers the token
// as a second subprotocol so it never travels in the URL.
const wsSubprotocol = "ccl-bearer"

// Server exposes the core App over REST + WebSocket for the web client.
type Server struct {
	app    *core.App
	token  string
	assets fs.FS // embedded UI assets; nil serves a placeholder

	ln    net.Listener
	allow allowlist

	upgrader websocket.Upgrader
	devProxy *httputil.ReverseProxy // non-nil in dev: proxies static to Vite

	mu         sync.Mutex
	activeConn *wsConn
}

// SetDevProxy routes non-API requests to a running Vite dev server (hot
// reload) instead of the embedded/placeholder assets.
func (s *Server) SetDevProxy(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return err
	}
	s.devProxy = httputil.NewSingleHostReverseProxy(u)
	return nil
}

// New builds a server over the App. assets may be nil (a placeholder page is
// served until the web client is embedded).
func New(app *core.App, assets fs.FS) *Server {
	s := &Server{
		app:    app,
		token:  newToken(),
		assets: assets,
	}
	s.upgrader = websocket.Upgrader{
		HandshakeTimeout: 10 * time.Second,
		Subprotocols:     []string{wsSubprotocol},
		CheckOrigin:      func(r *http.Request) bool { return s.allow.checkHostOrigin(r) == "" },
	}
	return s
}

// Token returns the per-session bearer token.
func (s *Server) Token() string { return s.token }

// Listen binds the given loopback address and derives the request allowlist from
// the bound port. It refuses any non-loopback host.
func (s *Server) Listen(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return err
	}
	if !isLoopbackHost(host) {
		return fmt.Errorf("отказ слушать не-loopback адрес %q (используйте 127.0.0.1)", host)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.ln = ln
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	s.allow = newAllowlist(port)
	return nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// URL is the address to open in a browser, with the token in the fragment so it
// is never sent to the server or written to logs.
func (s *Server) URL() string {
	if s.ln == nil {
		return ""
	}
	return fmt.Sprintf("http://%s/#token=%s", s.ln.Addr().String(), s.token)
}

// Serve runs until the listener is closed.
func (s *Server) Serve() error {
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	return srv.Serve(s.ln)
}

// Close stops listening.
func (s *Server) Close() error {
	if s.ln != nil {
		return s.ln.Close()
	}
	return nil
}

// Handler builds the full routing tree.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/ws", s.handleWS)
	s.registerAPI(mux)
	mux.HandleFunc("/", s.handleStatic)
	return mux
}

// guard wraps an API handler with the Host/Origin allowlist and bearer-token
// checks. It is the single choke point for authenticated requests.
func (s *Server) guard(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if reason := s.allow.checkHostOrigin(r); reason != "" {
			http.Error(w, reason, http.StatusForbidden)
			return
		}
		if !tokenEqual(bearerToken(r), s.token) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		fn(w, r)
	}
}

// setActiveConn registers the connection that approval requests are routed to.
func (s *Server) setActiveConn(c *wsConn) {
	s.mu.Lock()
	s.activeConn = c
	s.mu.Unlock()
}

func (s *Server) clearActiveConn(c *wsConn) {
	s.mu.Lock()
	if s.activeConn == c {
		s.activeConn = nil
	}
	s.mu.Unlock()
}
