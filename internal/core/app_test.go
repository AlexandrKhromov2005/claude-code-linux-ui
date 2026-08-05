package core

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeClaude writes a script that emits a fixed stream-json transcript, so the
// turn lifecycle can be tested without the real CLI.
func fakeClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "fake-claude.sh")
	script := `#!/bin/sh
printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-xyz","model":"test-model"}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello "}}}'
printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"world"}}}'
printf '%s\n' '{"type":"result","session_id":"sess-xyz","total_cost_usd":0.0123,"result":"Hello world"}'
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	store := newTestStore(t)
	cfg, _ := store.LoadConfig()
	engine := &Engine{BinPath: fakeClaude(t), Mode: ModeChat}
	return NewApp(store, cfg, engine)
}

// recordingClaude writes its CLI arguments to $ARGS_DUMP for the main streaming
// turn (identified by the stream-json output format) so a test can inspect the
// dispatched prompt. Side calls like memory summarization use --output-format
// json instead; for those it returns an error result and records nothing, so the
// post-turn memory upkeep cannot clobber the dump or rewrite memory.
func recordingClaude(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "rec-claude.sh")
	script := `#!/bin/sh
stream=0
for a in "$@"; do
  [ "$a" = "stream-json" ] && stream=1
done
if [ "$stream" = "1" ]; then
  [ -n "$ARGS_DUMP" ] && printf '%s\n' "$@" > "$ARGS_DUMP"
  printf '%s\n' '{"type":"system","subtype":"init","session_id":"sess-xyz","model":"test-model"}'
  printf '%s\n' '{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Hello world"}}}'
  printf '%s\n' '{"type":"result","session_id":"sess-xyz","total_cost_usd":0.01,"result":"Hello world"}'
else
  printf '%s\n' '{"result":"","is_error":true}'
fi
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestSendTurnSeedsAutoMemoryOnce verifies that the cross-thread auto-memory seed
// is prepended to the outgoing prompt on a thread's first turn only, that the
// thread latches as seeded, and that the stored transcript keeps the user's
// original text without the seed block.
func TestSendTurnSeedsAutoMemoryOnce(t *testing.T) {
	store := newTestStore(t)
	cfg, _ := store.LoadConfig()
	app := NewApp(store, cfg, &Engine{BinPath: recordingClaude(t), Mode: ModeChat})

	if _, err := app.UseCwd(t.TempDir()); err != nil {
		t.Fatalf("UseCwd: %v", err)
	}
	slug := app.CurrentProject().Slug()
	if err := store.WriteAutoMemory(slug, "- проект на Go"); err != nil {
		t.Fatalf("WriteAutoMemory: %v", err)
	}

	dump := filepath.Join(t.TempDir(), "args.txt")
	t.Setenv("ARGS_DUMP", dump)

	drain := func(text string) string {
		_, ch, err := app.SendTurn(context.Background(), text, nil)
		if err != nil {
			t.Fatalf("SendTurn: %v", err)
		}
		for range ch {
		}
		b, err := os.ReadFile(dump)
		if err != nil {
			t.Fatalf("read args dump: %v", err)
		}
		return string(b)
	}

	first := drain("привет один")
	if !strings.Contains(first, "ПАМЯТЬ ПРОЕКТА") || !strings.Contains(first, "проект на Go") {
		t.Fatalf("first turn prompt missing the memory seed:\n%s", first)
	}
	if !strings.Contains(first, "привет один") {
		t.Fatalf("first turn prompt missing the user text:\n%s", first)
	}
	if th := app.CurrentThread(); !th.AutoMemorySeeded {
		t.Fatalf("thread should latch as seeded after the first turn")
	}
	if got := app.CurrentThread().Messages[0].Content; got != "привет один" {
		t.Fatalf("user message should be stored verbatim, got %q", got)
	}

	second := drain("привет два")
	if strings.Contains(second, "ПАМЯТЬ ПРОЕКТА") {
		t.Fatalf("second turn must not re-seed memory:\n%s", second)
	}
	if !strings.Contains(second, "привет два") {
		t.Fatalf("second turn prompt missing the user text:\n%s", second)
	}
}

