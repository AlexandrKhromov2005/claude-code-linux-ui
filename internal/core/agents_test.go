package core

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// frozen returns a tracker whose clock the test drives by hand.
func frozen(t0 time.Time) (*agentTracker, *time.Time) {
	now := t0
	tr := newAgentTracker()
	tr.now = func() time.Time { return now }
	return tr, &now
}

func TestParseAgentSignal(t *testing.T) {
	started := `{"type":"system","subtype":"task_started","task_id":"af33","tool_use_id":"toolu_01NW","description":"Read README","subagent_type":"general-purpose","task_type":"local_agent"}`
	sig, ok := parseAgentSignal("task_started", []byte(started))
	if !ok || sig.kind != "start" {
		t.Fatalf("task_started: ok=%v kind=%q", ok, sig.kind)
	}
	if sig.id != "af33" || sig.toolUseID != "toolu_01NW" || sig.agentType != "general-purpose" || sig.desc != "Read README" {
		t.Fatalf("task_started parsed as %+v", sig)
	}

	progress := `{"type":"system","subtype":"task_progress","task_id":"af33","tool_use_id":"toolu_01NW","description":"Reading README.md","subagent_type":"general-purpose","usage":{"total_tokens":15742,"tool_uses":1,"duration_ms":5296},"last_tool_name":"Read"}`
	sig, ok = parseAgentSignal("task_progress", []byte(progress))
	if !ok || sig.kind != "progress" {
		t.Fatalf("task_progress: ok=%v kind=%q", ok, sig.kind)
	}
	if sig.tool != "Read" || sig.tokens != 15742 || sig.toolUses != 1 || sig.desc != "Reading README.md" {
		t.Fatalf("task_progress parsed as %+v", sig)
	}

	updated := `{"type":"system","subtype":"task_updated","task_id":"af33","patch":{"status":"completed","end_time":1786566490307}}`
	sig, ok = parseAgentSignal("task_updated", []byte(updated))
	if !ok || sig.kind != "end" || sig.status != AgentDone {
		t.Fatalf("task_updated parsed as %+v (ok=%v)", sig, ok)
	}

	notification := `{"type":"system","subtype":"task_notification","task_id":"af33","tool_use_id":"toolu_01NW","status":"completed","usage":{"total_tokens":22559,"tool_uses":1,"duration_ms":37653}}`
	sig, ok = parseAgentSignal("task_notification", []byte(notification))
	if !ok || sig.status != AgentDone || sig.tokens != 22559 {
		t.Fatalf("task_notification parsed as %+v (ok=%v)", sig, ok)
	}

	// Anything other than an explicit success ends the subagent as failed.
	failed := `{"type":"system","subtype":"task_updated","task_id":"af33","patch":{"status":"error"}}`
	if sig, _ := parseAgentSignal("task_updated", []byte(failed)); sig.status != AgentFailed {
		t.Errorf("error status = %q, want failed", sig.status)
	}

	// An ending with no verdict at all must not retire a working subagent.
	if _, ok := parseAgentSignal("task_updated", []byte(`{"task_id":"af33","patch":{}}`)); ok {
		t.Error("a statusless task_updated must be ignored")
	}
	if _, ok := parseAgentSignal("task_started", []byte(`{"description":"no id"}`)); ok {
		t.Error("a task event without task_id must be ignored")
	}
	for _, other := range []string{"init", "api_retry", "background_tasks_changed", "thinking_tokens", "status"} {
		if _, ok := parseAgentSignal(other, []byte(`{"task_id":"af33"}`)); ok {
			t.Errorf("subtype %q must not read as a subagent signal", other)
		}
	}
}

