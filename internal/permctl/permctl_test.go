package permctl

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

func rpcCall(t *testing.T, url, body string) map[string]any {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	if buf.Len() == 0 {
		return nil
	}
	var out map[string]any
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", buf.String(), err)
	}
	return out
}

func TestPermissionToolContract(t *testing.T) {
	for _, tc := range []struct {
		name  string
		allow bool
	}{{"allow", true}, {"deny", false}} {
		t.Run(tc.name, func(t *testing.T) {
			var got core.ApprovalRequest
			p := New(func(req core.ApprovalRequest) core.ApprovalDecision {
				got = req
				return core.ApprovalDecision{Allow: tc.allow, Message: "denied for test"}
			})
			ts := httptest.NewServer(http.HandlerFunc(p.handle))
			defer ts.Close()

			init := rpcCall(t, ts.URL, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}`)
			res, _ := init["result"].(map[string]any)
			if res["protocolVersion"] != "2025-06-18" {
				t.Fatalf("protocolVersion not echoed: %v", res)
			}

			list := rpcCall(t, ts.URL, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
			lr, _ := list["result"].(map[string]any)
			tools, _ := lr["tools"].([]any)
			if len(tools) != 1 {
				t.Fatalf("expected 1 tool, got %v", tools)
			}
			if first, _ := tools[0].(map[string]any); first["name"] != toolName {
				t.Fatalf("tool name = %v, want %s", first["name"], toolName)
			}

			call := rpcCall(t, ts.URL, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"approve","arguments":{"tool_name":"Write","input":{"file_path":"/x","content":"hi"},"tool_use_id":"tu_1"}}}`)
			cr, _ := call["result"].(map[string]any)
			content, _ := cr["content"].([]any)
			if len(content) != 1 {
				t.Fatalf("expected 1 content block, got %v", cr)
			}
			block, _ := content[0].(map[string]any)
			text, _ := block["text"].(string)

			var payload map[string]any
			if err := json.Unmarshal([]byte(text), &payload); err != nil {
				t.Fatalf("payload not JSON: %q", text)
			}
			if tc.allow {
				if payload["behavior"] != "allow" {
					t.Fatalf("behavior = %v, want allow", payload["behavior"])
				}
				ui, _ := payload["updatedInput"].(map[string]any)
				if ui["file_path"] != "/x" {
					t.Fatalf("updatedInput not echoed: %v", payload["updatedInput"])
				}
			} else {
				if payload["behavior"] != "deny" || payload["message"] != "denied for test" {
					t.Fatalf("deny payload wrong: %v", payload)
				}
			}
			if got.ToolName != "Write" || got.ToolUseID != "tu_1" {
				t.Fatalf("decider request = %+v", got)
			}
		})
	}
}

func TestNotificationReturns202(t *testing.T) {
	p := New(nil)
	ts := httptest.NewServer(http.HandlerFunc(p.handle))
	defer ts.Close()
	resp, err := http.Post(ts.URL, "application/json", strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", resp.StatusCode)
	}
}

func TestResultJSON(t *testing.T) {
	allow := resultJSON(core.ApprovalDecision{Allow: true}, json.RawMessage(`{"a":1}`))
	if allow != `{"behavior":"allow","updatedInput":{"a":1}}` {
		t.Fatalf("allow payload = %s", allow)
	}
	emptyAllow := resultJSON(core.ApprovalDecision{Allow: true}, nil)
	if emptyAllow != `{"behavior":"allow","updatedInput":{}}` {
		t.Fatalf("empty allow payload = %s", emptyAllow)
	}
	deny := resultJSON(core.ApprovalDecision{Allow: false}, nil)
	if deny != `{"behavior":"deny","message":"Denied by user"}` {
		t.Fatalf("deny payload = %s", deny)
	}
}

func TestConfigAndPromptTool(t *testing.T) {
	p := New(nil)
	if err := p.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer p.Stop()
	cfg := p.MCPConfigJSON()
	if !strings.Contains(cfg, `"type":"http"`) || !strings.Contains(cfg, p.Addr()) {
		t.Fatalf("config missing fields: %s", cfg)
	}
	if p.PromptTool() != "mcp__permctl__approve" {
		t.Fatalf("prompt tool = %s", p.PromptTool())
	}
	if !strings.HasPrefix(p.Addr(), "127.0.0.1:") {
		t.Fatalf("server must bind loopback, got %s", p.Addr())
	}
}
