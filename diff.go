package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// toolPreview renders a readable preview of a gated tool call: a command for
// Bash, a colored diff for Edit/Write, pretty JSON otherwise.
func toolPreview(toolName string, input json.RawMessage) string {
	switch toolName {
	case "Bash":
		var in struct {
			Command     string `json:"command"`
			Description string `json:"description"`
		}
		_ = json.Unmarshal(input, &in)
		var b strings.Builder
		if in.Description != "" {
			b.WriteString(previewMetaStyle.Render(in.Description))
			b.WriteByte('\n')
		}
		b.WriteString(cmdStyle.Render("$ " + in.Command))
		return b.String()

	case "Write":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		_ = json.Unmarshal(input, &in)
		return previewPathStyle.Render("write "+in.FilePath) + "\n" + renderAdds(in.Content)

	case "Edit":
		var in struct {
			FilePath  string `json:"file_path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		_ = json.Unmarshal(input, &in)
		return previewPathStyle.Render("edit "+in.FilePath) + "\n" + renderDiff(in.OldString, in.NewString)

	case "MultiEdit":
		var in struct {
			FilePath string `json:"file_path"`
			Edits    []struct {
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
			} `json:"edits"`
		}
		_ = json.Unmarshal(input, &in)
		var b strings.Builder
		b.WriteString(previewPathStyle.Render(fmt.Sprintf("edit %s · %d изм.", in.FilePath, len(in.Edits))))
		for _, e := range in.Edits {
			b.WriteByte('\n')
			b.WriteString(renderDiff(e.OldString, e.NewString))
		}
		return b.String()

	default:
		var pretty bytes.Buffer
		if json.Indent(&pretty, input, "", "  ") == nil && pretty.Len() > 0 {
			return cmdStyle.Render(pretty.String())
		}
		return cmdStyle.Render(string(input))
	}
}

func renderAdds(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = diffAddStyle.Render("+ " + l)
	}
	return strings.Join(out, "\n")
}

func renderDiff(oldS, newS string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimRight(oldS, "\n"), "\n") {
		b.WriteString(diffDelStyle.Render("- " + l))
		b.WriteByte('\n')
	}
	news := strings.Split(strings.TrimRight(newS, "\n"), "\n")
	for i, l := range news {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(diffAddStyle.Render("+ " + l))
	}
	return b.String()
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

// toolTarget is a one-line description of a tool call for the action timeline.
func toolTarget(toolName string, input json.RawMessage) string {
	switch toolName {
	case "Bash":
		var in struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(input, &in)
		return firstLine(in.Command)
	case "Write", "Edit", "MultiEdit":
		var in struct {
			FilePath string `json:"file_path"`
		}
		_ = json.Unmarshal(input, &in)
		return in.FilePath
	default:
		return ""
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}
