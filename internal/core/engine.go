package core

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// Mode selects the tool policy a turn runs under.
type Mode int

const (
	ModeChat  Mode = iota // read-only: Read, Grep, Glob; everything else denied
	ModeAgent             // full toolset, gated through the approval broker
)

func (m Mode) String() string {
	if m == ModeAgent {
		return "agent"
	}
	return "chat"
}

// ParseMode maps a string to a Mode, defaulting to chat.
func ParseMode(s string) Mode {
	if strings.EqualFold(strings.TrimSpace(s), "agent") {
		return ModeAgent
	}
	return ModeChat
}

// chatTools is the read-only allowlist for chat mode.
const chatTools = "Read,Grep,Glob"

// EffortLevels are the reasoning-effort levels accepted by --effort. An empty
// effort means "model default" (the flag is omitted).
var EffortLevels = []string{"low", "medium", "high", "xhigh", "max"}

// ValidEffort reports whether s is an accepted --effort level ("" = model default).
func ValidEffort(s string) bool {
	return s == "" || slices.Contains(EffortLevels, s)
}

// ValidEffortChoice reports whether s is a value the level picker accepts: an
// --effort level, "" (model default), or "ultracode" (a session setting that
// sends xhigh and orchestrates dynamic workflows, set via --settings).
func ValidEffortChoice(s string) bool {
	return s == "ultracode" || ValidEffort(s)
}

// EventKind classifies streamed events coming out of the claude CLI.
type EventKind int

const (
	EvText       EventKind = iota // a chunk of assistant text (token delta)
	EvToolStart                   // Claude started using a tool
	EvSystemInit                  // session metadata (model, session id)
	EvResult                      // turn finished successfully
	EvError                       // something went wrong
	EvRetry                       // API retry in progress
	EvNotice                      // an out-of-band notice from the core (e.g. budget)
)

// Event is the normalized unit a client consumes. It is deliberately a plain
// value type, not tied to any UI framework.
type Event struct {
	Kind      EventKind
	Text      string
	Tool      string
	Model     string
	SessionID string
	CostUSD   float64
	Attempt   int
	Err       error

	// Context-window usage from a result event. CtxUsed is the input-side token
	// count (input + cache read + cache creation), matching Claude Code's
	// used_percentage formula; CtxWindow is the model's context window size.
	CtxUsed   int
	CtxWindow int
}

// Engine drives Claude Code in headless mode (`claude -p`). It is configured
// per project/mode; the session id lives with the thread and is passed in per
// turn via Send so follow-ups continue through --resume.
type Engine struct {
	BinPath    string // path to the `claude` binary
	Model      string // optional --model override ("" = CLI default)
	Cwd        string // process working directory (project root)
	MemoryFile string // --append-system-prompt-file path ("" = none)
	Mode       Mode
	Effort     string // optional --effort level ("" = model default)

	// Agent-mode wiring, supplied by the permission service.
	PermPromptTool string // e.g. mcp__permctl__approve
	MCPConfig      string // inline JSON for --mcp-config
	SettingsJSON   string // inline JSON for --settings (allow/deny rules)

	// SkipPermissions runs agent mode with --dangerously-skip-permissions: every
	// tool is auto-allowed with no approval prompt. Opt-in and dangerous.
	SkipPermissions bool
}

// rawEvent is a permissive view of one NDJSON line from --output-format stream-json.
type rawEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	SessionID string  `json:"session_id"`
	Model     string  `json:"model"`
	Result    string  `json:"result"`
	TotalCost float64 `json:"total_cost_usd"`
	IsError   bool    `json:"is_error"`

	Attempt int `json:"attempt"`

	Event json.RawMessage `json:"event"`

	// result-only: token usage and per-model context window
	Usage struct {
		InputTokens         int `json:"input_tokens"`
		CacheCreationTokens int `json:"cache_creation_input_tokens"`
		CacheReadTokens     int `json:"cache_read_input_tokens"`
		OutputTokens        int `json:"output_tokens"`
	} `json:"usage"`
	ModelUsage map[string]struct {
		ContextWindow int `json:"contextWindow"`
	} `json:"modelUsage"`
}

