package core

import (
	"bufio"
	"bytes"
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
	EvRateLimit                   // a subscription rate-limit status update
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

	// Usage is what the turn actually billed, summed over every tool iteration.
	// It is the accounting view (how many tokens this turn cost), as opposed to
	// CtxUsed above, which is the live context view (how full the window is).
	Usage TokenUsage

	// Subscription rate-limit status from a rate_limit_event. The headless CLI
	// reports the binding limit's type, reset time and status (no percentage).
	LimitType   string // "five_hour" | "seven_day"
	LimitResets int64  // unix seconds when the window resets
	LimitStatus string // e.g. "allowed"
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
	MCPConfig      string // inline JSON for --mcp-config (permission server)
	SettingsJSON   string // inline JSON for --settings (allow/deny rules)

	// ExtraMCP holds user-configured research MCP servers (mcp.json), merged
	// into the agent-mode --mcp-config alongside the permission server. Their
	// tools are gated through the same approval modal as any other tool.
	ExtraMCP map[string]json.RawMessage

	// SkipPermissions runs agent mode with --dangerously-skip-permissions: every
	// tool is auto-allowed with no approval prompt. Opt-in and dangerous.
	SkipPermissions bool

	// FallbackModel is passed to --fallback-model so an overloaded primary model
	// degrades to a working one instead of failing the turn ("" = no fallback).
	FallbackModel string
}

// caps returns the installed CLI's optional-flag support (probed once per binary).
func (e *Engine) caps() CLICaps { return DetectCLICaps(e.BinPath) }

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

	// assistant message: per-call usage; the last one reflects current context
	Message struct {
		Usage struct {
			InputTokens         int `json:"input_tokens"`
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`

	// rate_limit_event: binding subscription limit
	RateLimitInfo struct {
		Status        string `json:"status"`
		ResetsAt      int64  `json:"resetsAt"`
		RateLimitType string `json:"rateLimitType"`
	} `json:"rate_limit_info"`
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

	args := e.turnArgs(prompt, resumeID, e.caps())
	if debugEnabled() {
		fmt.Fprintf(os.Stderr, "[cclu] turn args: %q\n", args)
	}
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

		// lastCtx tracks the most recent API call's context size (input + cache).
		// The result event's top-level usage sums every tool iteration, so it
		// overcounts; the last assistant message reflects the live context.
		var lastCtx int

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

			case "assistant":
				if c := re.Message.Usage.InputTokens + re.Message.Usage.CacheReadTokens + re.Message.Usage.CacheCreationTokens; c > 0 {
					lastCtx = c
				}

			case "rate_limit_event":
				if re.RateLimitInfo.RateLimitType != "" {
					out <- Event{
						Kind:        EvRateLimit,
						LimitType:   re.RateLimitInfo.RateLimitType,
						LimitResets: re.RateLimitInfo.ResetsAt,
						LimitStatus: re.RateLimitInfo.Status,
					}
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
					if debugEnabled() {
						fmt.Fprintf(os.Stderr, "[cclu] turn usage: input=%d cache_read=%d cache_creation=%d output=%d\n",
							re.Usage.InputTokens, re.Usage.CacheReadTokens, re.Usage.CacheCreationTokens, re.Usage.OutputTokens)
					}
					// Prefer the last assistant call's context; fall back to the
					// (aggregate) result usage only if no assistant usage was seen.
					ctxUsed := lastCtx
					if ctxUsed == 0 {
						ctxUsed = re.Usage.InputTokens + re.Usage.CacheReadTokens + re.Usage.CacheCreationTokens
					}
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
						Usage: TokenUsage{
							Input:       re.Usage.InputTokens,
							CacheRead:   re.Usage.CacheReadTokens,
							CacheCreate: re.Usage.CacheCreationTokens,
							Output:      re.Usage.OutputTokens,
						},
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

// debugEnabled reports whether verbose per-turn diagnostics are on. It is gated
// by CCLU_DEBUG so it never touches normal output unless explicitly switched on.
func debugEnabled() bool {
	v := os.Getenv("CCLU_DEBUG")
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// SideCall is a self-contained text task run outside the conversation — memory
// upkeep, thread summarisation — where the model is asked to transform text it
// is handed and nothing else.
//
// It exists as its own type because running such a task through a plain
// `claude -p` is startlingly expensive. The CLI is built for coding sessions, so
// by default it assembles its full system prompt, discovers CLAUDE.md up the
// tree, declares every built-in tool, loads every ambient MCP server and lists
// the skill catalogue — measured at ~30k input tokens before the actual prompt is
// even considered. For a turn that reads two paragraphs and rewrites a bullet
// list, all of it is waste, and it was being paid once per turn.
//
// So a side call strips the environment down to the task: the default system
// prompt is replaced rather than appended to, no tools are declared, no MCP
// server is loaded, no settings file is read, no skills are listed and no
// resumable session is left behind. It runs in a neutral directory so nothing
// project-scoped is discovered either. The same request then costs a few hundred
// tokens.
//
// Every one of those flags is optional and version-dependent, so each is applied
// only where the installed CLI advertises it; on an older binary the call still
// runs, just without the savings.
type SideCall struct {
	Bin          string // path to the claude binary
	Model        string // model alias ("" = CLI default)
	Effort       string // reasoning effort ("" = model default)
	SystemPrompt string // replaces the default system prompt entirely
	Prompt       string // the task itself
}

// Run executes the side call, returning the result text and what it consumed.
// The usage is reported even on success-with-empty-result so callers can account
// for background spend rather than letting it go unmeasured.
func (c SideCall) Run(ctx context.Context) (string, TokenUsage, error) {
	args := []string{"-p", c.Prompt, "--output-format", "json", "--permission-mode", "dontAsk"}
	if c.Model != "" {
		args = append(args, "--model", c.Model)
	}
	if c.Effort != "" {
		args = append(args, "--effort", c.Effort)
	}

	caps := DetectCLICaps(c.Bin)
	if c.SystemPrompt != "" && caps.SystemPrompt {
		args = append(args, "--system-prompt", c.SystemPrompt)
	}
	if caps.Tools {
		// An empty tool set is the point: this task is pure text transformation,
		// and the text it is handed comes from a conversation, so a model with
		// tools here would be both an expense and an unnecessary way for that
		// text to reach the filesystem.
		args = append(args, "--tools", "")
	}
	if caps.StrictMCPConfig {
		args = append(args, "--strict-mcp-config")
	}
	if caps.SettingSources {
		args = append(args, "--setting-sources", "")
	}
	if caps.DisableSlashCommands {
		args = append(args, "--disable-slash-commands")
	}
	if caps.NoSessionPersistence {
		args = append(args, "--no-session-persistence")
	}

	cmd := exec.CommandContext(ctx, c.Bin, args...)
	// Deliberately not the project directory: a side call needs no project
	// context, and a neutral cwd keeps CLAUDE.md discovery and directory-scoped
	// MCP servers out of the request on CLI versions that lack the flags above.
	cmd.Dir = os.TempDir()

	out, err := cmd.Output()
	if err != nil {
		return "", TokenUsage{}, err
	}
	var r struct {
		Result  string `json:"result"`
		IsError bool   `json:"is_error"`
		Usage   struct {
			InputTokens         int `json:"input_tokens"`
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
			OutputTokens        int `json:"output_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(out, &r) != nil {
		return "", TokenUsage{}, fmt.Errorf("не удалось разобрать ответ claude")
	}
	usage := TokenUsage{
		Input:       r.Usage.InputTokens,
		CacheRead:   r.Usage.CacheReadTokens,
		CacheCreate: r.Usage.CacheCreationTokens,
		Output:      r.Usage.OutputTokens,
	}
	if r.IsError {
		return "", usage, fmt.Errorf("claude вернул ошибку")
	}
	return r.Result, usage, nil
}

