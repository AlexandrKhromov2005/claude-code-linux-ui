package core

import (
	"path/filepath"
	"strings"
)

// ExportThreadMarkdown writes a thread transcript as a readable Markdown file.
func ExportThreadMarkdown(t *Thread, project *Project, path string) error {
	var b strings.Builder
	title := t.Title
	if title == "" {
		title = "Тред " + t.ID
	}
	b.WriteString("# " + title + "\n\n")
	if project != nil {
		b.WriteString("- Проект: " + project.Name + " (" + project.Cwd + ")\n")
	}
	b.WriteString("- Создан: " + t.Created.Format("2006-01-02 15:04") + "\n")
	if t.ClaudeSessionID != "" {
		b.WriteString("- Сессия: " + t.ClaudeSessionID + "\n")
	}
	b.WriteString("\n---\n\n")

	for _, msg := range t.Messages {
		switch msg.Role {
		case "user":
			b.WriteString("## You\n\n" + strings.TrimRight(msg.Content, "\n") + "\n\n")
		case "assistant":
			b.WriteString("## Claude\n\n" + strings.TrimRight(msg.Content, "\n") + "\n\n")
		default: // tool / system entries render as quotes
			b.WriteString("> " + strings.TrimRight(msg.Content, "\n") + "\n\n")
		}
	}
	return writeFileAtomic(path, []byte(b.String()))
}

// DefaultExportPath places the export in the project directory, named after the
// thread.
func DefaultExportPath(project *Project, t *Thread) string {
	name := slugify(t.Title)
	if name == "project" {
		name = t.ID
	}
	dir := "."
	if project != nil && project.Cwd != "" {
		dir = project.Cwd
	}
	return filepath.Join(dir, name+".md")
}
