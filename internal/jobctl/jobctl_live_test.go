package jobctl

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// liveRunner backs the MCP server with a real supervisor, mirroring what the
// app wires in production closely enough to exercise the transport contract.
type liveRunner struct {
	mgr *core.JobManager
	dir string
}

func (r *liveRunner) StartJob(command, description string) (*core.Job, error) {
	return r.mgr.Start(core.StartSpec{Command: command, Description: description, Dir: r.dir})
}

func (r *liveRunner) AwaitJob(ctx context.Context, id string, timeoutSeconds int) (string, error) {
	timeout := time.Duration(timeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	wctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	j, done, err := r.mgr.Await(wctx, id)
	if err != nil {
		return "", err
	}
	if !done {
		v, _ := r.mgr.View(id)
		return core.JobWaitPending(v), nil
	}
	r.mgr.MarkNotified(id)
	tail, _ := r.mgr.Logs(id, 40)
	return core.JobWaitResult(j, tail), nil
}

func (r *liveRunner) Jobs() *core.JobManager { return r.mgr }

// TestLiveClaudeJobWait drives the real `claude` binary end to end: the model
// hands a command to the supervisor, parks on wait until it finishes, and
// reports. The command sleeps 70 seconds on purpose — past the CLI's 60-second
// default cutoff for an HTTP MCP call, so the test fails if the per-server
// timeout or the SSE keepalives ever stop doing their job. Skipped unless
// CLAUDE_LIVE=1.
func TestLiveClaudeJobWait(t *testing.T) {
	if os.Getenv("CLAUDE_LIVE") == "" {
		t.Skip("set CLAUDE_LIVE=1 to run the live claude job-wait test")
	}
	bin := os.Getenv("CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}

	mgr, err := core.NewJobManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	go mgr.Watch()
	t.Cleanup(mgr.Close)

	workDir := t.TempDir()
	srv := New(&liveRunner{mgr: mgr, dir: workDir})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	defer srv.Stop()

	cfg, err := json.Marshal(map[string]any{"mcpServers": srv.MCPServers()})
	if err != nil {
		t.Fatal(err)
	}

	prompt := "Запусти фоновую задачу командой `sleep 70; echo done-marker` через инструмент " +
		"mcp__jobs__start. Затем вызови mcp__jobs__wait с полученным id — задача идёт около минуты, " +
		"дождись её завершения. В конце сообщи одним предложением, с каким кодом она завершилась."

	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin,
		"-p", prompt,
		"--output-format", "json",
		"--model", "haiku",
		"--strict-mcp-config",
		"--mcp-config", string(cfg),
		"--allowedTools", "mcp__jobs__start,mcp__jobs__wait,mcp__jobs__status,mcp__jobs__logs",
		"--permission-mode", "default",
	)
	cmd.Dir = workDir
	out, err := cmd.Output()
	t.Logf("claude output: %s", out)
	if err != nil {
		t.Fatalf("claude failed: %v", err)
	}

	jobs := mgr.List()
	if len(jobs) != 1 {
		t.Fatalf("supervisor holds %d jobs, want the 1 the model started", len(jobs))
	}
	j := jobs[0]
	if j.Status != core.JobDone {
		t.Errorf("job status = %q (exit %d), want done", j.Status, j.ExitCode)
	}
	// Notified proves the ending went out through wait, in-turn — not through a
	// wake-up that has no one to receive it here.
	if !j.Notified {
		t.Error("job ending was not collected by the wait call")
	}
	if d := j.EndedAt.Sub(j.StartedAt); d < 65*time.Second {
		t.Errorf("job ran %v — the model cannot have waited through the sleep", d)
	}
}
