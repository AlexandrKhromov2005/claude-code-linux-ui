package core

import (
	"encoding/json"
	"time"
)

// A subagent is a nested Claude Code session the main model launches through the
// Agent tool. It is asynchronous: the tool call returns as soon as the subagent
// starts, the main model keeps talking (and may even finish its reply), and the
// subagent's own work arrives later on the same stream. Without a separate
// account of it the client looks idle while real work is happening, which is
// exactly the case this file exists to make visible.
//
// The CLI reports subagents through four `system` events — task_started,
// task_progress, task_updated, task_notification — plus the subagent's own
// assistant/user messages, which carry parent_tool_use_id. The messages are the
// finest-grained proof of life: one arrives every time the subagent says or does
// anything, so their timing is what separates "working" from "gone quiet".
//
// (There is a fifth event, background_tasks_changed, holding the full list of
// running tasks. It is deliberately not consumed: the four above already cover
// the lifecycle, and reconciling against a list that is emitted both before a
// task is announced and after it ends would end a subagent on an ordering quirk.)

// AgentStatus is where one subagent stands in its lifecycle.
type AgentStatus string

const (
	AgentRunning AgentStatus = "running" // started, no end reported yet
	AgentDone    AgentStatus = "done"    // finished normally
	AgentFailed  AgentStatus = "failed"  // the CLI reported a non-success end
	AgentAborted AgentStatus = "aborted" // the turn ended while it was still running
)

// Silence thresholds for a running subagent. A subagent only speaks between
// steps — one tool call or one model call and it is quiet for as long as that
// takes — so these are set past what healthy work looks like: measured against
// claude 2.1.228, a trivial subagent still went ~35s without a word while
// composing its answer. Silence is reported as an observation, never as death;
// only the turn ending decides that.
const (
	AgentQuietAfter  = 45 * time.Second  // longer than a normal step: worth showing
	AgentSilentAfter = 150 * time.Second // long enough that something is likely wrong
)

// AgentState is one subagent as last observed.
//
// ID is the CLI's task_id, which identifies the agent slot rather than a single
// assignment: the main model can hand the same slot a new job when the previous
// one is done, and does so routinely. The slot is therefore the entity shown to
// the user, with Runs counting how many jobs it has been given.
type AgentState struct {
	ID       string      `json:"id"`       // task_id — the agent slot
	Type     string      `json:"type"`     // subagent_type, e.g. "general-purpose"
	Task     string      `json:"task"`     // what it was launched to do
	Activity string      `json:"activity"` // what it reported doing most recently
	Tool     string      `json:"tool"`     // the tool behind that activity
	Status   AgentStatus `json:"status"`
	Tokens   int         `json:"tokens"`   // tokens the subagent itself has spent
	ToolUses int         `json:"toolUses"` // tool calls it has made
	Runs     int         `json:"runs"`     // jobs this slot has been given

	// Wall-clock, in unix milliseconds, on the machine running the CLI. The web
	// client is told the server's clock alongside these so a browser on the far
	// end of an SSH tunnel measures ages against the same clock.
	StartedAt int64 `json:"startedAt"`
	LastSeen  int64 `json:"lastSeen"` // last signal of any kind
	EndedAt   int64 `json:"endedAt"`  // 0 while running
}

// Live reports whether the subagent is still expected to be working.
func (a AgentState) Live() bool { return a.Status == AgentRunning }

// Silence is how long the subagent has said nothing, as of now. It stops
// growing once the subagent has ended.
func (a AgentState) Silence(now time.Time) time.Duration {
	if a.LastSeen == 0 {
		return 0
	}
	end := now
	if !a.Live() && a.EndedAt > 0 {
		end = time.UnixMilli(a.EndedAt)
	}
	d := end.Sub(time.UnixMilli(a.LastSeen))
	if d < 0 {
		return 0
	}
	return d
}

// Elapsed is how long the subagent has been working, or worked in total.
func (a AgentState) Elapsed(now time.Time) time.Duration {
	if a.StartedAt == 0 {
		return 0
	}
	end := now
	if !a.Live() && a.EndedAt > 0 {
		end = time.UnixMilli(a.EndedAt)
	}
	d := end.Sub(time.UnixMilli(a.StartedAt))
	if d < 0 {
		return 0
	}
	return d
}

// agentSignal is one parsed subagent event, normalized across the four `system`
// subtypes that carry them.
type agentSignal struct {
	kind      string // "start" | "progress" | "end"
	id        string // task_id
	toolUseID string
	agentType string
	desc      string
	tool      string
	tokens    int
	toolUses  int
	status    AgentStatus // "end" only
}

// agentTracker keeps the live picture of a turn's subagents. It is owned by the
// goroutine reading one turn's stream, so it needs no locking.
type agentTracker struct {
	order  []string // task_ids, in the order they first appeared
	byID   map[string]*AgentState
	byTool map[string]string // tool_use_id -> task_id, for heartbeats
	now    func() time.Time
}

func newAgentTracker() *agentTracker {
	return &agentTracker{byID: map[string]*AgentState{}, byTool: map[string]string{}, now: time.Now}
}

