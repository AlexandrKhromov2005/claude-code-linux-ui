package permctl

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// mutatingTools is a broad set of tools that could create a file, denied
// together so the file invariant does not depend on which one Claude picks.
const mutatingTools = `"Write","Edit","MultiEdit","NotebookEdit","Bash"`

// TestLiveClaudeSettingsRules verifies the two deterministic settings
// guarantees: a pre-approved tool never reaches the approval prompt, and a deny
// rule wins over an allow rule (the decision is made by settings, not the
// broker). Gated on CLAUDE_LIVE.
func TestLiveClaudeSettingsRules(t *testing.T) {
	if os.Getenv("CLAUDE_LIVE") == "" {
		t.Skip("set CLAUDE_LIVE=1 to run the live settings-rules test")
	}
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}

	runClaude := func(t *testing.T, settings, wantFile string, deciderAllow bool) (consulted int, fileExists bool) {
		dir := t.TempDir()
		var mu sync.Mutex
		srv := New(func(req core.ApprovalRequest) core.ApprovalDecision {
			mu.Lock()
			consulted++
			mu.Unlock()
			return core.ApprovalDecision{Allow: deciderAllow, Message: "test"}
		})
		if err := srv.Start(); err != nil {
			t.Fatalf("start: %v", err)
		}
		defer srv.Stop()

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, bin,
			"-p", "Create a file named "+wantFile+" containing the word hi.",
			"--output-format", "stream-json", "--verbose",
			"--permission-mode", "default",
			"--permission-prompt-tool", srv.PromptTool(),
			"--mcp-config", srv.MCPConfigJSON(),
			"--settings", settings,
		)
		cmd.Dir = dir
		_ = cmd.Run()
		_, statErr := os.Stat(filepath.Join(dir, wantFile))
		mu.Lock()
		defer mu.Unlock()
		return consulted, statErr == nil
	}

	t.Run("allow_rule_skips_prompt", func(t *testing.T) {
		// All mutating tools are pre-approved; the broker would deny. Whatever
		// tool Claude reaches for, the prompt must never be consulted.
		settings := `{"permissions":{"allow":[` + mutatingTools + `]}}`
		consulted, created := runClaude(t, settings, "allowed.txt", false)
		if consulted != 0 {
			t.Errorf("pre-approved tools must not reach the prompt, consulted=%d", consulted)
		}
		t.Logf("file created=%v (best effort; depends on Claude attempting a tool)", created)
	})

	t.Run("deny_beats_allow", func(t *testing.T) {
		// Mutating tools are both allowed and denied; deny must win, so the
		// broker (which would allow) is never consulted and no file appears.
		settings := `{"permissions":{"allow":[` + mutatingTools + `],"deny":[` + mutatingTools + `]}}`
		consulted, created := runClaude(t, settings, "denied.txt", true)
		if consulted != 0 {
			t.Errorf("denied tools must not reach the prompt, consulted=%d", consulted)
		}
		if created {
			t.Errorf("denied.txt was created despite deny rules")
		}
	})
}
