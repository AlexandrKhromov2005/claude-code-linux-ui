package core

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
	cfg.BudgetWarnUSD = 2.5
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
}

func TestUniqueSlug(t *testing.T) {
	s := newTestStore(t)
	a, _ := s.CreateProject("Dup", "/a")
	b, _ := s.CreateProject("Dup", "/b")
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
	created, _ := s.CreateProject("Here", "/here")
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
	p, _ := s.CreateProject("T", "/t")

	th := s.NewThread()
	th.Title = "hello"
	th.ClaudeSessionID = "sess-123"
	now := time.Now()
	th.Messages = []Msg{
		{Role: "user", Content: "hi", Attachments: []string{"/x/a.png"}, Ts: now},
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
	if len(got.Messages) != 2 || got.Messages[0].Content != "hi" {
		t.Fatalf("messages mismatch: %+v", got.Messages)
	}
	if len(got.Messages[0].Attachments) != 1 || got.Messages[0].Attachments[0] != "/x/a.png" {
		t.Fatalf("attachments lost: %+v", got.Messages[0])
	}
}

func TestListThreadsOrdering(t *testing.T) {
	s := newTestStore(t)
	p, _ := s.CreateProject("L", "/l")

	older := s.NewThread()
	older.Title = "older"
	_ = s.SaveThread(p.Slug(), older)
	time.Sleep(10 * time.Millisecond)
	newer := s.NewThread()
	newer.Title = "newer"
	_ = s.SaveThread(p.Slug(), newer)

	list, err := s.ListThreads(p.Slug())
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != 2 || list[0].Title != "newer" {
		t.Fatalf("ordering wrong: %+v", list)
	}

	if err := s.DeleteThread(p.Slug(), newer.ID); err != nil {
		t.Fatalf("DeleteThread: %v", err)
	}
	list, _ = s.ListThreads(p.Slug())
	if len(list) != 1 || list[0].Title != "older" {
		t.Fatalf("delete failed: %+v", list)
	}
}

func TestLegacyDirMigration(t *testing.T) {
	dir := t.TempDir()
	cfgHome := filepath.Join(dir, "config")
	dataHome := filepath.Join(dir, "data")
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("XDG_DATA_HOME", dataHome)

	// Seed a legacy claude-tui data dir with one project marker.
	legacy := filepath.Join(dataHome, legacyAppName, "projects", "demo")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "project.toml"), []byte("name='demo'\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := NewStore(); err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	// The project should now live under the new app name.
	moved := filepath.Join(dataHome, appName, "projects", "demo", "project.toml")
	if _, err := os.Stat(moved); err != nil {
		t.Fatalf("legacy data was not migrated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dataHome, legacyAppName)); !os.IsNotExist(err) {
		t.Fatalf("legacy dir should be gone after rename")
	}
}
