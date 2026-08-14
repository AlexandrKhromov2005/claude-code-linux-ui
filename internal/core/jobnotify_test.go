package core

import (
	"sync"
	"testing"
	"time"
)

// recordingDispatcher captures the wake-up turns a finished job would start.
type recordingDispatcher struct {
	mu    sync.Mutex
	calls []struct{ threadID, text string }
}

func (d *recordingDispatcher) fn(threadID, text string) {
	d.mu.Lock()
	d.calls = append(d.calls, struct{ threadID, text string }{threadID, text})
	d.mu.Unlock()
}

func (d *recordingDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.calls)
}

// jobAppFixture builds an app with a job supervisor and a recording dispatcher.
func jobAppFixture(t *testing.T) (*App, *JobManager, *recordingDispatcher, *Project) {
	t.Helper()
	store := newTestStore(t)
	p, err := store.CreateProject("demo", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, Config{}, &Engine{BinPath: "claude"})
	mgr, err := NewJobManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	d := &recordingDispatcher{}
	app.SetJobs(mgr, nil, nil)
	app.SetTurnDispatcher(d.fn)
	app.OpenProjectObj(p)
	return app, mgr, d, p
}

// finishedJob registers a terminal job directly, standing in for one the
// supervisor watched to completion.
func finishedJob(m *JobManager, id, slug, threadID string) Job {
	j := &Job{
		ID: id, Command: "make fuzz", Description: "прогон",
		ProjectSlug: slug, ThreadID: threadID,
		Status: JobDone, StartedAt: time.Now().Add(-time.Minute), EndedAt: time.Now(),
	}
	m.mu.Lock()
	m.jobs[id] = j
	m.mu.Unlock()
	m.persist(j)
	return *j
}

func TestJobFinishWakesTheOpenProject(t *testing.T) {
	app, mgr, d, p := jobAppFixture(t)
	th := app.CurrentThread()

	app.onJobFinished(finishedJob(mgr, "job-a", p.Slug(), th.ID))

	if d.count() != 1 {
		t.Fatalf("dispatched %d turns, want 1", d.count())
	}
	got, _ := mgr.Get("job-a")
	if !got.Notified {
		t.Error("job was not latched as notified after a successful dispatch")
	}
}

// The engine is configured for one project at a time, so a job belonging to a
// different one cannot start a turn now — but its result must not be thrown
// away either. It stays unlatched, waiting.
func TestJobFinishDefersWhenProjectNotOpen(t *testing.T) {
	app, mgr, d, _ := jobAppFixture(t)
	th := app.CurrentThread()

	app.onJobFinished(finishedJob(mgr, "job-elsewhere", "some-other-project", th.ID))

	if d.count() != 0 {
		t.Fatalf("dispatched %d turns for a project that is not open, want 0", d.count())
	}
	got, _ := mgr.Get("job-elsewhere")
	if got.Notified {
		t.Error("job was latched as notified even though nothing was delivered — the result is now lost")
	}
}

// A restarted server has no project open at all. Same rule: defer, do not drop.
func TestJobFinishDefersWithNoProjectOpen(t *testing.T) {
	store := newTestStore(t)
	p, err := store.CreateProject("demo", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, Config{}, &Engine{BinPath: "claude"})
	mgr, err := NewJobManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	d := &recordingDispatcher{}
	app.SetJobs(mgr, nil, nil)
	app.SetTurnDispatcher(d.fn)
	// deliberately no OpenProjectObj — this is the just-restarted state

	app.onJobFinished(finishedJob(mgr, "job-orphan", p.Slug(), "thread-1"))

	if d.count() != 0 {
		t.Fatalf("dispatched %d turns with no project open, want 0", d.count())
	}
	if got, _ := mgr.Get("job-orphan"); got.Notified {
		t.Error("job was retired without ever being reported")
	}

	// Opening the project is what finally delivers it.
	app.OpenProjectObj(p)
	app.DeliverPendingJobNotices()
	if d.count() != 1 {
		t.Fatalf("after opening the project, dispatched %d turns, want 1", d.count())
	}
	if got, _ := mgr.Get("job-orphan"); !got.Notified {
		t.Error("delivered job was not latched")
	}
}

// Delivery must not repeat: a second open of the same project is silent.
func TestPendingNoticesDeliverOnlyOnce(t *testing.T) {
	app, mgr, d, p := jobAppFixture(t)
	finishedJob(mgr, "job-p", p.Slug(), "thread-1")

	app.DeliverPendingJobNotices()
	app.DeliverPendingJobNotices()

	if d.count() != 1 {
		t.Fatalf("dispatched %d turns, want exactly 1", d.count())
	}
}

// A project left alone for a week must not wake up to a queue of turns, each
// costing tokens for work the user has moved past.
func TestPendingNoticesAreCapped(t *testing.T) {
	app, mgr, d, p := jobAppFixture(t)
	for i := range 10 {
		finishedJob(mgr, "job-"+string(rune('a'+i)), p.Slug(), "thread-1")
	}

	app.DeliverPendingJobNotices()

	if d.count() != maxPendingJobNotices {
		t.Fatalf("dispatched %d turns, want the cap of %d", d.count(), maxPendingJobNotices)
	}
	// Everything else is retired quietly rather than left to resurface later.
	app.DeliverPendingJobNotices()
	if d.count() != maxPendingJobNotices {
		t.Fatalf("suppressed notices came back on a later open: %d total", d.count())
	}
}

// With reporting switched off, a job is retired without a turn and never
// resurfaces.
func TestJobNotifyDisabledRetiresSilently(t *testing.T) {
	app, mgr, d, p := jobAppFixture(t)
	app.mu.Lock()
	app.cfg.JobNotifyDisabled = true
	app.mu.Unlock()

	app.onJobFinished(finishedJob(mgr, "job-quiet", p.Slug(), "thread-1"))

	if d.count() != 0 {
		t.Fatalf("dispatched %d turns with notifications off, want 0", d.count())
	}
	if got, _ := mgr.Get("job-quiet"); !got.Notified {
		t.Error("job should be retired, not left pending, when reporting is off")
	}
	app.DeliverPendingJobNotices()
	if d.count() != 0 {
		t.Error("a retired job resurfaced on project open")
	}
}

// A job with no thread has nowhere to report and must not dispatch.
func TestJobWithoutThreadIsNotDispatched(t *testing.T) {
	app, mgr, d, p := jobAppFixture(t)
	app.onJobFinished(finishedJob(mgr, "job-nothread", p.Slug(), ""))
	if d.count() != 0 {
		t.Fatalf("dispatched %d turns for a job with no thread, want 0", d.count())
	}
}

// An already-notified job is never announced twice, whichever path reaches it.
func TestAlreadyNotifiedJobIsIgnored(t *testing.T) {
	app, mgr, d, p := jobAppFixture(t)
	j := finishedJob(mgr, "job-done", p.Slug(), "thread-1")
	mgr.MarkNotified(j.ID)
	j.Notified = true

	app.onJobFinished(j)
	app.DeliverPendingJobNotices()

	if d.count() != 0 {
		t.Fatalf("re-announced an already reported job %d times", d.count())
	}
}
