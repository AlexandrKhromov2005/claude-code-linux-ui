package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// stripANSI removes styling so assertions read the text, not the escapes.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == 0x1b {
			for i < len(s) && s[i] != 'm' {
				i++
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestAgentsViewEmptyWithoutSubagents(t *testing.T) {
	m := model{width: 100}
	if got := m.agentsView(); got != "" {
		t.Errorf("agentsView = %q, want empty", got)
	}
	if m.agentsH() != 0 {
		t.Errorf("agentsH = %d, want 0 — the row must not be reserved", m.agentsH())
	}
}

func TestAgentsViewReportsLiveness(t *testing.T) {
	now := time.Now()
	ms := func(d time.Duration) int64 { return now.Add(-d).UnixMilli() }

	cases := []struct {
		name   string
		agent  core.AgentState
		want   []string
		absent string
	}{
		{
			name: "working",
			agent: core.AgentState{
				ID: "a1", Type: "general-purpose", Status: core.AgentRunning, Tool: "Read",
				StartedAt: ms(12 * time.Second), LastSeen: ms(2 * time.Second),
			},
			want: []string{"general-purpose", "Read", "12с"},
		},
		{
			name: "quiet",
			agent: core.AgentState{
				ID: "a1", Type: "explore", Status: core.AgentRunning,
				StartedAt: ms(2 * time.Minute), LastSeen: ms(50 * time.Second),
			},
			want: []string{"тишина", "50с"},
		},
		{
			name: "silent",
			agent: core.AgentState{
				ID: "a1", Type: "explore", Status: core.AgentRunning,
				StartedAt: ms(9 * time.Minute), LastSeen: ms(4 * time.Minute),
			},
			want: []string{"нет сигнала", "4м00с"},
		},
		{
			name: "done",
			agent: core.AgentState{
				ID: "a1", Type: "general-purpose", Status: core.AgentDone,
				StartedAt: ms(70 * time.Second), LastSeen: ms(10 * time.Second), EndedAt: ms(10 * time.Second),
			},
			want:   []string{"✓", "1м00с", "готово: 1"},
			absent: "нет сигнала",
		},
		{
			name: "aborted",
			agent: core.AgentState{
				ID: "a1", Type: "general-purpose", Status: core.AgentAborted,
				StartedAt: ms(30 * time.Second), LastSeen: ms(25 * time.Second), EndedAt: ms(1 * time.Second),
			},
			want:   []string{"прерван", "прервано: 1"},
			absent: "готово",
		},
		{
			name: "failed",
			agent: core.AgentState{
				ID: "a1", Type: "general-purpose", Status: core.AgentFailed,
				StartedAt: ms(30 * time.Second), EndedAt: ms(1 * time.Second),
			},
			want: []string{"✗", "ошибка"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := model{width: 200, agents: []core.AgentState{tc.agent}}
			if m.agentsH() != 1 {
				t.Fatalf("agentsH = %d, want 1", m.agentsH())
			}
			got := stripANSI(m.agentsView())
			for _, want := range tc.want {
				if !strings.Contains(got, want) {
					t.Errorf("agentsView = %q, want it to mention %q", got, want)
				}
			}
			if tc.absent != "" && strings.Contains(got, tc.absent) {
				t.Errorf("agentsView = %q, must not mention %q", got, tc.absent)
			}
		})
	}
}

func TestAgentsViewCountsRunning(t *testing.T) {
	now := time.Now().UnixMilli()
	m := model{width: 200, agents: []core.AgentState{
		{ID: "a1", Type: "explore", Status: core.AgentRunning, StartedAt: now, LastSeen: now},
		{ID: "a2", Type: "executor", Status: core.AgentRunning, StartedAt: now, LastSeen: now},
		{ID: "a3", Type: "critic", Status: core.AgentDone, StartedAt: now, EndedAt: now},
	}}
	got := stripANSI(m.agentsView())
	if !strings.Contains(got, "2/3") {
		t.Errorf("agentsView = %q, want a 2/3 running count", got)
	}
	for _, name := range []string{"explore", "executor", "critic"} {
		if !strings.Contains(got, name) {
			t.Errorf("agentsView = %q, want it to list %q", got, name)
		}
	}
}

// The line shares one row with nothing else, so it must never wrap.
func TestAgentsViewFitsTheWidth(t *testing.T) {
	now := time.Now().UnixMilli()
	var many []core.AgentState
	for _, name := range []string{"general-purpose", "explore", "executor", "critic", "verifier"} {
		many = append(many, core.AgentState{
			ID: name, Type: name, Status: core.AgentRunning, Tool: "Read",
			StartedAt: now, LastSeen: now,
		})
	}
	for _, width := range []int{40, 80, 120} {
		m := model{width: width, agents: many}
		line := m.agentsView()
		if strings.Contains(line, "\n") {
			t.Errorf("width %d: line wrapped", width)
		}
		if w := lipgloss.Width(line); w > width {
			t.Errorf("width %d: rendered %d columns wide", width, w)
		}
	}
}

// A turn that reports subagents has to give the row back to the viewport, and
// take it back when the next turn has none.
func TestAgentEventResizesTheViewport(t *testing.T) {
	m := New(newTestApp(t)).(model)
	m.width, m.height, m.ready = 100, 30, true
	m.layout()
	full := m.vp.Height

	updated, _ := m.Update(eventMsg(core.Event{
		Kind:   core.EvAgents,
		Agents: []core.AgentState{{ID: "a1", Type: "explore", Status: core.AgentRunning}},
	}))
	withAgents := updated.(model)
	if withAgents.vp.Height != full-1 {
		t.Errorf("viewport height %d with a subagent line, want %d", withAgents.vp.Height, full-1)
	}
	if len(withAgents.agents) != 1 {
		t.Fatalf("model kept %d agents, want 1", len(withAgents.agents))
	}
}
