package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestLiveClaudeSettingsRules verifies that remembered allow rules pre-approve a
// tool (the modal is never consulted) and that deny rules win over allow. Gated
// on CLAUDE_LIVE like the other live test.
func TestLiveClaudeSettingsRules(t *testing.T) {
	if os.Getenv("CLAUDE_LIVE") == "" {
		t.Skip("set CLAUDE_LIVE=1 to run the live settings-rules test")
	}
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}

	run := func(t *testing.T, settings string, deciderAllow bool, wantFile string, wantCalled, wantFileExists bool) {
		dir := t.TempDir()

		var mu sync.Mutex
		called := 0
		srv := NewPermissionServer(func(req ApprovalRequest) ApprovalDecision {
			mu.Lock()
			called++
			mu.Unlock()
			return ApprovalDecision{Allow: deciderAllow, Message: "test"}
		})
		if err := srv.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		defer srv.Stop()

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin,
			"-p", "Create a file named "+wantFile+" containing the word hi. Use the Write tool.",
			"--output-format", "stream-json", "--verbose",
			"--permission-mode", "default",
			"--permission-prompt-tool", permPromptTool(),
			"--mcp-config", srv.MCPConfigJSON(),
			"--settings", settings,
		)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil && ctx.Err() != nil {
			t.Fatalf("timeout: %v\n%s", err, out)
		}

		mu.Lock()
		gotCalled := called
		mu.Unlock()
		if wantCalled && gotCalled == 0 {
			t.Errorf("expected approve to be consulted, but it was not\n%s", out)
		}
		if !wantCalled && gotCalled != 0 {
			t.Errorf("approve was consulted %d time(s) but a settings rule should have decided", gotCalled)
		}
		_, statErr := os.Stat(filepath.Join(dir, wantFile))
		if wantFileExists && statErr != nil {
			t.Errorf("expected %s to exist: %v", wantFile, statErr)
		}
		if !wantFileExists && statErr == nil {
			t.Errorf("%s should not exist", wantFile)
		}
	}

	t.Run("remembered_allow_skips_prompt", func(t *testing.T) {
		// Allow rule pre-approves Write; the decider would deny, proving it is
		// never consulted.
		run(t, `{"permissions":{"allow":["Write"]}}`, false, "allowed.txt", false, true)
	})

	t.Run("deny_beats_allow", func(t *testing.T) {
		// Deny wins over allow; the decider would allow, proving it is bypassed.
		run(t, `{"permissions":{"allow":["Write"],"deny":["Write"]}}`, true, "denied.txt", false, false)
	})
}
