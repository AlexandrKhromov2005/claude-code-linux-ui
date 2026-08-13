package web

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// The web client renders subagents straight from this message, so the field
// names are a contract with web/src/lib/AgentPanel.svelte.
func TestAgentsEventWireShape(t *testing.T) {
	started := time.Now().Add(-30 * time.Second).UnixMilli()
	seen := time.Now().Add(-3 * time.Second).UnixMilli()

	m := eventToMsg(core.Event{Kind: core.EvAgents, Agents: []core.AgentState{{
		ID: "af33", Type: "general-purpose", Task: "Read README", Activity: "Reading README.md",
		Tool: "Read", Status: core.AgentRunning, Tokens: 15742, ToolUses: 1, Runs: 1,
		StartedAt: started, LastSeen: seen,
	}}})

	if m["kind"] != "agents" {
		t.Fatalf("kind = %v, want agents", m["kind"])
	}

	blob, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var wire struct {
		Kind   string `json:"kind"`
		Now    int64  `json:"now"`
		Agents []struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Task      string `json:"task"`
			Activity  string `json:"activity"`
			Tool      string `json:"tool"`
			Status    string `json:"status"`
			Tokens    int    `json:"tokens"`
			ToolUses  int    `json:"toolUses"`
			Runs      int    `json:"runs"`
			StartedAt int64  `json:"startedAt"`
			LastSeen  int64  `json:"lastSeen"`
			EndedAt   int64  `json:"endedAt"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(blob, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(wire.Agents) != 1 {
		t.Fatalf("got %d agents on the wire, want 1", len(wire.Agents))
	}
	a := wire.Agents[0]
	if a.ID != "af33" || a.Type != "general-purpose" || a.Task != "Read README" {
		t.Errorf("identity lost in transit: %+v", a)
	}
	if a.Activity != "Reading README.md" || a.Tool != "Read" || a.Status != "running" {
		t.Errorf("activity lost in transit: %+v", a)
	}
	if a.Tokens != 15742 || a.ToolUses != 1 || a.Runs != 1 {
		t.Errorf("counters lost in transit: %+v", a)
	}
	if a.StartedAt != started || a.LastSeen != seen || a.EndedAt != 0 {
		t.Errorf("timings lost in transit: %+v", a)
	}

	// Ages are measured against the server's clock, so it has to travel too: a
	// browser on the far end of an SSH tunnel has a clock of its own.
	if wire.Now == 0 {
		t.Error("no server clock sent with the subagent update")
	}
	if skew := wire.Now - time.Now().UnixMilli(); skew > 5000 || skew < -5000 {
		t.Errorf("server clock stamped %d ms away from now", skew)
	}
}

// Events that are not about subagents must not grow an agents field.
func TestNonAgentEventsCarryNoAgents(t *testing.T) {
	for _, ev := range []core.Event{
		{Kind: core.EvText, Text: "привет"},
		{Kind: core.EvToolStart, Tool: "Read"},
		{Kind: core.EvResult, CostUSD: 0.01},
	} {
		m := eventToMsg(ev)
		if _, ok := m["agents"]; ok {
			t.Errorf("%v event carries an agents field", m["kind"])
		}
		if _, ok := m["now"]; ok {
			t.Errorf("%v event carries a clock stamp", m["kind"])
		}
	}
}