func TestAgentTrackerLifecycle(t *testing.T) {
	tr, now := frozen(time.Unix(1000, 0))

	if !tr.apply(agentSignal{kind: "start", id: "af33", toolUseID: "toolu_01NW", agentType: "general-purpose", desc: "Read README"}) {
		t.Fatal("start must register a change")
	}
	if !tr.any() {
		t.Fatal("tracker reports no agents after a start")
	}

	*now = now.Add(5 * time.Second)
	tr.apply(agentSignal{kind: "progress", id: "af33", toolUseID: "toolu_01NW", desc: "Reading README.md", tool: "Read", tokens: 15742, toolUses: 1})

	snap := tr.snapshot()
	if len(snap) != 1 {
		t.Fatalf("snapshot has %d agents, want 1", len(snap))
	}
	a := snap[0]
	if a.Status != AgentRunning || !a.Live() {
		t.Errorf("status = %q, want running", a.Status)
	}
	if a.Task != "Read README" || a.Activity != "Reading README.md" || a.Tool != "Read" {
		t.Errorf("agent described as %+v", a)
	}
	if a.Tokens != 15742 || a.ToolUses != 1 || a.Runs != 1 {
		t.Errorf("counters = tokens %d, tools %d, runs %d", a.Tokens, a.ToolUses, a.Runs)
	}
	if RunningAgents(snap) != 1 {
		t.Errorf("RunningAgents = %d, want 1", RunningAgents(snap))
	}

	*now = now.Add(30 * time.Second)
	tr.apply(agentSignal{kind: "end", id: "af33", status: AgentDone, tokens: 22559})

	a = tr.snapshot()[0]
	if a.Status != AgentDone || a.Live() {
		t.Errorf("status = %q, want done", a.Status)
	}
	if a.Tokens != 22559 {
		t.Errorf("tokens = %d, want the final count", a.Tokens)
	}
	if a.Activity != "" {
		t.Errorf("a finished agent still claims to be %q", a.Activity)
	}
	if a.Elapsed(*now) != 35*time.Second {
		t.Errorf("elapsed = %v, want 35s", a.Elapsed(*now))
	}
	// Time keeps passing, but a finished agent's clocks stop with it.
	later := now.Add(time.Hour)
	if a.Elapsed(later) != 35*time.Second || a.Silence(later) != 0 {
		t.Errorf("finished agent kept ageing: elapsed %v, silence %v", a.Elapsed(later), a.Silence(later))
	}
	if RunningAgents(tr.snapshot()) != 0 {
		t.Error("finished agent still counted as running")
	}

	// The CLI announces the same ending twice (task_updated, then
	// task_notification). A repeat that says nothing new is not a change...
	if tr.apply(agentSignal{kind: "end", id: "af33", status: AgentDone}) {
		t.Error("a repeated ending with nothing new must not register as a change")
	}
	// ...but the repeat is where the final tally arrives, and that is.
	if !tr.apply(agentSignal{kind: "end", id: "af33", status: AgentDone, tokens: 30000, toolUses: 3}) {
		t.Error("a repeated ending carrying the final tally must register")
	}
	if a := tr.snapshot()[0]; a.Tokens != 30000 || a.ToolUses != 3 {
		t.Errorf("final tally not recorded: tokens %d, tools %d", a.Tokens, a.ToolUses)
	}
	// Nor may an ending arrive for an agent that never started.
	if tr.apply(agentSignal{kind: "end", id: "unknown", status: AgentDone}) {
		t.Error("an ending for an unknown agent must be ignored")
	}
}

// The CLI hands the same task_id a new job when the previous one is done, so a
// second start is a restart of that slot rather than a second agent.
func TestAgentTrackerSlotReuse(t *testing.T) {
	tr, now := frozen(time.Unix(1000, 0))
	tr.apply(agentSignal{kind: "start", id: "abd6", toolUseID: "toolu_01TN", desc: "Count lines"})
	tr.apply(agentSignal{kind: "progress", id: "abd6", toolUseID: "toolu_01TN", desc: "Reading README.md", tool: "Read"})
	tr.apply(agentSignal{kind: "end", id: "abd6", status: AgentDone})

	*now = now.Add(time.Minute)
	tr.apply(agentSignal{kind: "start", id: "abd6", toolUseID: "toolu_01TV", desc: "Count lines"})

	snap := tr.snapshot()
	if len(snap) != 1 {
		t.Fatalf("slot reuse produced %d agents, want 1", len(snap))
	}
	a := snap[0]
	if a.Status != AgentRunning || a.EndedAt != 0 {
		t.Errorf("restarted agent = %+v, want running with no end", a)
	}
	if a.Runs != 2 {
		t.Errorf("runs = %d, want 2", a.Runs)
	}
	if a.StartedAt != now.UnixMilli() {
		t.Error("restarted agent kept the previous run's start time")
	}
	if a.Activity != "" || a.Tool != "" {
		t.Errorf("restarted agent kept stale activity %q/%q", a.Activity, a.Tool)
	}

	// The old assignment's tool_use_id still points at this slot, but a straggling
	// message from it must not be mistaken for the new run going quiet.
	if !tr.touch("toolu_01TN") {
		t.Error("a message from a previous assignment is still proof of life")
	}
}

