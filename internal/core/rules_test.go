package core

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSuggestRule(t *testing.T) {
	proj := &Project{Cwd: "/proj"}
	cases := []struct {
		tool  string
		input string
		want  string
	}{
		{"Bash", `{"command":"go test ./..."}`, "Bash(go test:*)"},
		{"Bash", `{"command":"git push origin main"}`, "Bash(git push:*)"},
		{"Bash", `{"command":"ls -la"}`, "Bash(ls:*)"},
		{"Bash", `{"command":""}`, "Bash"},
		{"Write", `{"file_path":"/proj/src/main.go"}`, "Write(./src/*)"},
		{"Edit", `{"file_path":"/proj/main.go"}`, "Edit(./*)"},
		{"Edit", `{"file_path":"/outside/x.go"}`, "Edit(/outside/*)"},
		{"WebFetch", `{"url":"https://x"}`, "WebFetch"},
	}
	for _, c := range cases {
		if got := SuggestRule(proj, c.tool, json.RawMessage(c.input)); got != c.want {
			t.Errorf("SuggestRule(%s, %s) = %q, want %q", c.tool, c.input, got, c.want)
		}
	}
}

func TestSettingsJSON(t *testing.T) {
	p := &Project{Permissions: Permissions{Allow: []string{"Bash(go test:*)"}, Deny: []string{"Bash(rm:*)"}}}
	got := settingsJSON(p)
	var parsed struct {
		Permissions struct {
			Allow []string `json:"allow"`
			Deny  []string `json:"deny"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("settings not JSON: %v", err)
	}
	if len(parsed.Permissions.Allow) != 1 || parsed.Permissions.Deny[0] != "Bash(rm:*)" {
		t.Fatalf("settings round-trip wrong: %s", got)
	}
	empty := settingsJSON(nil)
	if !strings.Contains(empty, `"allow":[]`) || !strings.Contains(empty, `"deny":[]`) {
		t.Fatalf("empty settings = %s", empty)
	}
}

func TestAddAllowRule(t *testing.T) {
	p := &Project{}
	if !addAllowRule(p, "Bash(go test:*)") {
		t.Fatal("first add should report a change")
	}
	if addAllowRule(p, "Bash(go test:*)") {
		t.Fatal("duplicate add should be a no-op")
	}
	if len(p.Permissions.Allow) != 1 {
		t.Fatalf("allow list = %v", p.Permissions.Allow)
	}
	if addAllowRule(p, "  ") {
		t.Fatal("blank rule should not be added")
	}
}

func TestBuildToolPreview(t *testing.T) {
	bash := BuildToolPreview("Bash", json.RawMessage(`{"command":"ls -la","description":"list"}`))
	if bash.Kind != PreviewCommand || bash.Command != "ls -la" || bash.Description != "list" {
		t.Fatalf("bash preview wrong: %+v", bash)
	}
	write := BuildToolPreview("Write", json.RawMessage(`{"file_path":"/a","content":"x\ny"}`))
	if write.Kind != PreviewWrite || write.Path != "/a" || len(write.Diff) != 2 {
		t.Fatalf("write preview wrong: %+v", write)
	}
	if write.Diff[0].Op != DiffAdd {
		t.Fatalf("write diff op = %q", write.Diff[0].Op)
	}
	edit := BuildToolPreview("Edit", json.RawMessage(`{"file_path":"/a","old_string":"a","new_string":"b"}`))
	if edit.Kind != PreviewEdit || len(edit.Diff) != 2 || edit.Diff[0].Op != DiffDel || edit.Diff[1].Op != DiffAdd {
		t.Fatalf("edit preview wrong: %+v", edit)
	}
	gen := BuildToolPreview("WebFetch", json.RawMessage(`{"url":"https://x"}`))
	if gen.Kind != PreviewGeneric || !strings.Contains(gen.Raw, "url") {
		t.Fatalf("generic preview wrong: %+v", gen)
	}
	if ToolTarget("Write", json.RawMessage(`{"file_path":"/a/b.go"}`)) != "/a/b.go" {
		t.Fatalf("tool target wrong")
	}
}
