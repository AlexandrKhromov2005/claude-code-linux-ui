package core

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"
)

// runningJob registers a job in the running state without a real process, so a
// test can drive its ending by hand.
func runningJob(m *JobManager, id, slug, threadID string) *Job {
	j := &Job{
		ID: id, Command: "make build", Description: "сборка",
		ProjectSlug: slug, ThreadID: threadID,
		Status: JobRunning, StartedAt: time.Now().Add(-time.Minute),
	}
	m.mu.Lock()
	m.jobs[id] = j
	m.mu.Unlock()
	m.persist(j)
	return j
}

func TestAwaitReturnsFinishedJobImmediately(t *testing.T) {
	m := newTestJobs(t)
	finishedJob(m, "job-done", "p", "t")

	j, done, err := m.Await(context.Background(), "job-done")
	if err != nil || !done {
		t.Fatalf("Await = (%v, done=%v), want the finished job at once", err, done)
	}
	if j.Status != JobDone {
		t.Errorf("status = %q, want %q", j.Status, JobDone)
	}
}

func TestAwaitUnknownJobIsAnError(t *testing.T) {
	m := newTestJobs(t)
	if _, _, err := m.Await(context.Background(), "job-ghost"); err == nil {
		t.Error("awaiting a job that does not exist should fail, not park forever")
	}
}