func TestAgentTrackerTouch(t *testing.T) {
	tr, now := frozen(time.Unix(1000, 0))
	tr.apply(agentSignal{kind: "start", id: "af33", toolUseID: "toolu_01NW"})
	start := tr.snapshot()[0].LastSeen

	if tr.touch("toolu_unknown") {
		t.Error("a heartbeat from an unknown tool call must be ignored")
	}

	*now = now.Add(9 * time.Second)
	if !tr.touch("toolu_01NW") {
		t.Fatal("a heartbeat from a running agent must register")
	}
	a := tr.snapshot()[0]
	if a.LastSeen <= start {
		t.Error("heartbeat did not move the last-seen mark")
	}
	if a.Silence(*now) != 0 {
		t.Errorf("silence = %v right after a heartbeat, want 0", a.Silence(*now))
	}

	*now = now.Add(40 * time.Second)
	if got := tr.snapshot()[0].Silence(*now); got != 40*time.Second {
		t.Errorf("silence = %v, want 40s", got)
	}

	// A message that trails in after the job ended says when the agent last
	// spoke; it must not put a finished agent back to work.
	tr.apply(agentSignal{kind: "end", id: "af33", status: AgentDone})
	if tr.touch("toolu_01NW") {
		t.Error("a heartbeat resurrected a finished agent")
	}
	if tr.snapshot()[0].Status != AgentDone {
		t.Error("finished agent went back to running")
	}
}

func TestAgentTrackerAbortRunning(t *testing.T) {
	tr, now := frozen(time.Unix(1000, 0))
	tr.apply(agentSignal{kind: "start", id: "a1", toolUseID: "t1"})
	tr.apply(agentSignal{kind: "start", id: "a2", toolUseID: "t2"})
	tr.apply(agentSignal{kind: "end", id: "a2", status: AgentDone})

	*now = now.Add(10 * time.Second)
	if !tr.abortRunning() {
		t.Fatal("aborting a running agent must register as a change")
	}
	snap := tr.snapshot()
	if snap[0].ID != "a1" || snap[1].ID != "a2" {
		t.Fatalf("snapshot lost its order: %q, %q", snap[0].ID, snap[1].ID)
	}
	if snap[0].Status != AgentAborted || snap[0].EndedAt == 0 {
		t.Errorf("running agent = %+v, want aborted", snap[0])
	}
	if snap[1].Status != AgentDone {
		t.Errorf("finished agent = %q, want it left alone", snap[1].Status)
	}
	if tr.abortRunning() {
		t.Error("a second abort found something left to abort")
	}
}

// collect drains scanStream over a recorded transcript.
func collect(t *testing.T, ndjson string) []Event {
	t.Helper()
	ch := make(chan Event, 512)
	go func() {
		scanStream(strings.NewReader(ndjson), ch)
		close(ch)
	}()
	var out []Event
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func lastAgents(evs []Event) []AgentState {
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Kind == EvAgents {
			return evs[i].Agents
		}
	}
	return nil
}

