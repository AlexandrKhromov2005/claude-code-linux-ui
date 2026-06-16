package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newTestStore points the XDG paths at a temp dir so tests stay isolated.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	s, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func TestConfigRoundTrip(t *testing.T) {
	s := newTestStore(t)

	// Absent config returns defaults.
	cfg, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.ClaudeBin != "claude" || cfg.DefaultMode != "chat" {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}

	cfg.DefaultModel = "opus"
	cfg.LastProject = "demo"
	cfg.Theme = "dark"
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	got, err := s.LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if got != cfg {
		t.Fatalf("config round-trip mismatch:\n got %+v\nwant %+v", got, cfg)
	}
}

func TestProjectRoundTrip(t *testing.T) {
	s := newTestStore(t)

	p, err := s.CreateProject("My Project", "/tmp/work")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if p.Slug() != "my-project" {
		t.Fatalf("slug = %q, want my-project", p.Slug())
	}
	if _, err := os.Stat(s.MemoryPath(p.Slug())); err != nil {
		t.Fatalf("memory.md missing: %v", err)
	}
	if _, err := os.Stat(s.threadsDir(p.Slug())); err != nil {
		t.Fatalf("threads dir missing: %v", err)
	}

	p.Permissions.Allow = []string{"Bash(go test:*)", "Edit(./src/*)"}
	p.Permissions.Deny = []string{"Bash(rm:*)"}
	p.Model = "sonnet"
	if err := s.SaveProject(p); err != nil {
		t.Fatalf("SaveProject: %v", err)
	}

	got, err := s.LoadProject(p.Slug())
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if got.Name != "My Project" || got.Cwd != "/tmp/work" || got.Model != "sonnet" {
		t.Fatalf("scalar fields mismatch: %+v", got)
	}
	if len(got.Permissions.Allow) != 2 || got.Permissions.Allow[0] != "Bash(go test:*)" {
		t.Fatalf("allow rules lost: %+v", got.Permissions)
	}
	if len(got.Permissions.Deny) != 1 || got.Permissions.Deny[0] != "Bash(rm:*)" {
		t.Fatalf("deny rules lost: %+v", got.Permissions)
	}
	if got.Created.Unix() != p.Created.Unix() {
		t.Fatalf("created time changed: %v vs %v", got.Created, p.Created)
	}
}

func TestUniqueSlug(t *testing.T) {
	s := newTestStore(t)

	a, err := s.CreateProject("Dup", "/a")
	if err != nil {
		t.Fatalf("CreateProject a: %v", err)
	}
	b, err := s.CreateProject("Dup", "/b")
	if err != nil {
		t.Fatalf("CreateProject b: %v", err)
	}
	if a.Slug() == b.Slug() {
		t.Fatalf("slugs collided: %q", a.Slug())
	}
	if b.Slug() != "dup-2" {
		t.Fatalf("second slug = %q, want dup-2", b.Slug())
	}
}

func TestProjectForCwd(t *testing.T) {
	s := newTestStore(t)
	if p, _ := s.ProjectForCwd("/nope"); p != nil {
		t.Fatalf("expected no project for unknown cwd")
	}
	created, err := s.CreateProject("Here", "/here")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	got, err := s.ProjectForCwd("/here")
	if err != nil {
		t.Fatalf("ProjectForCwd: %v", err)
	}
	if got == nil || got.Slug() != created.Slug() {
		t.Fatalf("ProjectForCwd mismatch: %+v", got)
	}
}

func TestThreadRoundTrip(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProject("T", "/t")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	th := s.NewThread()
	th.Title = "hello"
	th.ClaudeSessionID = "sess-123"
	now := time.Now()
	th.Messages = []Msg{
		{Role: "user", Content: "hi", Ts: now},
		{Role: "assistant", Content: "hey", Ts: now, ToolMeta: map[string]any{"tool": "Bash"}},
	}
	if err := s.SaveThread(p.Slug(), th); err != nil {
		t.Fatalf("SaveThread: %v", err)
	}

	got, err := s.LoadThread(p.Slug(), th.ID)
	if err != nil {
		t.Fatalf("LoadThread: %v", err)
	}
	if got.Title != "hello" || got.ClaudeSessionID != "sess-123" {
		t.Fatalf("thread scalars mismatch: %+v", got)
	}
	if len(got.Messages) != 2 || got.Messages[0].Content != "hi" || got.Messages[1].Role != "assistant" {
		t.Fatalf("messages mismatch: %+v", got.Messages)
	}
	if got.Messages[1].ToolMeta["tool"] != "Bash" {
		t.Fatalf("tool_meta lost: %+v", got.Messages[1].ToolMeta)
	}
	if !got.Messages[0].Ts.Equal(now) {
		t.Fatalf("timestamp changed: %v vs %v", got.Messages[0].Ts, now)
	}
}

func TestListThreadsOrdering(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProject("L", "/l")
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	older := s.NewThread()
	older.Title = "older"
	if err := s.SaveThread(p.Slug(), older); err != nil {
		t.Fatalf("save older: %v", err)
	}
	// Force a later Updated on the second thread.
	time.Sleep(10 * time.Millisecond)
	newer := s.NewThread()
	newer.Title = "newer"
	if err := s.SaveThread(p.Slug(), newer); err != nil {
		t.Fatalf("save newer: %v", err)
	}

	list, err := s.ListThreads(p.Slug())
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("got %d threads, want 2", len(list))
	}
	if list[0].Title != "newer" {
		t.Fatalf("ordering wrong: first is %q, want newer", list[0].Title)
	}

	if err := s.DeleteThread(p.Slug(), newer.ID); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}
	list, _ = s.ListThreads(p.Slug())
	if len(list) != 1 || list[0].Title != "older" {
		t.Fatalf("delete failed: %+v", list)
	}
}
