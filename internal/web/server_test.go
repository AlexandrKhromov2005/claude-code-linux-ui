package web

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

func newTestApp(t *testing.T) *core.App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	store, err := core.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	cfg, _ := store.LoadConfig()
	return core.NewApp(store, cfg, &core.Engine{BinPath: "claude", Mode: core.ModeChat})
}

// startServer binds a loopback server and returns it with its host:port.
func startServer(t *testing.T) (*Server, string) {
	t.Helper()
	s := New(newTestApp(t), nil)
	if err := s.Listen("127.0.0.1:0"); err != nil {
		t.Fatalf("Listen: %v", err)
	}
	go func() { _ = s.Serve() }()
	t.Cleanup(func() { _ = s.Close() })
	return s, s.ln.Addr().String()
}

func TestListenRejectsNonLoopback(t *testing.T) {
	s := New(newTestApp(t), nil)
	if err := s.Listen("0.0.0.0:0"); err == nil {
		t.Fatalf("expected refusal to bind non-loopback address")
	}
}

func do(t *testing.T, method, url string, mutate func(*http.Request)) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	return resp
}

func TestAPIRequiresToken(t *testing.T) {
	s, addr := startServer(t)
	base := "http://" + addr

	resp := do(t, http.MethodGet, base+"/api/state", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()

	resp = do(t, http.MethodGet, base+"/api/state", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+s.token)
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("with token: status = %d, want 200", resp.StatusCode)
	}
	resp.Body.Close()

	// A wrong token is rejected.
	resp = do(t, http.MethodGet, base+"/api/state", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer deadbeef")
	})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad token: status = %d, want 401", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestBadOriginRejected(t *testing.T) {
	s, addr := startServer(t)
	resp := do(t, http.MethodGet, "http://"+addr+"/api/state", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+s.token)
		r.Header.Set("Origin", "http://evil.example.com")
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad origin: status = %d, want 403", resp.StatusCode)
	}
}

func TestBadHostRejected(t *testing.T) {
	s, addr := startServer(t)
	resp := do(t, http.MethodGet, "http://"+addr+"/api/state", func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+s.token)
		r.Host = "rebind.evil.example.com" // DNS-rebinding style Host
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad host: status = %d, want 403", resp.StatusCode)
	}
}

func TestStaticServedWithoutToken(t *testing.T) {
	_, addr := startServer(t)
	resp := do(t, http.MethodGet, "http://"+addr+"/", nil)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("static: status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(string(body), "claude-code-linux-ui") {
		t.Fatalf("placeholder page not served")
	}

	// Static still rejects a foreign Origin (DNS-rebinding defense).
	resp = do(t, http.MethodGet, "http://"+addr+"/", func(r *http.Request) {
		r.Header.Set("Origin", "http://evil.example.com")
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("static bad origin: status = %d, want 403", resp.StatusCode)
	}
}

func dialWS(t *testing.T, addr string, subprotocols []string, origin string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	d := websocket.Dialer{Subprotocols: subprotocols, HandshakeTimeout: 5 * time.Second}
	hdr := http.Header{}
	if origin != "" {
		hdr.Set("Origin", origin)
	}
	return d.Dial("ws://"+addr+"/ws", hdr)
}

func TestWSRequiresToken(t *testing.T) {
	_, addr := startServer(t)
	_, resp, err := dialWS(t, addr, []string{wsSubprotocol}, "http://"+addr)
	if err == nil {
		t.Fatalf("ws without token should fail")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ws no token: want 401, got %v", resp)
	}
}

func TestWSBadOriginRejected(t *testing.T) {
	s, addr := startServer(t)
	_, resp, err := dialWS(t, addr, []string{wsSubprotocol, s.token}, "http://evil.example.com")
	if err == nil {
		t.Fatalf("ws with bad origin should fail")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("ws bad origin: want 403, got %v", resp)
	}
}

func TestWSValidHandshake(t *testing.T) {
	s, addr := startServer(t)
	ws, resp, err := dialWS(t, addr, []string{wsSubprotocol, s.token}, "http://"+addr)
	if err != nil {
		t.Fatalf("valid ws dial failed: %v (resp %v)", err, resp)
	}
	defer ws.Close()
	if ws.Subprotocol() != wsSubprotocol {
		t.Fatalf("negotiated subprotocol = %q, want %q", ws.Subprotocol(), wsSubprotocol)
	}
	// The server greets with the current state.
	var msg map[string]any
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := ws.ReadJSON(&msg); err != nil {
		t.Fatalf("read state: %v", err)
	}
	if msg["type"] != "state" {
		t.Fatalf("first message type = %v, want state", msg["type"])
	}
}
