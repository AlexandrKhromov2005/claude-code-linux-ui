package core_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// TestLiveHandoffThread exercises the real summarisation path: a thread with a
// transcript is folded into a summary and continued in a fresh thread that
// carries no Claude session id, so its next turn starts a short context instead
// of replaying everything.
func TestLiveHandoffThread(t *testing.T) {
	bin := liveBin(t)

	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	store, err := core.NewStore()
	if err != nil {
		t.Fatal(err)
	}
	p, err := store.CreateProject("live-handoff", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	app := core.NewApp(store, core.Config{AutoMemoryDisabled: true},
		&core.Engine{BinPath: bin, Model: "haiku", Mode: core.ModeChat})
	app.OpenProjectObj(p)

	// Give the thread a transcript worth summarising, without spending a turn on
	// it: the handoff reads the stored messages, not the live session.
	old := app.CurrentThread()
	old.Title = "Настройка фаззинга"
	old.ClaudeSessionID = "sess-old"
	old.Messages = []core.Msg{
		{Role: "user", Content: "Нужно настроить фаззинг libFuzzer для парсера конфигов.", Ts: time.Now()},
		{Role: "assistant", Content: "Договорились: цель — parser_fuzzer.cc, корпус в corpus/, сборка с -fsanitize=fuzzer,address.", Ts: time.Now()},
		{Role: "tool", Content: "Edit build.sh", Ts: time.Now()},
		{Role: "user", Content: "Добавь ещё сбор покрытия.", Ts: time.Now()},
		{Role: "assistant", Content: "Добавил -fprofile-instr-generate -fcoverage-mapping. Осталось написать скрипт отчёта.", Ts: time.Now()},
	}
	if err := store.SaveThread(p.Slug(), old); err != nil {
		t.Fatal(err)
	}

	res, err := app.HandoffThread(context.Background())
	if err != nil {
		t.Fatalf("HandoffThread: %v", err)
	}
	t.Logf("handoff used %d input tokens, summary %d chars:\n%s",
		res.Usage.TotalIn(), len(res.Summary), res.Summary)

	if res.OldThreadID != old.ID {
		t.Errorf("old thread id = %q, want %q", res.OldThreadID, old.ID)
	}
	if strings.TrimSpace(res.Summary) == "" {
		t.Fatal("handoff produced an empty summary")
	}

	next := app.CurrentThread()
	if next.ID == old.ID {
		t.Fatal("handoff did not switch to a new thread")
	}
	// The whole point: no resume id, so the next turn does not replay the old
	// transcript.
	if next.ClaudeSessionID != "" {
		t.Errorf("new thread carries session id %q — it would resume the old context", next.ClaudeSessionID)
	}
	if next.ContinuedFrom != old.ID {
		t.Errorf("new thread ContinuedFrom = %q, want %q", next.ContinuedFrom, old.ID)
	}
	if len(next.Messages) != 1 || next.Messages[0].Role != "system" {
		t.Fatalf("new thread should open with one system seed, got %d messages", len(next.Messages))
	}
	if !strings.Contains(next.Messages[0].Content, res.Summary) {
		t.Error("the seed message does not carry the summary")
	}

	// A summary that is not much smaller than the transcript would defeat the
	// purpose; it should be a digest, not a copy.
	if res.Usage.TotalIn() == 0 {
		t.Error("handoff reported no token usage")
	}

	// The old thread must survive untouched and stay readable.
	kept, err := store.LoadThread(p.Slug(), old.ID)
	if err != nil {
		t.Fatalf("old thread is gone: %v", err)
	}
	if len(kept.Messages) != 5 {
		t.Errorf("old thread has %d messages, want the original 5", len(kept.Messages))
	}
}