// turnArgs assembles the full command line for one turn. It takes caps as a
// parameter rather than probing, so the flag policy can be exercised against
// both a current and an older CLI without spawning anything.
func (e *Engine) turnArgs(prompt, resumeID string, caps CLICaps) []string {
	args := []string{
		"-p", prompt,
		"--output-format", "stream-json",
		"--verbose",
		"--include-partial-messages",
	}
	// Every turn is a fresh process that rebuilds the system prompt, and the
	// default prompt embeds per-machine sections — cwd, env, memory paths and git
	// status. Git status changes the moment the agent edits a file, so those
	// sections differ from what the previous turn cached, and the bytes after them
	// have to be written into the cache again rather than read back.
	//
	// Moving them into the first user message keeps more of the prefix stable.
	// Measured over three turns against fable, this shifts roughly 2k tokens per
	// turn from cache-write pricing to cache-read pricing — worthwhile and free,
	// though not the dominant cost. The model still receives the same
	// information, just further down.
	if caps.ExcludeDynamicPrompt {
		args = append(args, "--exclude-dynamic-system-prompt-sections")
	}
	if e.Model != "" {
		args = append(args, "--model", e.Model)
	}
	if e.FallbackModel != "" && caps.FallbackModel {
		args = append(args, "--fallback-model", e.FallbackModel)
	}
	if e.Effort != "" {
		args = append(args, "--effort", e.Effort)
	}
	if e.SettingsJSON != "" {
		args = append(args, "--settings", e.SettingsJSON)
	}
	// memory.md starts empty and usually stays that way, and the runtime file
	// mirroring it therefore exists but holds nothing. Passing it anyway asked the
	// CLI to append an empty system prompt on every turn — harmless but pointless,
	// so the file is checked for content rather than mere existence.
	if e.MemoryFile != "" && fileHasContent(e.MemoryFile) {
		args = append(args, "--append-system-prompt-file", e.MemoryFile)
	}
	if resumeID != "" {
		args = append(args, "--resume", resumeID)
	}
	return append(args, e.modeArgs()...)
}

// fileHasContent reports whether path holds anything but whitespace. A missing
// or unreadable file counts as empty, so a broken path silently costs nothing
// rather than degrading every turn.
func fileHasContent(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return len(bytes.TrimSpace(b)) > 0
}

// modeArgs returns the tool-policy flags for the engine's current mode. In agent
// mode it also folds any research MCP servers into the --mcp-config: they ride
// alongside the permission server, and under --dangerously-skip-permissions they
// travel on their own (there is no permission server to merge with then).
func (e *Engine) modeArgs() []string {
	if e.Mode != ModeAgent {
		return []string{"--allowedTools", chatTools, "--permission-mode", "dontAsk"}
	}
	var args []string
	if e.SkipPermissions {
		args = []string{"--dangerously-skip-permissions"}
	} else {
		args = []string{"--permission-mode", "default"}
		if e.PermPromptTool != "" {
			args = append(args, "--permission-prompt-tool", e.PermPromptTool)
		}
	}
	if cfg := mergeMCPConfig(e.MCPConfig, e.ExtraMCP); cfg != "" {
		args = append(args, "--mcp-config", cfg)
	}
	return args
}
