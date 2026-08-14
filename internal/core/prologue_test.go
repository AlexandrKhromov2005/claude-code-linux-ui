package core

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The CLI refuses --append-system-prompt together with --append-system-prompt-file,
// so the standing instructions and the user's own memory have to share one file.
func TestRuntimeMemoryCombinesPrologueAndUserMemory(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProject("Mem", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	slug := p.Slug()
	if err := s.WriteMemory(slug, "Заметка пользователя"); err != nil {
		t.Fatal(err)
	}
	if err := s.RegenRuntimeMemory(slug, JobsGuidance); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.RuntimeMemoryPath(slug))
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	if !strings.Contains(out, "mcp__jobs__start") {
		t.Error("standing instructions missing from the injected file")
	}
	if !strings.Contains(out, "Заметка пользователя") {
		t.Error("user memory missing from the injected file")
	}
	// The instructions come first, so the user's own note reads as an addition to
	// them rather than being buried under them.
	if strings.Index(out, "mcp__jobs__start") > strings.Index(out, "Заметка пользователя") {
		t.Error("user memory should follow the standing instructions, not precede them")
	}
}

// The injected file sits at the front of the cached prefix, so rewriting it with
// identical content would be a needless cache risk. Regenerating unchanged input
// must leave the file exactly as it was.
func TestRegenRuntimeMemoryIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProject("Mem", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	slug := p.Slug()
	if err := s.WriteMemory(slug, "заметка"); err != nil {
		t.Fatal(err)
	}
	if err := s.RegenRuntimeMemory(slug, JobsGuidance); err != nil {
		t.Fatal(err)
	}
	path := s.RuntimeMemoryPath(slug)
	first, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	for range 3 {
		if err := s.RegenRuntimeMemory(slug, JobsGuidance); err != nil {
			t.Fatal(err)
		}
	}
	second, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)

	if string(before) != string(after) {
		t.Error("content changed across identical regenerations")
	}
	if !first.ModTime().Equal(second.ModTime()) {
		t.Error("file was rewritten even though nothing changed")
	}
}

func TestRegenRuntimeMemoryEmptyStaysEmpty(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProject("Mem", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegenRuntimeMemory(p.Slug(), ""); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.RuntimeMemoryPath(p.Slug()))
	if err != nil {
		t.Fatal(err)
	}
	// An empty file is what makes the engine skip the flag entirely.
	if strings.TrimSpace(string(b)) != "" {
		t.Errorf("expected an empty injected file, got %q", b)
	}
}

// Chat mode cannot start jobs, so it must not be told about a tool it has no
// access to — and agent mode must be, or the model reaches for Bash instead.
func TestPrologueFollowsMode(t *testing.T) {
	store := newTestStore(t)
	p, err := store.CreateProject("demo", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	app := NewApp(store, Config{}, &Engine{BinPath: "claude"})
	app.OpenProjectObj(p)

	app.mu.Lock()
	app.jobMCP = map[string]json.RawMessage{"jobs": json.RawMessage(`{"type":"http"}`)}
	app.mu.Unlock()

	app.SetMode(ModeChat)
	app.mu.Lock()
	chat := app.runtimePrologueLocked()
	app.mu.Unlock()
	if chat != "" {
		t.Error("chat mode was told about the jobs tool it cannot reach")
	}

	app.SetMode(ModeAgent)
	app.mu.Lock()
	agent := app.runtimePrologueLocked()
	app.mu.Unlock()
	if !strings.Contains(agent, "mcp__jobs__start") {
		t.Error("agent mode was not told about the jobs tool")
	}

	// And with no supervisor wired at all, nothing is claimed either way.
	app.mu.Lock()
	app.jobMCP = nil
	none := app.runtimePrologueLocked()
	app.mu.Unlock()
	if none != "" {
		t.Error("guidance was injected with no job supervisor available")
	}
}
