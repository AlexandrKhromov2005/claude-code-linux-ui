package core

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"chat": ModeChat, "agent": ModeAgent, "AGENT": ModeAgent, "": ModeChat, "weird": ModeChat,
	}
	for in, want := range cases {
		if got := ParseMode(in); got != want {
			t.Errorf("ParseMode(%q) = %v, want %v", in, got, want)
		}
	}
	if ModeAgent.String() != "agent" || ModeChat.String() != "chat" {
		t.Errorf("Mode.String mismatch")
	}
}

func TestModeArgs(t *testing.T) {
	chat := (&Engine{Mode: ModeChat}).modeArgs()
	if !slices.Contains(chat, "--allowedTools") || !slices.Contains(chat, chatTools) || !slices.Contains(chat, "dontAsk") {
		t.Errorf("chat args = %v", chat)
	}

	agent := (&Engine{Mode: ModeAgent, PermPromptTool: "mcp__permctl__approve"}).modeArgs()
	if !slices.Contains(agent, "--permission-prompt-tool") || slices.Contains(agent, "--dangerously-skip-permissions") {
		t.Errorf("agent args = %v", agent)
	}

	skip := (&Engine{Mode: ModeAgent, SkipPermissions: true, PermPromptTool: "mcp__permctl__approve"}).modeArgs()
	if !slices.Contains(skip, "--dangerously-skip-permissions") || slices.Contains(skip, "--permission-prompt-tool") {
		t.Errorf("skip args = %v", skip)
	}
}

func TestValidEffort(t *testing.T) {
	for _, ok := range []string{"", "low", "medium", "high", "xhigh", "max"} {
		if !ValidEffort(ok) {
			t.Errorf("ValidEffort(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"lowest", "HIGH", "ultra", "auto", "1"} {
		if ValidEffort(bad) {
			t.Errorf("ValidEffort(%q) = true, want false", bad)
		}
	}
}

func TestMakeTitle(t *testing.T) {
	if got := makeTitle("  hello world  "); got != "hello world" {
		t.Errorf("trim: %q", got)
	}
	if got := makeTitle("first line\nsecond"); got != "first line" {
		t.Errorf("multiline: %q", got)
	}
	long := makeTitle(strings.Repeat("a", 100))
	if r := []rune(long); len(r) != 58 || r[57] != '…' {
		t.Errorf("truncate len=%d", len(r))
	}
}

func TestIsImagePath(t *testing.T) {
	for _, p := range []string{"a.png", "b.JPG", "c.jpeg", "/x/y/z.webp", "icon.svg"} {
		if !IsImagePath(p) {
			t.Errorf("%q should be an image", p)
		}
	}
	for _, p := range []string{"a.txt", "main.go", "noext"} {
		if IsImagePath(p) {
			t.Errorf("%q should not be an image", p)
		}
	}
}

func TestValidateAttachmentPath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ValidateAttachmentPath(file); err != nil || got != file {
		t.Fatalf("valid file rejected: %v", err)
	}
	if _, err := ValidateAttachmentPath(dir); err == nil {
		t.Fatalf("directory should be rejected")
	}
	if _, err := ValidateAttachmentPath(filepath.Join(dir, "missing")); err == nil {
		t.Fatalf("missing file should be rejected")
	}
}

func TestRelDisplay(t *testing.T) {
	if got := RelDisplay("/proj", "/proj/src/main.go"); got != "src/main.go" {
		t.Errorf("relative = %q", got)
	}
	if got := RelDisplay("/proj", "/other/file.txt"); got != "file.txt" {
		t.Errorf("outside = %q", got)
	}
}

func TestBuildPrompt(t *testing.T) {
	got := BuildPrompt("hello", []string{"/a/b.txt", "/c.png"})
	if got != "hello @/a/b.txt @/c.png" {
		t.Errorf("prompt = %q", got)
	}
}

func TestMakeSnippet(t *testing.T) {
	s := makeSnippet("the quick brown fox jumps over the lazy dog", 16, 5)
	if !strings.Contains(s, "brown") {
		t.Errorf("snippet = %q", s)
	}
	cyr := "разработка терминального клиента для Claude на Go"
	idx := strings.Index(cyr, "клиента")
	snip := makeSnippet(cyr, idx, len("клиента"))
	if !utf8.ValidString(snip) {
		t.Errorf("snippet not valid UTF-8: %q", snip)
	}
	if !strings.Contains(snip, "клиента") {
		t.Errorf("snippet lost match: %q", snip)
	}
}

func TestExportThreadMarkdown(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/out.md"
	th := &Thread{ID: "t1", Title: "Demo", Messages: []Msg{
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi there"},
		{Role: "tool", Content: "Write a.txt"},
	}}
	if err := ExportThreadMarkdown(th, &Project{Name: "P", Cwd: "/p"}, path); err != nil {
		t.Fatalf("export: %v", err)
	}
	b, _ := os.ReadFile(path)
	out := string(b)
	for _, want := range []string{"# Demo", "Проект: P", "## You", "hello", "## Claude", "> Write"} {
		if !strings.Contains(out, want) {
			t.Errorf("export missing %q", want)
		}
	}
}

func TestDefaultExportPath(t *testing.T) {
	proj := &Project{Cwd: "/proj"}
	if got := DefaultExportPath(proj, &Thread{ID: "x", Title: "My Thread"}); got != "/proj/my-thread.md" {
		t.Errorf("path = %q", got)
	}
	if got := DefaultExportPath(proj, &Thread{ID: "id123", Title: ""}); got != "/proj/id123.md" {
		t.Errorf("fallback path = %q", got)
	}
}
