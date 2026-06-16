package main

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
		got := suggestRule(proj, c.tool, json.RawMessage(c.input))
		if got != c.want {
			t.Errorf("suggestRule(%s, %s) = %q, want %q", c.tool, c.input, got, c.want)
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

	// A nil project still produces valid empty arrays, not null.
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
