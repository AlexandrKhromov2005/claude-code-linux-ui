package core

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
)

// bashMultiplexers are commands whose first sub-token is meaningful, so a
// remembered rule keeps two tokens (e.g. "go test") instead of just "go".
var bashMultiplexers = map[string]bool{
	"go": true, "git": true, "npm": true, "pnpm": true, "yarn": true,
	"cargo": true, "docker": true, "kubectl": true, "make": true, "gh": true,
	"pip": true, "pip3": true, "python": true, "python3": true, "node": true,
	"bun": true, "deno": true, "apt": true, "apt-get": true, "brew": true,
}

// SuggestRule proposes an editable allow-rule for a gated tool call, following
// the smart defaults from the spec: a command prefix for Bash, a path glob for
// edits.
func SuggestRule(project *Project, toolName string, input json.RawMessage) string {
	switch toolName {
	case "Bash":
		var in struct {
			Command string `json:"command"`
		}
		_ = json.Unmarshal(input, &in)
		if prefix := bashPrefix(in.Command); prefix != "" {
			return "Bash(" + prefix + ":*)"
		}
		return "Bash"
	case "Write", "Edit", "MultiEdit", "NotebookEdit":
		var in struct {
			FilePath     string `json:"file_path"`
			NotebookPath string `json:"notebook_path"`
		}
		_ = json.Unmarshal(input, &in)
		path := in.FilePath
		if path == "" {
			path = in.NotebookPath
		}
		if glob := pathGlob(project, path); glob != "" {
			return toolName + "(" + glob + ")"
		}
		return toolName
	default:
		return toolName
	}
}

func bashPrefix(cmd string) string {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return ""
	}
	if len(fields) >= 2 && bashMultiplexers[fields[0]] && !strings.HasPrefix(fields[1], "-") {
		return fields[0] + " " + fields[1]
	}
	return fields[0]
}

func pathGlob(project *Project, path string) string {
	if path == "" {
		return ""
	}
	dir := filepath.Dir(path)
	if project != nil && project.Cwd != "" {
		if rel, err := filepath.Rel(project.Cwd, dir); err == nil && !strings.HasPrefix(rel, "..") {
			if rel == "." {
				return "./*"
			}
			return "./" + rel + "/*"
		}
	}
	return dir + "/*"
}

// settingsJSON builds the inline --settings value carrying the project's
// remembered allow/deny rules. Claude Code enforces deny over allow.
func settingsJSON(p *Project) string {
	allow := []string{}
	deny := []string{}
	if p != nil {
		if p.Permissions.Allow != nil {
			allow = p.Permissions.Allow
		}
		if p.Permissions.Deny != nil {
			deny = p.Permissions.Deny
		}
	}
	b, _ := json.Marshal(map[string]any{
		"permissions": map[string]any{"allow": allow, "deny": deny},
	})
	return string(b)
}

// addAllowRule appends a rule to the project's allow list if not already present.
func addAllowRule(p *Project, rule string) bool {
	rule = strings.TrimSpace(rule)
	if p == nil || rule == "" {
		return false
	}
	if slices.Contains(p.Permissions.Allow, rule) {
		return false
	}
	p.Permissions.Allow = append(p.Permissions.Allow, rule)
	return true
}