// A subagent turn, in the shape claude 2.1.228 streams it: the Agent tool
// returns immediately, the model finishes a first reply, the subagent works and
// reports back, and the model replies again — two result events in one turn.
const subagentTranscript = `
{"type":"system","subtype":"init","session_id":"sess-1","model":"claude-haiku-4-5-20251001"}
{"type":"stream_event","event":{"type":"content_block_start","content_block":{"type":"tool_use","name":"Agent"}}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"Запускаю сабагента."}}}
{"type":"assistant","message":{"usage":{"input_tokens":20,"cache_creation_input_tokens":13236,"cache_read_input_tokens":49844}}}
{"type":"system","subtype":"background_tasks_changed","tasks":[{"task_id":"af33","task_type":"local_agent","description":"Read README"}]}
{"type":"system","subtype":"task_started","task_id":"af33","tool_use_id":"toolu_01NW","description":"Read README","subagent_type":"general-purpose","task_type":"local_agent"}
{"type":"result","subtype":"success","is_error":false,"session_id":"sess-1","total_cost_usd":0.05,"result":"Сабагент ещё работает.","usage":{"input_tokens":20,"cache_creation_input_tokens":13236,"cache_read_input_tokens":49844,"output_tokens":809},"modelUsage":{"claude-haiku-4-5-20251001":{"contextWindow":200000}}}
{"type":"assistant","parent_tool_use_id":"toolu_01NW","subagent_type":"general-purpose","message":{"usage":{"input_tokens":5,"cache_read_input_tokens":900000}}}
{"type":"stream_event","parent_tool_use_id":"toolu_01NW","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"мысли сабагента"}}}
{"type":"system","subtype":"task_progress","task_id":"af33","tool_use_id":"toolu_01NW","description":"Reading README.md","subagent_type":"general-purpose","usage":{"total_tokens":15742,"tool_uses":1,"duration_ms":5296},"last_tool_name":"Read"}
{"type":"user","parent_tool_use_id":"toolu_01NW","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01XR"}]}}
{"type":"system","subtype":"task_updated","task_id":"af33","patch":{"status":"completed","end_time":1786566490307}}
{"type":"system","subtype":"task_notification","task_id":"af33","tool_use_id":"toolu_01NW","status":"completed","usage":{"total_tokens":22559,"tool_uses":1,"duration_ms":37653}}
{"type":"assistant","message":{"usage":{"input_tokens":20,"cache_creation_input_tokens":20259,"cache_read_input_tokens":65712}}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"README на 203 строки."}}}
{"type":"result","subtype":"success","is_error":false,"session_id":"sess-1","total_cost_usd":0.14,"result":"README на 203 строки.","usage":{"input_tokens":20,"cache_creation_input_tokens":20259,"cache_read_input_tokens":65712,"output_tokens":505},"modelUsage":{"claude-haiku-4-5-20251001":{"contextWindow":200000}}}
`

func TestScanStreamReportsSubagentLifecycle(t *testing.T) {
	evs := collect(t, subagentTranscript)

	var statuses []AgentStatus
	for _, ev := range evs {
		if ev.Kind == EvAgents && len(ev.Agents) == 1 {
			statuses = append(statuses, ev.Agents[0].Status)
		}
	}
	if len(statuses) < 2 {
		t.Fatalf("subagent produced %d snapshots, want at least a start and an end", len(statuses))
	}
	if statuses[0] != AgentRunning {
		t.Errorf("first snapshot = %q, want running", statuses[0])
	}
	if last := statuses[len(statuses)-1]; last != AgentDone {
		t.Errorf("final snapshot = %q, want done", last)
	}

	final := lastAgents(evs)
	if len(final) != 1 {
		t.Fatalf("final snapshot has %d agents, want 1", len(final))
	}
	a := final[0]
	if a.ID != "af33" || a.Type != "general-purpose" || a.Task != "Read README" {
		t.Errorf("agent identified as %+v", a)
	}
	if a.Tool != "Read" || a.Tokens != 22559 || a.ToolUses != 1 {
		t.Errorf("agent work recorded as tool=%q tokens=%d tools=%d", a.Tool, a.Tokens, a.ToolUses)
	}
	if a.StartedAt == 0 || a.LastSeen == 0 || a.EndedAt == 0 {
		t.Errorf("agent timings incomplete: %+v", a)
	}
}