func TestSendTurnPersists(t *testing.T) {
	app := newTestApp(t)
	// The engine runs with cwd set to the project dir, so it must exist.
	if _, err := app.UseCwd(t.TempDir()); err != nil {
		t.Fatalf("UseCwd: %v", err)
	}

	_, ch, err := app.SendTurn(context.Background(), "hi there", nil)
	if err != nil {
		t.Fatalf("SendTurn: %v", err)
	}
	var kinds []EventKind
	var text string
	for ev := range ch {
		kinds = append(kinds, ev.Kind)
		if ev.Kind == EvText {
			text += ev.Text
		}
	}
	if text != "Hello world" {
		t.Fatalf("streamed text = %q", text)
	}

	th := app.CurrentThread()
	if len(th.Messages) != 2 {
		t.Fatalf("expected user+assistant persisted, got %d: %+v", len(th.Messages), th.Messages)
	}
	if th.Messages[0].Role != "user" || th.Messages[0].Content != "hi there" {
		t.Fatalf("user message wrong: %+v", th.Messages[0])
	}
	if th.Messages[1].Role != "assistant" || th.Messages[1].Content != "Hello world" {
		t.Fatalf("assistant message wrong: %+v", th.Messages[1])
	}
	if th.ClaudeSessionID != "sess-xyz" {
		t.Fatalf("session id not stored: %q", th.ClaudeSessionID)
	}
	if app.Cost() < 0.0123-1e-9 {
		t.Fatalf("cost not accumulated: %v", app.Cost())
	}

	// The transcript must survive a reload from disk.
	reloaded, err := app.store.LoadThread(app.CurrentProject().Slug(), th.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(reloaded.Messages) != 2 {
		t.Fatalf("persisted thread has %d messages", len(reloaded.Messages))
	}
}

func TestSendTurnNoProject(t *testing.T) {
	app := newTestApp(t)
	if _, _, err := app.SendTurn(context.Background(), "hi", nil); err != ErrNoProject {
		t.Fatalf("expected ErrNoProject, got %v", err)
	}
}

// stubBroker answers every request with a fixed decision.
type stubBroker struct{ dec ApprovalDecision }

func (b stubBroker) RequestApproval(_ context.Context, _ ApprovalRequest) ApprovalDecision {
	return b.dec
}

func TestHandleApprovalNoBrokerDenies(t *testing.T) {
	app := newTestApp(t)
	dec := app.HandleApproval(ApprovalRequest{ToolName: "Bash"})
	if dec.Allow {
		t.Fatalf("no broker must deny")
	}
}

func TestHandleApprovalRemembersRule(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.UseCwd(t.TempDir()); err != nil {
		t.Fatalf("UseCwd: %v", err)
	}
	app.SetBroker(stubBroker{dec: ApprovalDecision{Allow: true, RememberRule: "Bash(go test:*)"}})

	req := ApprovalRequest{ToolName: "Bash", Input: json.RawMessage(`{"command":"go test ./..."}`)}
	dec := app.HandleApproval(req)
	if !dec.Allow {
		t.Fatalf("expected allow")
	}

	p := app.CurrentProject()
	reloaded, _ := app.store.LoadProject(p.Slug())
	found := false
	for _, r := range reloaded.Permissions.Allow {
		if r == "Bash(go test:*)" {
			found = true
		}
	}
	if !found {
		t.Fatalf("remembered rule not persisted: %+v", reloaded.Permissions.Allow)
	}

	// The decision should be recorded as a tool entry on the thread.
	th := app.CurrentThread()
	if len(th.Messages) != 1 || th.Messages[0].Role != "tool" {
		t.Fatalf("approval not recorded: %+v", th.Messages)
	}
	if allow, _ := th.Messages[0].ToolMeta["allow"].(bool); !allow {
		t.Fatalf("tool meta allow flag missing")
	}
}

func TestSetModeWarnsWithoutPermission(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.UseCwd(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if warn := app.SetMode(ModeAgent); warn == "" {
		t.Fatalf("agent mode without permission server should warn")
	}
	if warn := app.SetMode(ModeChat); warn != "" {
		t.Fatalf("chat mode should not warn: %q", warn)
	}
}
