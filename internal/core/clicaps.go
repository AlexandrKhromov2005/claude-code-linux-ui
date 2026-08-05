package core

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// CLICaps records which optional flags the installed claude binary accepts.
//
// The binary is user-supplied and versioned independently of this app, so a flag
// that exists today may be missing on an older install. An unknown flag makes the
// CLI exit non-zero, which would turn a token optimisation into a broken turn.
// Every optional flag is therefore probed once against `claude --help` and only
// passed when it is actually there; when the probe itself fails, nothing optional
// is sent and behaviour falls back to the previous, always-supported form.
type CLICaps struct {
	// ExcludeDynamicPrompt reports support for
	// --exclude-dynamic-system-prompt-sections, which moves cwd/env/git-status out
	// of the system prompt. Those sections change as the agent works, so leaving
	// them in re-creates the whole cached prefix on every turn.
	ExcludeDynamicPrompt bool

	// SystemPrompt reports support for --system-prompt, which replaces the default
	// Claude Code system prompt instead of appending to it.
	SystemPrompt bool

	// Tools reports support for --tools, which selects from the built-in tool set
	// ("" disables all of them).
	Tools bool

	// StrictMCPConfig reports support for --strict-mcp-config, which ignores every
	// ambient MCP configuration.
	StrictMCPConfig bool

	// SettingSources reports support for --setting-sources, which controls whether
	// user/project/local settings files are loaded at all.
	SettingSources bool

	// DisableSlashCommands reports support for --disable-slash-commands, which
	// keeps the skill catalogue out of the system prompt.
	DisableSlashCommands bool

	// NoSessionPersistence reports support for --no-session-persistence, so a side
	// call never leaves a resumable session behind.
	NoSessionPersistence bool

	// FallbackModel reports support for --fallback-model, used to survive an
	// overloaded primary model instead of failing the turn.
	FallbackModel bool
}

// capsCache memoises one probe per binary path; the probe spawns a process, and
// it runs on the path of every turn.
var (
	capsMu    sync.Mutex
	capsCache = map[string]CLICaps{}
)

// DetectCLICaps returns the capabilities of the claude binary at bin, probing it
// at most once per path. A failed probe is cached as "nothing optional is
// supported", which is the safe direction: the app keeps working exactly as it
// did before any of these flags existed.
func DetectCLICaps(bin string) CLICaps {
	capsMu.Lock()
	defer capsMu.Unlock()
	if c, ok := capsCache[bin]; ok {
		return c
	}
	c := probeCLICaps(bin)
	capsCache[bin] = c
	return c
}

// probeCLICaps runs `claude --help` and reads the supported flags out of it.
func probeCLICaps(bin string) CLICaps {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--help").Output()
	if err != nil {
		return CLICaps{}
	}
	return parseCLICaps(string(out))
}

// parseCLICaps maps a --help listing to the flags this app cares about. It
// matches on the flag token itself rather than on surrounding prose, so wording
// changes in the help text do not silently switch a capability off.
func parseCLICaps(help string) CLICaps {
	has := func(flag string) bool { return strings.Contains(help, flag) }
	return CLICaps{
		ExcludeDynamicPrompt: has("--exclude-dynamic-system-prompt-sections"),
		SystemPrompt:         has("--system-prompt"),
		Tools:                has("--tools"),
		StrictMCPConfig:      has("--strict-mcp-config"),
		SettingSources:       has("--setting-sources"),
		DisableSlashCommands: has("--disable-slash-commands"),
		NoSessionPersistence: has("--no-session-persistence"),
		FallbackModel:        has("--fallback-model"),
	}
}
