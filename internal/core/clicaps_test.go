package core

import "testing"

// modernHelp is a trimmed excerpt of `claude --help` from a CLI that supports
// every optional flag this app uses.
const modernHelp = `Usage: claude [options] [command] [prompt]

Options:
  --append-system-prompt <prompt>       Append a system prompt to the default
  --disable-slash-commands              Disable all skills
  --effort <level>                      Effort level for the current session
  --exclude-dynamic-system-prompt-sections
      Move per-machine sections (cwd, env info, memory paths, git status)
  --fallback-model <model>              Enable automatic fallback
  --no-session-persistence              Disable session persistence
  --setting-sources <sources>           Comma-separated list of setting sources
  --strict-mcp-config                   Only use MCP servers from --mcp-config
  --system-prompt <prompt>              System prompt to use for the session
  --tools <tools...>                    Specify the list of available tools
`

// legacyHelp stands in for an older CLI that predates the optimisation flags but
// still accepts everything the app relied on before them.
const legacyHelp = `Usage: claude [options] [command] [prompt]

Options:
  --append-system-prompt <prompt>       Append a system prompt to the default
  --effort <level>                      Effort level for the current session
  --model <model>                       Model for the current session
  --output-format <format>              Output format
`

func TestParseCLICapsModern(t *testing.T) {
	c := parseCLICaps(modernHelp)
	cases := map[string]bool{
		"ExcludeDynamicPrompt": c.ExcludeDynamicPrompt,
		"SystemPrompt":         c.SystemPrompt,
		"Tools":                c.Tools,
		"StrictMCPConfig":      c.StrictMCPConfig,
		"SettingSources":       c.SettingSources,
		"DisableSlashCommands": c.DisableSlashCommands,
		"NoSessionPersistence": c.NoSessionPersistence,
		"FallbackModel":        c.FallbackModel,
	}
	for name, ok := range cases {
		if !ok {
			t.Errorf("%s: want supported, got unsupported", name)
		}
	}
}

func TestParseCLICapsLegacy(t *testing.T) {
	c := parseCLICaps(legacyHelp)
	if c.ExcludeDynamicPrompt || c.Tools || c.StrictMCPConfig ||
		c.SettingSources || c.DisableSlashCommands || c.NoSessionPersistence || c.FallbackModel {
		t.Fatalf("legacy help must not advertise the newer flags: %+v", c)
	}
}

// A legacy help text mentions --append-system-prompt but not --system-prompt.
// Substring matching must not confuse the two, or a side call would pass a flag
// the binary rejects and fail every memory update.
func TestParseCLICapsDoesNotConfuseAppendSystemPrompt(t *testing.T) {
	if parseCLICaps(legacyHelp).SystemPrompt {
		t.Fatal("--append-system-prompt was misread as --system-prompt support")
	}
}

// An unreadable or missing binary must report no capabilities, so the app falls
// back to the flag set that has always worked rather than failing turns.
func TestDetectCLICapsMissingBinary(t *testing.T) {
	c := DetectCLICaps("/nonexistent/claude-binary-for-test")
	if (c != CLICaps{}) {
		t.Fatalf("want zero caps for a missing binary, got %+v", c)
	}
}
