package permctl

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// TestLiveClaudeApproval drives the real `claude` binary against the in-process
// permission server to confirm the MCP transport and allow/deny contract end to
// end. It asserts only deterministic invariants: a granted-and-attempted tool
// writes its file; a denied tool never does. Skipped unless CLAUDE_LIVE=1.
func TestLiveClaudeApproval(t *testing.T) {
	if os.Getenv("CLAUDE_LIVE") == "" {
		t.Skip("set CLAUDE_LIVE=1 to run the live claude approval test")
	}
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}

	run := func(t *testing.T, allow bool, prompt, wantFile string) {
		dir := t.TempDir()

		var mu sync.Mutex
		var calls []core.ApprovalRequest
		srv := New(func(req core.ApprovalRequest) core.ApprovalDecision {
			mu.Lock()
			calls = append(calls, req)
			mu.Unlock()
			return core.ApprovalDecision{Allow: allow, Message: "denied by test"}
		})
		if err := srv.Start(); err != nil {
			t.Fatalf("start server: %v", err)
		}
		defer srv.Stop()

		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
		defer cancel()

		cmd := exec.CommandContext(ctx, bin,
			"-p", prompt,
			"--output-format", "stream-json", "--verbose", "--include-partial-messages",
			"--permission-mode", "default",
			"--permission-prompt-tool", srv.PromptTool(),
			"--mcp-config", srv.MCPConfigJSON(),
		)
		cmd.Dir = dir
		_ = cmd.Run()

		mu.Lock()
		n := len(calls)
		for _, c := range calls {
			if c.ToolName == "" || (len(c.Input) > 0 && !json.Valid(c.Input)) {
				t.Errorf("approval request missing tool_name or had invalid input")
			}
		}
		mu.Unlock()

		_, statErr := os.Stat(filepath.Join(dir, wantFile))
		fileExists := statErr == nil
		t.Logf("approve consulted %d time(s), file exists=%v", n, fileExists)

		if allow {
			// When the broker granted an attempted tool, the write must land.
			if n > 0 && !fileExists {
				t.Errorf("allow: tool was approved but %s was not written", wantFile)
			}
		} else {
			// Deny is deterministic: every gated tool is refused, so the file
			// can never appear regardless of what Claude tried.
			if fileExists {
				t.Errorf("deny: %s was written despite denial", wantFile)
			}
		}
	}

	t.Run("allow", func(t *testing.T) {
		run(t, true, "Create a file named ok.txt containing exactly the word hello. Use the Write tool.", "ok.txt")
	})
	t.Run("deny", func(t *testing.T) {
		run(t, false, "Create a file named nope.txt containing exactly the word hello. Use the Write tool.", "nope.txt")
	})
}