// The core of the mechanism: a parked waiter is released the moment the job
// ends, with the final state in hand.
func TestAwaitWakesOnFinish(t *testing.T) {
	m := newTestJobs(t)
	runningJob(m, "job-r", "p", "t")

	type res struct {
		j    Job
		done bool
	}
	got := make(chan res, 1)
	go func() {
		j, done, _ := m.Await(context.Background(), "job-r")
		got <- res{j, done}
	}()
	// Give the waiter a moment to park before the ending lands.
	time.Sleep(20 * time.Millisecond)
	m.finish("job-r", JobFailed, 2)

	select {
	case r := <-got:
		if !r.done || r.j.Status != JobFailed || r.j.ExitCode != 2 {
			t.Errorf("waiter got %+v done=%v, want the failed ending", r.j, r.done)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter was never released by the finish")
	}
}

func TestAwaitTimesOutWhileJobRuns(t *testing.T) {
	m := newTestJobs(t)
	runningJob(m, "job-slow", "p", "t")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	j, done, err := m.Await(ctx, "job-slow")
	if err != nil {
		t.Fatal(err)
	}
	if done {
		t.Fatal("a running job was reported as finished")
	}
	if j.Status != JobRunning {
		t.Errorf("status = %q, want still running", j.Status)
	}
	// The expired waiter must be gone, or every timed-out wait would leak a
	// registration that later swallows the notification silently.
	m.mu.Lock()
	n := len(m.waiters["job-slow"])
	m.mu.Unlock()
	if n != 0 {
		t.Errorf("%d waiters still registered after the timeout", n)
	}
}

// Handing the ending to a live waiter IS the report, so the finish hook must
// see the job already latched — otherwise the wake-up path would announce the
// same result a second time and spend a turn restating it.
func TestFinishWithWaiterLatchesNotified(t *testing.T) {
	m := newTestJobs(t)
	var mu sync.Mutex
	var hook []Job
	m.SetHooks(nil, func(j Job) {
		mu.Lock()
		hook = append(hook, j)
		mu.Unlock()
	})
	runningJob(m, "job-w", "p", "t")

	done := make(chan Job, 1)
	go func() {
		j, _, _ := m.Await(context.Background(), "job-w")
		done <- j
	}()
	time.Sleep(20 * time.Millisecond)
	m.finish("job-w", JobDone, 0)

	j := <-done
	if !j.Notified {
		t.Error("the ending handed to a waiter was not latched as notified")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hook) != 1 || !hook[0].Notified {
		t.Errorf("finish hook saw %+v, want exactly one call with Notified=true", hook)
	}
	// And the latch must be on disk, so a restart does not re-announce it.
	got, _ := m.Get("job-w")
	if !got.Notified {
		t.Error("latch did not stick on the stored job")
	}
}

// Without a waiter nothing is latched: the ending still belongs to the wake-up
// path.
func TestFinishWithoutWaiterStaysUnlatched(t *testing.T) {
	m := newTestJobs(t)
	runningJob(m, "job-n", "p", "t")
	m.finish("job-n", JobDone, 0)
	if got, _ := m.Get("job-n"); got.Notified {
		t.Error("an ending nobody waited for was latched, so it will never be announced")
	}
}

// ---- App-level wait ---------------------------------------------------------

func TestAwaitJobRendersResultAndLatches(t *testing.T) {
	app, mgr, d, p := jobAppFixture(t)
	finishedJob(mgr, "job-a", p.Slug(), "thread-1")

	text, err := app.AwaitJob(context.Background(), "job-a", 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"job-a", "успешно", "отдельного уведомления не будет"} {
		if !strings.Contains(text, want) {
			t.Errorf("wait result missing %q:\n%s", want, text)
		}
	}
	if got, _ := mgr.Get("job-a"); !got.Notified {
		t.Error("a result collected through wait was not latched")
	}
	// Nothing else may announce it now.
	app.DeliverPendingJobNotices()
	if d.count() != 0 {
		t.Errorf("a waited-for job was re-announced %d times", d.count())
	}
}

func TestAwaitJobTimeoutRendersProgress(t *testing.T) {
	app, mgr, _, p := jobAppFixture(t)
	runningJob(mgr, "job-run", p.Slug(), "thread-1")

	text, err := app.AwaitJob(context.Background(), "job-run", 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ещё выполняется", "wait"} {
		if !strings.Contains(text, want) {
			t.Errorf("pending text missing %q:\n%s", want, text)
		}
	}
	if got, _ := mgr.Get("job-run"); got.Notified {
		t.Error("a timed-out wait latched the job — its real ending is now lost")
	}
}

// The read-only job tools must ride as implicit allows in agent mode: an
// unattended wait cannot stop at a modal nobody is around to answer. The
// mutating tools must not be granted, and the project's own rules on disk must
// stay untouched.
func TestAgentSettingsPreallowReadOnlyJobTools(t *testing.T) {
	app, _, _, p := jobAppFixture(t)
	app.mu.Lock()
	app.jobMCP = map[string]json.RawMessage{"jobs": json.RawMessage(`{}`)}
	app.mu.Unlock()
	app.SetMode(ModeAgent)

	app.mu.Lock()
	settings := app.engine.SettingsJSON
	app.mu.Unlock()
	for _, want := range []string{"mcp__jobs__wait", "mcp__jobs__status", "mcp__jobs__logs"} {
		if !strings.Contains(settings, want) {
			t.Errorf("agent settings missing implicit allow %q:\n%s", want, settings)
		}
	}
	for _, gated := range []string{"mcp__jobs__start", "mcp__jobs__stop"} {
		if strings.Contains(settings, gated) {
			t.Errorf("mutating tool %q was pre-allowed", gated)
		}
	}
	if len(p.Permissions.Allow) != 0 {
		t.Errorf("implicit allows leaked into the project rules: %v", p.Permissions.Allow)
	}
}

func TestChatSettingsCarryNoJobAllows(t *testing.T) {
	app, _, _, _ := jobAppFixture(t)
	app.mu.Lock()
	app.jobMCP = map[string]json.RawMessage{"jobs": json.RawMessage(`{}`)}
	app.mu.Unlock()
	app.SetMode(ModeChat)

	app.mu.Lock()
	settings := app.engine.SettingsJSON
	app.mu.Unlock()
	if strings.Contains(settings, "mcp__jobs__") {
		t.Errorf("chat settings mention job tools:\n%s", settings)
	}
}

func TestJobsGuidanceTeachesWait(t *testing.T) {
	if !strings.Contains(JobsGuidance, "mcp__jobs__wait") {
		t.Error("guidance does not mention wait — a subagent has no other way to learn it may block")
	}
	if !strings.Contains(JobsGuidance, "mcp__jobs__start") {
		t.Error("guidance lost the start tool")
	}
}
