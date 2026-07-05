package core

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
)

// researchMCPFile is the optional user-provided MCP config, read from the config
// directory. Its mcpServers are merged into agent-mode turns so Claude can reach
// research tools (web search/fetch, e.g. Brave, Tavily, Perplexity, a fetch
// server) alongside the in-process permission server. It uses the same schema
// Claude Code's own --mcp-config accepts, so entries can be copied verbatim.
const researchMCPFile = "mcp.json"

// mcpConfigFile is the on-disk shape of mcp.json (and of the inline --mcp-config
// value): a single mcpServers object. Only mcpServers is consumed; server
// entries are kept as raw JSON so any transport or field Claude Code supports
// passes through without this layer needing to model it.
type mcpConfigFile struct {
	MCPServers map[string]json.RawMessage `json:"mcpServers"`
}

// loadResearchMCP reads mcp.json at path and returns its mcpServers map. A
// missing file yields an empty map and no error; a malformed file is an error a
// client can surface (better to warn than to silently drop configured servers).
func loadResearchMCP(path string) (map[string]json.RawMessage, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]json.RawMessage{}, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg mcpConfigFile
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, err
	}
	if cfg.MCPServers == nil {
		cfg.MCPServers = map[string]json.RawMessage{}
	}
	return cfg.MCPServers, nil
}

// mergeMCPConfig folds extra servers into an existing inline --mcp-config value
// (the permission server's config). The built-in entries in base always win: a
// user server with the same name as a base server is ignored, so mcp.json can
// never shadow the permission server. It returns "" when there is nothing to
// pass, and returns base unchanged when there is nothing to merge, so the
// no-research path stays byte-for-byte what it was before.
func mergeMCPConfig(base string, extra map[string]json.RawMessage) string {
	if len(extra) == 0 {
		return base // nothing to merge — preserve base exactly (including "")
	}
	servers := map[string]json.RawMessage{}
	if base != "" {
		var bc mcpConfigFile
		if err := json.Unmarshal([]byte(base), &bc); err != nil {
			return base // unparseable base: never risk dropping the permission server
		}
		maps.Copy(servers, bc.MCPServers)
	}
	for k, v := range extra {
		if _, taken := servers[k]; taken {
			continue // a built-in server always wins over a same-named user entry
		}
		servers[k] = v
	}
	if len(servers) == 0 {
		return ""
	}
	b, err := json.Marshal(mcpConfigFile{MCPServers: servers})
	if err != nil {
		return base
	}
	return string(b)
}
