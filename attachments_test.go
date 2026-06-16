package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsImagePath(t *testing.T) {
	for _, p := range []string{"a.png", "b.JPG", "c.jpeg", "/x/y/z.webp", "icon.svg"} {
		if !isImagePath(p) {
			t.Errorf("%q should be an image", p)
		}
	}
	for _, p := range []string{"a.txt", "main.go", "noext", "archive.tar.gz"} {
		if isImagePath(p) {
			t.Errorf("%q should not be an image", p)
		}
	}
}

func TestAddAttachment(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "note.txt")
	if err := os.WriteFile(file, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	var m model
	m.addAttachment(file)
	if len(m.attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(m.attachments))
	}

	// Duplicate is ignored.
	m.addAttachment(file)
	if len(m.attachments) != 1 {
		t.Fatalf("duplicate was added: %v", m.attachments)
	}

	// Directory is rejected.
	m.addAttachment(dir)
	if len(m.attachments) != 1 {
		t.Fatalf("directory was attached: %v", m.attachments)
	}

	// Missing file is rejected.
	m.addAttachment(filepath.Join(dir, "missing.txt"))
	if len(m.attachments) != 1 {
		t.Fatalf("missing file was attached: %v", m.attachments)
	}
}

func TestDisplayName(t *testing.T) {
	m := model{project: &Project{Cwd: "/proj"}}
	if got := m.displayName("/proj/src/main.go"); got != "src/main.go" {
		t.Errorf("relative display = %q, want src/main.go", got)
	}
	// Outside the project falls back to the base name.
	if got := m.displayName("/other/file.txt"); got != "file.txt" {
		t.Errorf("outside display = %q, want file.txt", got)
	}
}
