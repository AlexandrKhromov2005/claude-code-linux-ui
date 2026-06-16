package main

import (
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestExportThreadMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/out.md"
	th := &Thread{
		ID:      "t1",
		Title:   "Demo thread",
		Created: time.Now(),
		Messages: []Msg{
			{Role: "user", Content: "hello"},
			{Role: "assistant", Content: "hi there"},
			{Role: "tool", Content: "✓ Write a.txt · разрешено"},
		},
	}
	proj := &Project{Name: "P", Cwd: "/p"}
	if err := exportThreadMarkdown(th, proj, path); err != nil {
		t.Fatalf("export: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	out := string(b)
	for _, want := range []string{"# Demo thread", "Проект: P", "## You", "hello", "## Claude", "hi there", "> ✓ Write"} {
		if !strings.Contains(out, want) {
			t.Errorf("export missing %q:\n%s", want, out)
		}
	}
}

func TestDefaultExportPath(t *testing.T) {
	proj := &Project{Cwd: "/proj"}
	th := &Thread{ID: "20260101T000000-abc", Title: "My Thread"}
	if got := defaultExportPath(proj, th); got != "/proj/my-thread.md" {
		t.Errorf("path = %q", got)
	}
	// Empty title slugifies to "project"; fall back to the id.
	th2 := &Thread{ID: "id123", Title: ""}
	if got := defaultExportPath(proj, th2); got != "/proj/id123.md" {
		t.Errorf("fallback path = %q", got)
	}
}

func TestMakeSnippet(t *testing.T) {
	s := makeSnippet("the quick brown fox jumps over the lazy dog", 16, 5)
	if !strings.Contains(s, "brown") {
		t.Errorf("snippet = %q", s)
	}

	// Cyrillic content must stay valid UTF-8 after slicing.
	cyr := "разработка терминального клиента для Claude на Go и Bubble Tea"
	idx := strings.Index(cyr, "клиента")
	snip := makeSnippet(cyr, idx, len("клиента"))
	if !utf8.ValidString(snip) {
		t.Errorf("snippet is not valid UTF-8: %q", snip)
	}
	if !strings.Contains(snip, "клиента") {
		t.Errorf("snippet lost the match: %q", snip)
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

func TestConfigBudgetRoundTrip(t *testing.T) {
	s := newTestStore(t)
	cfg, _ := s.LoadConfig()
	cfg.BudgetWarnUSD = 2.5
	if err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	got, _ := s.LoadConfig()
	if got.BudgetWarnUSD != 2.5 {
		t.Errorf("budget = %v, want 2.5", got.BudgetWarnUSD)
	}
}