// streamInner is the relevant slice of a raw Anthropic stream event.
type streamInner struct {
	Type  string `json:"type"`
	Delta struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"delta"`
	ContentBlock struct {
		Type string `json:"type"`
		Name string `json:"name"`
	} `json:"content_block"`
}

// Send launches one turn and returns a channel of events. resumeID continues an
// existing Claude session when non-empty. The channel is closed once the
// process exits. Cancel ctx to abort the turn.
func (e *Engine) Send(ctx context.Context, prompt, resumeID string) <-chan Event {
	out := make(chan Event, 128)

	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	if e.Model != "" {
		args = append(args, "--model", e.Model)
	}
	if e.Effort != "" {
		args = append(args, "--effort", e.Effort)
	}
	if e.SettingsJSON != "" {
		args = append(args, "--settings", e.SettingsJSON)
	}
	if e.MemoryFile != "" {
		if _, err := os.Stat(e.MemoryFile); err == nil {
			args = append(args, "--append-system-prompt-file", e.MemoryFile)
		}
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	args = append(args, e.modeArgs()...)

	cmd := exec.CommandContext(ctx, e.BinPath, args...)
	if e.Cwd != "" {
		cmd.Dir = e.Cwd
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		out <- Event{Kind: EvError, Err: err}
		close(out)
		return out
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		out <- Event{Kind: EvError, Err: fmt.Errorf("не удалось запустить %q: %w", e.BinPath, err)}
		close(out)
		return out
	}

	go func() {
		defer close(out)

		sc := bufio.NewScanner(stdout)
		// stream-json lines (and especially attached-file echoes) can be large.
		sc.Buffer(make([]byte, 0, 1<<20), 32<<20)

		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			var re rawEvent
			if json.Unmarshal([]byte(line), &re) != nil {
				continue
			}

			switch re.Type {
			case "system":
				switch re.Subtype {
				case "init":
					out <- Event{Kind: EvSystemInit, SessionID: re.SessionID, Model: re.Model}
				case "api_retry":
					out <- Event{Kind: EvRetry, Attempt: re.Attempt}
				}

			case "stream_event":
				var si streamInner
				if len(re.Event) > 0 {
					_ = json.Unmarshal(re.Event, &si)
				}
				if si.Delta.Type == "text_delta" && si.Delta.Text != "" {
					out <- Event{Kind: EvText, Text: si.Delta.Text}
				}
				if si.Type == "content_block_start" && si.ContentBlock.Type == "tool_use" {
					name := si.ContentBlock.Name
					if name == "" {
						name = "tool"
					}
					out <- Event{Kind: EvToolStart, Tool: name}
				}

			case "result":
				if re.IsError {
					msg := strings.TrimSpace(re.Result)
					if msg == "" {
						msg = "claude вернул ошибку"
					}
					out <- Event{Kind: EvError, Err: fmt.Errorf("%s", msg)}
				} else {
					ctxUsed := re.Usage.InputTokens + re.Usage.CacheReadTokens + re.Usage.CacheCreationTokens
					ctxWindow := 0
					model := re.Model
					for id, mu := range re.ModelUsage {
						if mu.ContextWindow > ctxWindow {
							ctxWindow = mu.ContextWindow
							model = id
						}
					}
					out <- Event{
						Kind: EvResult, SessionID: re.SessionID, CostUSD: re.TotalCost,
						Text: re.Result, Model: model, CtxUsed: ctxUsed, CtxWindow: ctxWindow,
					}
				}
			}
		}

		if err := cmd.Wait(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			out <- Event{Kind: EvError, Err: fmt.Errorf("%s", msg)}
		}
	}()

	return out
}

// modeArgs returns the tool-policy flags for the engine's current mode.
func (e *Engine) modeArgs() []string {
	switch e.Mode {
	case ModeAgent:
		if e.SkipPermissions {
			return []string{"--dangerously-skip-permissions"}
		}
		args := []string{"--permission-mode", "default"}
		if e.PermPromptTool != "" {
			args = append(args, "--permission-prompt-tool", e.PermPromptTool)
		}
		if e.MCPConfig != "" {
			args = append(args, "--mcp-config", e.MCPConfig)
		}
		return args
	default:
		return []string{"--allowedTools", chatTools, "--permission-mode", "dontAsk"}
	}
}