// The subagent has its own conversation. None of it may leak into the reply
// being streamed to the user, or into the context-window reading for this thread.
func TestScanStreamKeepsSubagentOutputOut(t *testing.T) {
	var text strings.Builder
	var results []Event
	for _, ev := range collect(t, subagentTranscript) {
		switch ev.Kind {
		case EvText:
			text.WriteString(ev.Text)
		case EvResult:
			results = append(results, ev)
		}
	}
	if got := text.String(); strings.Contains(got, "мысли сабагента") {
		t.Errorf("subagent text leaked into the reply: %q", got)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	// 49844+13236+20 for the first reply, 65712+20259+20 for the second — the
	// subagent's own 900k-token context must not be mistaken for this thread's.
	if results[0].CtxUsed != 63100 {
		t.Errorf("first result context = %d, want 63100", results[0].CtxUsed)
	}
	if results[1].CtxUsed != 85991 {
		t.Errorf("second result context = %d, want 85991", results[1].CtxUsed)
	}
}

// total_cost_usd restates the whole invocation's cost on every result, so events
// must carry the difference or a subagent turn bills several times over.
func TestScanStreamResultCostIsWhatTheEventAdds(t *testing.T) {
	var costs []float64
	for _, ev := range collect(t, subagentTranscript) {
		if ev.Kind == EvResult {
			costs = append(costs, ev.CostUSD)
		}
	}
	if len(costs) != 2 {
		t.Fatalf("got %d results, want 2", len(costs))
	}
	if costs[0] != 0.05 {
		t.Errorf("first result cost = %v, want 0.05", costs[0])
	}
	if diff := costs[1] - 0.09; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("second result cost = %v, want 0.09 (0.14 total less 0.05 already billed)", costs[1])
	}
	if total := costs[0] + costs[1]; total-0.14 > 1e-9 || total-0.14 < -1e-9 {
		t.Errorf("turn billed %v in total, want the CLI's 0.14", total)
	}
}

// A turn whose stream ends while a subagent is still working leaves the user
// with a spinner and no answer unless the cut is reported.
func TestScanStreamAbortsSubagentsLeftRunning(t *testing.T) {
	cut := `{"type":"system","subtype":"task_started","task_id":"af33","tool_use_id":"toolu_01NW","description":"Read README","subagent_type":"general-purpose"}
{"type":"system","subtype":"task_progress","task_id":"af33","tool_use_id":"toolu_01NW","description":"Reading README.md","last_tool_name":"Read","usage":{"total_tokens":100,"tool_uses":1}}
`
	final := lastAgents(collect(t, cut))
	if len(final) != 1 {
		t.Fatalf("final snapshot has %d agents, want 1", len(final))
	}
	if final[0].Status != AgentAborted {
		t.Errorf("status = %q, want aborted", final[0].Status)
	}
	if final[0].EndedAt == 0 {
		t.Error("aborted agent has no end time")
	}
}

// Cancelling a turn kills the CLI, and that is the user's own doing: the
// subagents are reported as cut short and nothing is reported as an error.
func TestSendCancelledTurnReportsNoError(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "claude")
	script := "#!/bin/sh\ncase \"$1\" in --help) echo usage; exit 0;; esac\nsleep 30\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake claude: %v", err)
	}

	e := &Engine{BinPath: bin, Mode: ModeChat}
	ctx, cancel := context.WithCancel(context.Background())
	ch := e.Send(ctx, "привет", "")
	cancel()

	done := make(chan []Event, 1)
	go func() {
		var evs []Event
		for ev := range ch {
			evs = append(evs, ev)
		}
		done <- evs
	}()
	select {
	case evs := <-done:
		for _, ev := range evs {
			if ev.Kind == EvError {
				t.Errorf("cancelled turn reported an error: %v", ev.Err)
			}
		}
	case <-time.After(20 * time.Second):
		t.Fatal("cancelled turn did not close its stream")
	}
}

// A turn without subagents must not gain a single subagent event.
func TestScanStreamSilentWithoutSubagents(t *testing.T) {
	plain := `{"type":"system","subtype":"init","session_id":"s","model":"m"}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"привет"}}}
{"type":"result","subtype":"success","is_error":false,"total_cost_usd":0.01,"result":"привет"}
`
	for _, ev := range collect(t, plain) {
		if ev.Kind == EvAgents {
			t.Fatalf("plain turn emitted a subagent snapshot: %+v", ev.Agents)
		}
	}
}
