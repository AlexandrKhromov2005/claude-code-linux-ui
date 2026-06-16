package core

import (
	"bytes"
	"encoding/json"
	"strings"
)

// PreviewKind classifies how a tool call should be previewed.
type PreviewKind string

const (
	PreviewCommand PreviewKind = "command" // Bash
	PreviewWrite   PreviewKind = "write"   // Write: all-added content
	PreviewEdit    PreviewKind = "edit"    // Edit/MultiEdit: a diff
	PreviewGeneric PreviewKind = "generic" // anything else: pretty JSON
)

// DiffOp marks a diff line as added, removed, or context.
type DiffOp string

const (
	DiffAdd DiffOp = "+"
	DiffDel DiffOp = "-"
)

// DiffLine is one line of a preview diff.
type DiffLine struct {
	Op   DiffOp `json:"op"`
	Text string `json:"text"`
}

// ToolPreview is a transport-neutral description of a gated tool call. Clients
// render it themselves (the TUI as a colored diff, the web as HTML).
type ToolPreview struct {
	Tool        string      `json:"tool"`
	Kind        PreviewKind `json:"kind"`
	Path        string      `json:"path,omitempty"`
	Command     string      `json:"command,omitempty"`
	Description string      `json:"description,omitempty"`
	Diff        []DiffLine  `json:"diff,omitempty"`
	Raw         string      `json:"raw,omitempty"`
}

// BuildToolPreview parses a tool invocation into a structured preview.
func BuildToolPreview(toolName string, input json.RawMessage) ToolPreview {
	p := ToolPreview{Tool: toolName}
	switch toolName {
	case "Bash":
		var in struct {
			Command     string `json:"command"`
			Description string `json:"description"`
		}
		_ = json.Unmarshal(input, &in)
		p.Kind = PreviewCommand
		p.Command = in.Command
		p.Description = in.Description

	case "Write":
		var in struct {
			FilePath string `json:"file_path"`
			Content  string `json:"content"`
		}
		_ = json.Unmarshal(input, &in)
		p.Kind = PreviewWrite
		p.Path = in.FilePath
		for _, l := range splitLines(in.Content) {
			p.Diff = append(p.Diff, DiffLine{Op: DiffAdd, Text: l})
		}

	case "Edit":
		var in struct {
			FilePath  string `json:"file_path"`
			OldString string `json:"old_string"`
			NewString string `json:"new_string"`
		}
		_ = json.Unmarshal(input, &in)
		p.Kind = PreviewEdit
		p.Path = in.FilePath
		p.Diff = diffLines(in.OldString, in.NewString)

	case "MultiEdit":
		var in struct {
			FilePath string `json:"file_path"`
			Edits    []struct {
				OldString string `json:"old_string"`
				NewString string `json:"new_string"`
			} `json:"edits"`
		}
		_ = json.Unmarshal(input, &in)
		p.Kind = PreviewEdit
		p.Path = in.FilePath
		for _, e := range in.Edits {
			p.Diff = append(p.Diff, diffLines(e.OldString, e.NewString)...)
		}

	default:
		p.Kind = PreviewGeneric
		var pretty bytes.Buffer
		if json.Indent(&pretty, input, "", "  ") == nil && pretty.Len() > 0 {
			p.Raw = pretty.String()
		} else {
			p.Raw = string(input)
		}
	}
	return p
}

func splitLines(s string) []string {
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

func diffLines(oldS, newS string) []DiffLine {
	var out []DiffLine
	for _, l := range splitLines(oldS) {
		out = append(out, DiffLine{Op: DiffDel, Text: l})
	}
	for _, l := range splitLines(newS) {
		out = append(out, DiffLine{Op: DiffAdd, Text: l})
	}
	return out
}

// ToolTarget is a one-line description of a tool call for the action timeline.
func ToolTarget(toolName string, input json.RawMessage) string {
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
