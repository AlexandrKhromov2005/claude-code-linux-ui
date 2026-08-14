package web

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// This file carries background jobs across the transport: their state out to the
// browser, and — when one finishes — a turn back into the conversation that
// started it.

// BroadcastJobs pushes the current job list to the connected client. It is the
// supervisor's change hook, so it fires while jobs run as well as when they end;
// that steady trickle is what makes a job visibly alive rather than merely
// listed.
func (s *Server) BroadcastJobs() {
	mgr := s.app.Jobs()
	if mgr == nil {
		return
	}
	s.mu.Lock()
	c := s.activeConn
	s.mu.Unlock()
	if c == nil {
		return
	}
	_ = c.writeJSON(map[string]any{"type": "jobs", "jobs": mgr.List(), "now": time.Now().UnixMilli()})
}

// wakeTurnTimeout bounds a turn started by a finished job. Nobody is watching a
// wake-up, so there is no one to notice a turn that has gone off the rails and
// no one to press cancel; without a bound, one stuck turn spends tokens until the
// server is restarted. Long enough for genuine follow-up work, short enough that
// a runaway is capped.
const wakeTurnTimeout = 10 * time.Minute

// wakeGuard serialises wake-up turns per thread. Two jobs finishing at once in
// the same conversation must not start two concurrent turns in it: they would
// both resume the same session and race to append to the same transcript.
type wakeGuard struct {
	mu     sync.Mutex
	active map[string]bool
}

func (g *wakeGuard) enter(threadID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.active == nil {
		g.active = map[string]bool{}
	}
	if g.active[threadID] {
		return false
	}
	g.active[threadID] = true
	return true
}

func (g *wakeGuard) leave(threadID string) {
	g.mu.Lock()
	delete(g.active, threadID)
	g.mu.Unlock()
}

// DispatchTurn runs a turn nobody asked for interactively — a finished job
// reporting back — and streams it to the connected client exactly like a turn
// the user typed. It satisfies core.TurnDispatcher.
//
// It runs on its own context rather than a connection's, because the point is
// that this survives whatever the user is doing: the browser may be closed, and
// the answer still belongs in the transcript.
func (s *Server) DispatchTurn(threadID, text string) {
	if threadID == "" {
		return
	}
	if !s.wake.enter(threadID) {
		return // a wake-up is already running in this thread
	}
	go func() {
		defer s.wake.leave(threadID)
		// A wake-up runs with nobody watching it, which is exactly when a turn that
		// goes off the rails is most expensive: there is no one to notice or press
		// cancel. The bound is generous enough for real follow-up work and short
		// enough that a stuck turn cannot burn tokens indefinitely.
		ctx, cancel := context.WithTimeout(context.Background(), wakeTurnTimeout)
		defer cancel()
		ch, err := s.app.SendTurnInThread(ctx, threadID, text)
		if err != nil {
			s.mu.Lock()
			c := s.activeConn
			s.mu.Unlock()
			if c != nil {
				_ = c.writeJSON(map[string]any{"type": "error", "message": err.Error(), "threadId": threadID})
			}
			return
		}
		s.streamTurn(threadID, ch)
	}()
}

// streamTurn relays a turn's events to whichever client is connected at the
// time. The connection is looked up per event rather than captured once, so a
// turn started while nobody was watching still reaches a browser that connects
// midway through it.
func (s *Server) streamTurn(threadID string, ch <-chan core.Event) {
	for ev := range ch {
		m := eventToMsg(ev)
		m["threadId"] = threadID
		if ev.Kind == core.EvResult {
			m["cost"] = s.app.Cost()
			used, win := s.app.ContextInfo()
			m["ctxUsed"] = used
			m["ctxWindow"] = win
			m["modelActual"] = s.app.ModelActual()
			m["usage"] = s.app.Usage()
			m["turnUsage"] = ev.Usage
		}
		s.mu.Lock()
		c := s.activeConn
		s.mu.Unlock()
		if c != nil {
			_ = c.writeJSON(m)
		}
	}
	s.mu.Lock()
	c := s.activeConn
	s.mu.Unlock()
	if c != nil {
		_ = c.writeJSON(map[string]any{"type": "turn_end", "threadId": threadID})
	}
}

// ---- REST -----------------------------------------------------------------

func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	mgr := s.app.Jobs()
	if mgr == nil {
		writeJSON(w, http.StatusOK, []core.JobView{})
		return
	}
	writeJSON(w, http.StatusOK, mgr.List())
}

func (s *Server) handleJobStop(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &body); err != nil || body.ID == "" {
		badRequest(w, "id required")
		return
	}
	mgr := s.app.Jobs()
	if mgr == nil {
		badRequest(w, "менеджер задач не запущен")
		return
	}
	if err := mgr.Stop(body.ID); err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleJobLogs(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if id == "" {
		badRequest(w, "id required")
		return
	}
	mgr := s.app.Jobs()
	if mgr == nil {
		badRequest(w, "менеджер задач не запущен")
		return
	}
	out, err := mgr.Logs(id, 400)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"logs": out})
}
