package tui

import (
	"fmt"
	"strings"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// renderPreview turns a structured tool preview into a colored block for the
// approval modal.
func renderPreview(p core.ToolPreview) string {
	switch p.Kind {
	case core.PreviewCommand:
		var b strings.Builder
		if p.Description != "" {
			b.WriteString(previewMetaStyle.Render(p.Description))
			b.WriteByte('\n')
		}
		b.WriteString(cmdStyle.Render("$ " + p.Command))
		return b.String()

	case core.PreviewWrite:
		head := previewPathStyle.Render("write " + p.Path)
		return head + "\n" + renderDiff(p.Diff)

	case core.PreviewEdit:
		head := previewPathStyle.Render("edit " + p.Path)
		return head + "\n" + renderDiff(p.Diff)

	default:
		return cmdStyle.Render(p.Raw)
	}
}

func renderDiff(lines []core.DiffLine) string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if l.Op == core.DiffDel {
			out[i] = diffDelStyle.Render("- " + l.Text)
		} else {
			out[i] = diffAddStyle.Render("+ " + l.Text)
		}
	}
	return strings.Join(out, "\n")
}

// clampLines trims a preview to at most max lines, noting how many were hidden.
func clampLines(s string, max int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= max {
		return s
	}
	hidden := len(lines) - max
	return strings.Join(lines[:max], "\n") + "\n" + hintStyle.Render(fmt.Sprintf("… ещё %d строк", hidden))
}

// formatApprovalLine renders a persisted tool decision for the transcript.
func formatApprovalLine(meta map[string]any) string {
	tool, _ := meta["tool"].(string)
	target, _ := meta["target"].(string)
	allow, _ := meta["allow"].(bool)
	mark, verdict := "✗", "отклонено"
	if allow {
		mark, verdict = "✓", "разрешено"
	}
	return strings.TrimSpace(fmt.Sprintf("%s %s %s · %s", mark, tool, target, verdict))
}
