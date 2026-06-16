package main

import (
	"path/filepath"
	"testing"
)

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"chat":  ModeChat,
		"agent": ModeAgent,
		"AGENT": ModeAgent,
		"":      ModeChat,
		"weird": ModeChat,
	}
	for in, want := range cases {
		if got := parseMode(in); got != want {
			t.Errorf("parseMode(%q) = %v, want %v", in, got, want)
		}
	}
	if ModeAgent.String() != "agent" || ModeChat.String() != "chat" {
		t.Errorf("Mode.String mismatch")
	}
}

func TestMakeTitle(t *testing.T) {
	if got := makeTitle("  hello world  "); got != "hello world" {
		t.Errorf("trim: %q", got)
	}
	if got := makeTitle("first line\nsecond"); got != "first line" {
		t.Errorf("multiline: %q", got)
	}
	long := makeTitle(string(make([]rune, 0)) + repeat("a", 100))
	if r := []rune(long); len(r) != 58 || r[57] != '…' {
		t.Errorf("truncate len=%d last=%q", len(r), string(r[len(r)-1]))
	}
}

func repeat(s string, n int) string {
	out := ""
	for range n {
		out += s
	}
	return out
}

func TestNewModelBootstrapNoProjects(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("CLAUDE_BIN", "claude")

	m, err := newModel()
	if err != nil {
		t.Fatalf("newModel: %v", err)
	}
	// With no projects and no last_project, the startup switcher opens.
	if m.overlay != overlayProjects {
		t.Fatalf("overlay = %v, want overlayProjects", m.overlay)
	}
	if m.project != nil {
		t.Fatalf("expected no active project at startup switcher")
	}
	// The switcher offers the current folder as a creatable project.
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
