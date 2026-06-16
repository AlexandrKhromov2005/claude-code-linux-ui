package main

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestLiveClaudeApproval drives the real `claude` binary against the in-process
// permission server to confirm the MCP transport and allow/deny contract end to
// end. It is skipped unless CLAUDE_LIVE=1 (it needs an authenticated CLI and
// network), so normal `go test` stays hermetic.
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
		var calls []ApprovalRequest
		srv := NewPermissionServer(func(req ApprovalRequest) ApprovalDecision {
			mu.Lock()
			calls = append(calls, req)
			mu.Unlock()
			return ApprovalDecision{Allow: allow, Message: "denied by test"}
		})
		if err := srv.Start(); err != nil {
			t.Fatalf("start server: %v", err)
		}
		defer srv.Stop()

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()

		args := []string{
			"-p", prompt,
			"--output-format", "stream-json", "--verbose", "--include-partial-messages",
			"--permission-mode", "default",
			"--permission-prompt-tool", permPromptTool(),
			"--mcp-config", srv.MCPConfigJSON(),
		}
		cmd := exec.CommandContext(ctx, bin, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil && ctx.Err() != nil {
			t.Fatalf("claude timed out: %v\n%s", err, out)
		}

		mu.Lock()
		n := len(calls)
		var names []string
		for _, c := range calls {
			names = append(names, c.ToolName)
		}
		mu.Unlock()
		t.Logf("approve called %d time(s): %v", n, names)
		if n == 0 {
			t.Fatalf("permission tool was never called\noutput:\n%s", out)
		}
		// The approve arguments must carry a tool name and JSON input object.
		for _, c := range calls {
			if c.ToolName == "" {
				t.Errorf("approval missing tool_name")
			}
			if len(c.Input) > 0 && !json.Valid(c.Input) {
				t.Errorf("approval input is not valid JSON: %s", c.Input)
			}
		}

		_, statErr := os.Stat(filepath.Join(dir, wantFile))
		if allow && statErr != nil {
			t.Errorf("allow: expected %s to be written, but: %v", wantFile, statErr)
		}
		if !allow && statErr == nil {
			t.Errorf("deny: %s was written despite denial", wantFile)
		}
	}

	t.Run("allow", func(t *testing.T) {
		run(t, true, "Create a file named ok.txt containing exactly the word hello. Use the Write tool.", "ok.txt")
	})
	t.Run("deny", func(t *testing.T) {
		run(t, false, "Create a file named nope.txt containing exactly the word hello. Use the Write tool.", "nope.txt")
	})
}
