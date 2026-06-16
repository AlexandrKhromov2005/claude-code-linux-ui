package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/permctl"
)

// TestLiveWebApprovalFlow drives the whole web stack against the real claude
// binary: a scripted client opens a project over REST, then streams an agent
// turn over WebSocket and answers the approval request. Gated on CLAUDE_LIVE.
func TestLiveWebApprovalFlow(t *testing.T) {
	if os.Getenv("CLAUDE_LIVE") == "" {
		t.Skip("set CLAUDE_LIVE=1 to run the live web approval test")
	}
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}

	run := func(t *testing.T, allow bool, wantFile string) {
		proj := t.TempDir()
		dir := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
		t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))

		store, err := core.NewStore()
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		cfg, _ := store.LoadConfig()
		app := core.NewApp(store, cfg, &core.Engine{BinPath: bin, Mode: core.ModeChat})

		perm := permctl.New(app.HandleApproval)
		if err := perm.Start(); err != nil {
			t.Fatalf("perm: %v", err)
		}
		defer perm.Stop()
		app.SetPermission(perm)

		s := New(app, nil)
		app.SetBroker(s)
		if err := s.Listen("127.0.0.1:0"); err != nil {
			t.Fatalf("Listen: %v", err)
		}
		go func() { _ = s.Serve() }()
		defer s.Close()
		addr := s.ln.Addr().String()
		base := "http://" + addr

		post := func(path string, body any) {
			b, _ := json.Marshal(body)
			req, _ := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(b))
			req.Header.Set("Authorization", "Bearer "+s.token)
			req.Header.Set("Origin", base)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("post %s: %v", path, err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("post %s: status %d", path, resp.StatusCode)
			}
			resp.Body.Close()
		}
		post("/api/projects/use", map[string]string{"cwd": proj})
		post("/api/mode", map[string]string{"mode": "agent"})

		d := websocket.Dialer{Subprotocols: []string{wsSubprotocol, s.token}, HandshakeTimeout: 5 * time.Second}
		hdr := http.Header{}
		hdr.Set("Origin", base)
		ws, _, err := d.Dial("ws://"+addr+"/ws", hdr)
		if err != nil {
			t.Fatalf("ws dial: %v", err)
		}
		defer ws.Close()

		_ = ws.WriteJSON(map[string]any{
			"type": "send",
			"text": "Create a file named " + wantFile + " containing the word hi. Use the Write tool.",
		})

		sawApproval := false
		denials := 0
		for {
			// Per-message idle timeout: a denied turn may retry for a while, so
			// the budget is per read, not for the whole turn.
			_ = ws.SetReadDeadline(time.Now().Add(90 * time.Second))
			var msg map[string]any
			if err := ws.ReadJSON(&msg); err != nil {
				t.Fatalf("ws read: %v", err)
			}
			switch msg["type"] {
			case "approval_request":
				sawApproval = true
				_ = ws.WriteJSON(map[string]any{"type": "approval", "id": msg["id"], "allow": allow})
				if !allow {
					denials++
					// Claude retries denied calls; stop it after a couple of
					// rounds so the test stays bounded.
					if denials >= 2 {
						_ = ws.WriteJSON(map[string]any{"type": "cancel"})
					}
				}
			case "turn_end":
				goto done
			}
		}
	done:
		_, statErr := os.Stat(filepath.Join(proj, wantFile))
		if allow && statErr != nil {
			t.Errorf("allow: %s should exist: %v", wantFile, statErr)
		}
		if !allow && statErr == nil {
			t.Errorf("deny: %s should not exist", wantFile)
		}
		t.Logf("approval seen=%v, file exists=%v", sawApproval, statErr == nil)
	}

	t.Run("allow", func(t *testing.T) { run(t, true, "web_allow.txt") })
	t.Run("deny", func(t *testing.T) { run(t, false, "web_deny.txt") })
}
