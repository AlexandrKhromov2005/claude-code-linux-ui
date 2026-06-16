package core_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/permctl"
)

// liveBroker answers approvals with a fixed verdict and counts invocations.
type liveBroker struct {
	allow bool
	calls atomic.Int32
}

func (b *liveBroker) RequestApproval(_ context.Context, _ core.ApprovalRequest) core.ApprovalDecision {
	b.calls.Add(1)
	return core.ApprovalDecision{Allow: b.allow, Message: "test"}
}

// TestLiveAppAgentTurn exercises the whole stack the refactor must preserve:
// App.SendTurn in agent mode -> engine -> permctl -> App.HandleApproval ->
// broker, plus transcript persistence. Skipped unless CLAUDE_LIVE=1.
func TestLiveAppAgentTurn(t *testing.T) {
	if os.Getenv("CLAUDE_LIVE") == "" {
		t.Skip("set CLAUDE_LIVE=1 to run the live full-stack agent test")
	}
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}

	run := func(t *testing.T, allow bool, wantFile string) {
		proj := t.TempDir()
		t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "cfg"))
		t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))

		store, err := core.NewStore()
		if err != nil {
			t.Fatalf("NewStore: %v", err)
		}
		cfg, _ := store.LoadConfig()
		app := core.NewApp(store, cfg, &core.Engine{BinPath: bin, Mode: core.ModeChat})

		perm := permctl.New(app.HandleApproval)
		if err := perm.Start(); err != nil {
			t.Fatalf("perm start: %v", err)
		}
		defer perm.Stop()
		app.SetPermission(perm)
		broker := &liveBroker{allow: allow}
		app.SetBroker(broker)

		if _, err := app.UseCwd(proj); err != nil {
			t.Fatalf("UseCwd: %v", err)
		}
		app.SetMode(core.ModeAgent)

		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		ch, err := app.SendTurn(ctx, "Create a file named "+wantFile+" containing the word hi. Use the Write tool.", nil)
		if err != nil {
			t.Fatalf("SendTurn: %v", err)
		}
		for range ch {
		}

		_, statErr := os.Stat(filepath.Join(proj, wantFile))
		fileExists := statErr == nil
		if allow {
			// Only meaningful once Claude actually requested (and we granted) a tool.
			if broker.calls.Load() > 0 && !fileExists {
				t.Errorf("allow: approved but %s was not written", wantFile)
			}
		} else if fileExists {
			t.Errorf("deny: %s should not exist", wantFile)
		}

		// If Claude actually requested approval, the decision must be recorded in
		// the transcript. (Claude may occasionally decline to attempt the tool,
		// in which case there is nothing to record.)
		var toolEntries int
		for _, m := range app.CurrentThread().Messages {
			if m.Role == "tool" {
				toolEntries++
			}
		}
		if broker.calls.Load() > 0 && toolEntries == 0 {
			t.Errorf("approval was requested %d time(s) but none recorded in transcript", broker.calls.Load())
		}
		t.Logf("broker calls=%d, tool entries=%d", broker.calls.Load(), toolEntries)
	}

	t.Run("allow", func(t *testing.T) { run(t, true, "app_allow.txt") })
	t.Run("deny", func(t *testing.T) { run(t, false, "app_deny.txt") })
}