// apply folds one signal in and reports whether the visible state changed.
func (t *agentTracker) apply(s agentSignal) bool {
	if s.id == "" {
		return false
	}
	ms := t.now().UnixMilli()
	a := t.byID[s.id]
	if a == nil {
		if s.kind == "end" {
			return false // an end for something we never saw start
		}
		a = &AgentState{ID: s.id, StartedAt: ms}
		t.byID[s.id] = a
		t.order = append(t.order, s.id)
	}
	if s.toolUseID != "" {
		t.byTool[s.toolUseID] = s.id
	}
	if s.agentType != "" {
		a.Type = s.agentType
	}
	a.LastSeen = ms

	switch s.kind {
	case "start":
		// A slot being started again means a fresh job on the same agent, so the
		// clock and the finished state restart with it.
		a.Runs++
		a.Status = AgentRunning
		a.StartedAt = ms
		a.EndedAt = 0
		a.Activity = ""
		a.Tool = ""
		if s.desc != "" {
			a.Task = s.desc
		}
	case "progress":
		a.Status = AgentRunning
		a.EndedAt = 0
		a.Activity = s.desc
		a.Tool = s.tool
		if s.tokens > a.Tokens {
			a.Tokens = s.tokens
		}
		if s.toolUses > a.ToolUses {
			a.ToolUses = s.toolUses
		}
	case "end":
		// task_updated and task_notification both announce the same ending, and
		// only the second carries the final tally. So the repeat is not ignored
		// outright — it is reported as a change only if it actually added
		// something the user can see.
		repeat := !a.Live()
		grew := s.tokens > a.Tokens || s.toolUses > a.ToolUses
		if !repeat {
			a.Status = s.status
			a.EndedAt = ms
			a.Activity = ""
		}
		if s.tokens > a.Tokens {
			a.Tokens = s.tokens
		}
		if s.toolUses > a.ToolUses {
			a.ToolUses = s.toolUses
		}
		return !repeat || grew
	}
	return true
}

// touch records proof of life from a subagent's own message. It never changes a
// finished subagent's status: a straggling message from a job that has already
// ended is evidence of when it last spoke, not that it is running again.
func (t *agentTracker) touch(toolUseID string) bool {
	id := t.byTool[toolUseID]
	if id == "" {
		return false
	}
	a := t.byID[id]
	if a == nil || !a.Live() {
		return false
	}
	a.LastSeen = t.now().UnixMilli()
	return true
}

// abortRunning marks everything still running as cut short, which is what the
// turn's stream ending means for a subagent that never reported back.
func (t *agentTracker) abortRunning() bool {
	ms := t.now().UnixMilli()
	changed := false
	for _, a := range t.byID {
		if a.Live() {
			a.Status = AgentAborted
			a.EndedAt = ms
			a.Activity = ""
			changed = true
		}
	}
	return changed
}

// any reports whether any subagent has been seen this turn.
func (t *agentTracker) any() bool { return len(t.order) > 0 }

// snapshot copies the current picture, oldest agent first.
func (t *agentTracker) snapshot() []AgentState {
	out := make([]AgentState, 0, len(t.order))
	for _, id := range t.order {
		if a := t.byID[id]; a != nil {
			out = append(out, *a)
		}
	}
	return out
}

// RunningAgents counts the subagents in a snapshot that are still working.
func RunningAgents(list []AgentState) int {
	n := 0
	for _, a := range list {
		if a.Live() {
			n++
		}
	}
	return n
}

// taskRaw is the union of the fields the four task events carry. They are parsed
// separately from the main event struct because task_progress reuses the `usage`
// key with a different shape than a result event's.
type taskRaw struct {
	TaskID       string `json:"task_id"`
	ToolUseID    string `json:"tool_use_id"`
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type"`
	LastToolName string `json:"last_tool_name"`
	Status       string `json:"status"` // task_notification
	Patch        struct {
		Status string `json:"status"` // task_updated
	} `json:"patch"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
		ToolUses    int `json:"tool_uses"`
	} `json:"usage"`
}

// parseAgentSignal turns one `system` line into a signal, or reports false if
// the subtype is not about subagents.
func parseAgentSignal(subtype string, line []byte) (agentSignal, bool) {
	var kind string
	switch subtype {
	case "task_started":
		kind = "start"
	case "task_progress":
		kind = "progress"
	case "task_updated", "task_notification":
		kind = "end"
	default:
		return agentSignal{}, false
	}
	var tr taskRaw
	if json.Unmarshal(line, &tr) != nil || tr.TaskID == "" {
		return agentSignal{}, false
	}
	s := agentSignal{
		kind:      kind,
		id:        tr.TaskID,
		toolUseID: tr.ToolUseID,
		agentType: tr.SubagentType,
		desc:      tr.Description,
		tool:      tr.LastToolName,
		tokens:    tr.Usage.TotalTokens,
		toolUses:  tr.Usage.ToolUses,
	}
	if kind == "end" {
		status := tr.Status
		if status == "" {
			status = tr.Patch.Status
		}
		if status == "" {
			// An end with no verdict is not an end we can trust; treating it as
			// completion would silently retire a subagent that is still working.
			return agentSignal{}, false
		}
		s.status = agentEndStatus(status)
	}
	return s, true
}

// agentEndStatus maps the CLI's end status onto ours, treating anything that is
// not an explicit success as a failure rather than quietly calling it done.
func agentEndStatus(s string) AgentStatus {
	switch s {
	case "completed", "success", "succeeded", "done":
		return AgentDone
	default:
		return AgentFailed
	}
}
