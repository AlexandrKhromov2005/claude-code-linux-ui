package core

import (
	"slices"
	"testing"
)

// argValue returns the value following flag, or "" when the flag is absent.
func argValue(args []string, flag string) string {
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		return ""
	}
	return args[i+1]
}

var modernCaps = parseCLICaps(modernHelp)

// The whole point of the flag is that it applies to resumed turns: that is where
// a changed git status would otherwise re-cache the entire transcript.
func TestTurnArgsExcludesDynamicPromptOnResume(t *testing.T) {
	e := &Engine{BinPath: "claude", Mode: ModeChat}
	args := e.turnArgs("привет", "sess-1", modernCaps)

	if !slices.Contains(args, "--exclude-dynamic-system-prompt-sections") {
		t.Error("resumed turn must keep the volatile sections out of the cached prefix")
	}
	if got := argValue(args, "--resume"); got != "sess-1" {
		t.Errorf("--resume = %q, want sess-1", got)
	}
}

// On a CLI that predates the flag, passing it would make the process exit
// non-zero and break every turn, so it must simply be omitted.
func TestTurnArgsOmitsUnsupportedFlags(t *testing.T) {
	e := &Engine{BinPath: "claude", Mode: ModeChat, FallbackModel: "sonnet"}
	args := e.turnArgs("привет", "", CLICaps{})

	for _, flag := range []string{"--exclude-dynamic-system-prompt-sections", "--fallback-model"} {
		if slices.Contains(args, flag) {
			t.Errorf("%s was passed to a CLI that does not support it", flag)
		}
	}
	// The turn must still be well formed without the optional flags.
	if got := argValue(args, "--output-format"); got != "stream-json" {
		t.Errorf("--output-format = %q, want stream-json", got)
	}
}

func TestTurnArgsFallbackModel(t *testing.T) {
	e := &Engine{BinPath: "claude", Mode: ModeChat, Model: "opus", FallbackModel: "sonnet"}
	args := e.turnArgs("привет", "", modernCaps)

	if got := argValue(args, "--model"); got != "opus" {
		t.Errorf("--model = %q, want opus", got)
	}
	if got := argValue(args, "--fallback-model"); got != "sonnet" {
		t.Errorf("--fallback-model = %q, want sonnet", got)
	}
}

// An empty fallback must not produce a bare flag with a missing value, which the
// CLI would read as the next flag.
func TestTurnArgsNoEmptyFallback(t *testing.T) {
	e := &Engine{BinPath: "claude", Mode: ModeChat, Model: "opus"}
	if args := e.turnArgs("привет", "", modernCaps); slices.Contains(args, "--fallback-model") {
		t.Error("--fallback-model passed with no value configured")
	}
}

// Chat mode is the read-only mode; the optimisation flags must not disturb it.
func TestTurnArgsChatStaysReadOnly(t *testing.T) {
	e := &Engine{BinPath: "claude", Mode: ModeChat}
	args := e.turnArgs("привет", "", modernCaps)

	if got := argValue(args, "--allowedTools"); got != chatTools {
		t.Errorf("--allowedTools = %q, want %q", got, chatTools)
	}
	if got := argValue(args, "--permission-mode"); got != "dontAsk" {
		t.Errorf("--permission-mode = %q, want dontAsk", got)
	}
}

// Agent mode still routes through the approval broker with the new flags in
// place — an optimisation must never quietly widen what the agent may do.
func TestTurnArgsAgentKeepsApprovalWiring(t *testing.T) {
	e := &Engine{
		BinPath:        "claude",
		Mode:           ModeAgent,
		PermPromptTool: "mcp__permctl__approve",
		MCPConfig:      `{"mcpServers":{"permctl":{}}}`,
	}
	args := e.turnArgs("привет", "", modernCaps)

	if got := argValue(args, "--permission-prompt-tool"); got != "mcp__permctl__approve" {
		t.Errorf("--permission-prompt-tool = %q, want the approval tool", got)
	}
	if got := argValue(args, "--permission-mode"); got != "default" {
		t.Errorf("--permission-mode = %q, want default", got)
	}
	if slices.Contains(args, "--dangerously-skip-permissions") {
		t.Error("approval bypass leaked into a normal agent turn")
	}
}
