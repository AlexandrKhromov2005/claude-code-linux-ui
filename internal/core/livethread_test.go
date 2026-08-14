package core

import (
	"testing"
	"time"
)

// A thread with a turn in flight is held in memory and appended to as the turn
// streams. Resolving it by id must hand back that same object: loading a second
// copy from disk forks the transcript, and whichever copy saves last erases the
// other's messages. This is reachable in normal use — switching threads while
// one is working, or a background job reporting into a thread the user is not
// looking at.
func TestLiveThreadIsSharedNotReloaded(t *testing.T) {
	store := newTestStore(t)
	p, err := store.CreateProject("demo", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, Config{}, &Engine{BinPath: "claude"})
	app.OpenProjectObj(p)

	th := app.CurrentThread()
	th.Messages = append(th.Messages, Msg{Role: "user", Content: "первый", Ts: time.Now()})
	if err := store.SaveThread(p.Slug(), th); err != nil {
		t.Fatal(err)
	}

	// Simulate a turn holding the thread.
	app.mu.Lock()
	app.retainThreadLocked(th)
	app.mu.Unlock()

	// Switch away, then resolve the busy thread by id.
	app.NewThread()
	got, err := app.threadFor(p.Slug(), th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != th {
		t.Fatal("resolving a busy thread loaded a second copy instead of sharing the live one")
	}

	// An append made through the live pointer must be visible to the other holder.
	got.Messages = append(got.Messages, Msg{Role: "assistant", Content: "второй", Ts: time.Now()})
	if len(th.Messages) != 2 {
		t.Fatalf("the two references diverged: live thread has %d messages", len(th.Messages))
	}

	// Opening it in the UI must also land on the live object.
	opened, err := app.OpenThread(th.ID)
	if err != nil {
		t.Fatal(err)
	}
	if opened != th {
		t.Fatal("OpenThread reloaded a busy thread from disk instead of sharing it")
	}

	// Once the turn releases it, a fresh load is correct again.
	app.releaseThread(th.ID)
	app.mu.Lock()
	stillLive := app.live[th.ID]
	app.mu.Unlock()
	if stillLive != nil {
		t.Error("thread stayed registered as live after its turn released it")
	}
}

// Two holds on the same thread — a re-send, or a job reporting back while the
// user is typing — must both have to release before it stops being shared.
func TestLiveThreadHoldsAreCounted(t *testing.T) {
	store := newTestStore(t)
	p, err := store.CreateProject("demo", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, Config{}, &Engine{BinPath: "claude"})
	app.OpenProjectObj(p)
	th := app.CurrentThread()

	app.mu.Lock()
	app.retainThreadLocked(th)
	app.retainThreadLocked(th)
	app.mu.Unlock()

	app.releaseThread(th.ID)
	app.mu.Lock()
	afterFirst := app.live[th.ID]
	app.mu.Unlock()
	if afterFirst != th {
		t.Fatal("thread stopped being shared while a second turn still held it")
	}

	app.releaseThread(th.ID)
	app.mu.Lock()
	afterSecond := app.live[th.ID]
	refs := app.liveRefs[th.ID]
	app.mu.Unlock()
	if afterSecond != nil || refs != 0 {
		t.Fatalf("thread stayed live after every hold released (refs=%d)", refs)
	}
}

// Resolving a thread that is not busy falls back to disk, and the open thread is
// returned directly rather than reloaded.
func TestThreadForFallsBackToDisk(t *testing.T) {
	store := newTestStore(t)
	p, err := store.CreateProject("demo", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, Config{}, &Engine{BinPath: "claude"})
	app.OpenProjectObj(p)

	current := app.CurrentThread()
	current.Messages = append(current.Messages, Msg{Role: "user", Content: "привет", Ts: time.Now()})
	if err := store.SaveThread(p.Slug(), current); err != nil {
		t.Fatal(err)
	}

	// The open thread comes back as the same object.
	got, err := app.threadFor(p.Slug(), current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != current {
		t.Error("the open thread was reloaded instead of returned directly")
	}

	// A different, idle thread is loaded from disk.
	app.NewThread()
	loaded, err := app.threadFor(p.Slug(), current.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded == nil || len(loaded.Messages) != 1 {
		t.Fatalf("idle thread did not load from disk: %+v", loaded)
	}
}
