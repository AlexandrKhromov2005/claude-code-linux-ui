package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// EventKind classifies streamed events coming out of the claude CLI.
type EventKind int

const (
	EvText       EventKind = iota // a chunk of assistant text (token delta)
	EvToolStart                   // Claude started using a tool
	EvSystemInit                  // session metadata (model, session id)
	EvResult                      // turn finished successfully
	EvError                       // something went wrong
	EvRetry                       // API retry in progress
)

// Event is the normalized unit the UI consumes.
type Event struct {
	Kind      EventKind
	Text      string
	Tool      string
	Model     string
	SessionID string
	CostUSD   float64
	Attempt   int
	Err       error
}

// Engine drives Claude Code in headless mode (`claude -p`) and keeps the
// session id so follow-up turns continue the same conversation via --resume.
type Engine struct {
	BinPath      string // path to the `claude` binary
	Model        string // optional --model override ("" = CLI default)
	AllowedTools string // comma-separated --allowedTools (read-only by default)

	sessionID string // captured from the first turn, reused on --resume
}

// rawEvent is a permissive view of one NDJSON line from --output-format stream-json.
type rawEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`

	// system/init + result
	SessionID string  `json:"session_id"`
	Model     string  `json:"model"`
	Result    string  `json:"result"`
	TotalCost float64 `json:"total_cost_usd"`
	IsError   bool    `json:"is_error"`

	// system/api_retry
	Attempt int `json:"attempt"`

	// stream_event carries the raw Anthropic streaming event
	Event json.RawMessage `json:"event"`
}

// streamInner is the relevant slice of a raw Anthropic stream event.
type streamInner struct {
	Type  string `json:"type"` // message_start, content_block_start, content_block_delta, ...
	Delta struct {
		Type string `json:"type"` // text_delta
		Text string `json:"text"`
	} `json:"delta"`
	ContentBlock struct {
		Type string `json:"type"` // tool_use, text
		Name string `json:"name"`
	} `json:"content_block"`
}

// SessionID exposes the current session id (empty before the first turn).
func (e *Engine) SessionID() string { return e.sessionID }

// ResetSession forgets the conversation so the next turn starts fresh.
func (e *Engine) ResetSession() { e.sessionID = "" }

// Send launches one turn and returns a channel of events. The channel is
// closed once the underlying process exits. Cancel ctx to abort the turn.
func (e *Engine) Send(ctx context.Context, prompt string) <-chan Event {
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
	if e.AllowedTools != "" {
		args = append(args, "--allowedTools", e.AllowedTools)
	}
	if e.sessionID != "" {
		args = append(args, "--resume", e.sessionID)
	}

	cmd := exec.CommandContext(ctx, e.BinPath, args...)
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
				continue // ignore anything that isn't a JSON event
			}

			switch re.Type {
			case "system":
				switch re.Subtype {
				case "init":
					if re.SessionID != "" {
						e.sessionID = re.SessionID
					}
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
				if re.SessionID != "" {
					e.sessionID = re.SessionID
				}
				if re.IsError {
					msg := strings.TrimSpace(re.Result)
					if msg == "" {
						msg = "claude вернул ошибку"
					}
					out <- Event{Kind: EvError, Err: fmt.Errorf("%s", msg)}
				} else {
					out <- Event{Kind: EvResult, SessionID: re.SessionID, CostUSD: re.TotalCost, Text: re.Result}
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
