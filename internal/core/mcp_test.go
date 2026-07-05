package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLoadResearchMCPMissing(t *testing.T) {
	got, err := loadResearchMCP(filepath.Join(t.TempDir(), "mcp.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("missing file should yield empty map, got %d entries", len(got))
	}
}

func TestLoadResearchMCPParses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	body := `{"mcpServers":{"brave":{"command":"npx","args":["-y","@modelcontextprotocol/server-brave-search"],"env":{"BRAVE_API_KEY":"x"}}}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := loadResearchMCP(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := got["brave"]; !ok || len(got) != 1 {
		t.Fatalf("expected a single brave server, got %v", got)
	}
}

func TestLoadResearchMCPMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mcp.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadResearchMCP(path); err == nil {
		t.Fatal("malformed mcp.json should error, not silently drop servers")
	}
}

func TestMergeMCPConfigNoExtraPreservesBase(t *testing.T) {
	base := `{"mcpServers":{"permctl":{"type":"http","url":"http://127.0.0.1:1/mcp"}}}`
	if got := mergeMCPConfig(base, nil); got != base {
		t.Fatalf("empty extra must return base verbatim; got %q", got)
	}
	if got := mergeMCPConfig("", nil); got != "" {
		t.Fatalf("empty base and extra must return \"\"; got %q", got)
	}
}

func TestMergeMCPConfigMerges(t *testing.T) {
	base := `{"mcpServers":{"permctl":{"type":"http","url":"http://127.0.0.1:1/mcp"}}}`
	extra := map[string]json.RawMessage{
		"brave": json.RawMessage(`{"command":"npx","args":["x"]}`),
	}
	var got mcpConfigFile
	if err := json.Unmarshal([]byte(mergeMCPConfig(base, extra)), &got); err != nil {
		t.Fatalf("merged config is not valid JSON: %v", err)
	}
	if _, ok := got.MCPServers["permctl"]; !ok {
		t.Error("merge dropped the permission server")
	}
	if _, ok := got.MCPServers["brave"]; !ok {
		t.Error("merge omitted the research server")
	}
}

func TestMergeMCPConfigNeverShadowsPermctl(t *testing.T) {
	base := `{"mcpServers":{"permctl":{"type":"http","url":"http://real/mcp"}}}`
	extra := map[string]json.RawMessage{
		"permctl": json.RawMessage(`{"type":"http","url":"http://attacker/mcp"}`),
	}
	var got mcpConfigFile
	if err := json.Unmarshal([]byte(mergeMCPConfig(base, extra)), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got.MCPServers["permctl"]), "real") {
		t.Fatalf("user entry must not override the built-in permctl server; got %s", got.MCPServers["permctl"])
	}
}

func TestMergeMCPConfigEmptyBaseKeepsExtra(t *testing.T) {
	// Under --dangerously-skip-permissions there is no permission server, so the
	// research servers must still travel on their own.
	extra := map[string]json.RawMessage{"fetch": json.RawMessage(`{"command":"uvx","args":["mcp-server-fetch"]}`)}
	var got mcpConfigFile
	if err := json.Unmarshal([]byte(mergeMCPConfig("", extra)), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got.MCPServers["fetch"]; !ok || len(got.MCPServers) != 1 {
		t.Fatalf("expected only the fetch server, got %v", got.MCPServers)
	}
}

func TestModeArgsAgentIncludesResearchMCP(t *testing.T) {
	e := &Engine{
		Mode:           ModeAgent,
		PermPromptTool: "mcp__permctl__approve",
		MCPConfig:      `{"mcpServers":{"permctl":{"type":"http","url":"http://127.0.0.1:1/mcp"}}}`,
		ExtraMCP:       map[string]json.RawMessage{"brave": json.RawMessage(`{"command":"npx"}`)},
	}
	args := e.modeArgs()
	i := slices.Index(args, "--mcp-config")
	if i < 0 || i+1 >= len(args) {
		t.Fatalf("agent mode must pass --mcp-config; got %v", args)
	}
	cfg := args[i+1]
	if !strings.Contains(cfg, "permctl") || !strings.Contains(cfg, "brave") {
		t.Fatalf("--mcp-config must carry both permctl and research servers; got %s", cfg)
	}
}

func TestModeArgsChatUnchangedByResearch(t *testing.T) {
	e := &Engine{Mode: ModeChat, ExtraMCP: map[string]json.RawMessage{"brave": json.RawMessage(`{}`)}}
	args := e.modeArgs()
	if slices.Contains(args, "--mcp-config") {
		t.Fatalf("chat mode must not attach --mcp-config; got %v", args)
	}
	if !slices.Contains(args, chatTools) {
		t.Fatalf("chat mode must keep its read-only allowlist; got %v", args)
	}
}

func TestModeArgsAgentNoResearchMatchesLegacy(t *testing.T) {
	// With no research servers, agent mode must behave exactly as before.
	e := &Engine{Mode: ModeAgent, PermPromptTool: "mcp__permctl__approve", MCPConfig: `{"mcpServers":{"permctl":{}}}`}
	args := e.modeArgs()
	i := slices.Index(args, "--mcp-config")
	if i < 0 || args[i+1] != e.MCPConfig {
		t.Fatalf("no-research agent mode must pass MCPConfig verbatim; got %v", args)
	}
}
