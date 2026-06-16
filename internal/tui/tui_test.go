package tui

import (
	"path/filepath"
	"testing"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

func newTestApp(t *testing.T) *core.App {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	store, err := core.NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	cfg, _ := store.LoadConfig()
	return core.NewApp(store, cfg, &core.Engine{BinPath: "claude", Mode: core.ModeChat})
}

func TestNewBootstrapNoProjects(t *testing.T) {
	app := newTestApp(t)
	m, ok := New(app).(model)
	if !ok {
		t.Fatalf("New did not return a model")
	}
	if m.overlay != overlayProjects {
		t.Fatalf("overlay = %v, want overlayProjects", m.overlay)
	}
	if app.CurrentProject() != nil {
		t.Fatalf("expected no active project at startup switcher")
	}
	var hasUseCwd bool
	for _, it := range m.projList.items {
		if it.action == "use-cwd" {
			hasUseCwd = true
		}
	}
	if !hasUseCwd {
		t.Fatalf("switcher missing 'use current folder' action")
	}
}

func TestApplyTheme(t *testing.T) {
	defer applyTheme("dark")
	applyTheme("light")
	if colAccent != themes["light"].accent {
		t.Errorf("light accent not applied")
	}
	applyTheme("does-not-exist")
	if colAccent != themes["dark"].accent {
		t.Errorf("unknown theme should fall back to dark")
	}
	if len(themeNames()) != 3 {
		t.Errorf("themeNames = %v", themeNames())
	}
}

func TestFormatApprovalLine(t *testing.T) {
	allow := formatApprovalLine(map[string]any{"tool": "Bash", "target": "go test", "allow": true})
	if allow != "✓ Bash go test · разрешено" {
		t.Errorf("allow line = %q", allow)
	}
	deny := formatApprovalLine(map[string]any{"tool": "Write", "target": "/a", "allow": false})
	if deny != "✗ Write /a · отклонено" {
		t.Errorf("deny line = %q", deny)
	}
}

func TestUserDisplay(t *testing.T) {
	if got := userDisplay("hi", nil); got != "hi" {
		t.Errorf("no attachments = %q", got)
	}
	got := userDisplay("look", []string{"/x/a.png"})
	if got != "look\n🖼 a.png" {
		t.Errorf("with attachment = %q", got)
	}
}
