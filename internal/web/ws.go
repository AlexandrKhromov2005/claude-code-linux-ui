package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/gorilla/websocket"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

// handleWS authenticates and upgrades a WebSocket connection. The token is read
// from Sec-WebSocket-Protocol (never the URL), and Origin/Host are validated by
// the upgrader's CheckOrigin and the explicit check below.
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if reason := s.allow.checkHostOrigin(r); reason != "" {
		http.Error(w, reason, http.StatusForbidden)
		return
	}
	if !s.validWSToken(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ws, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return // upgrader already wrote the error
	}
	c := &wsConn{ws: ws, s: s, pending: map[string]chan core.ApprovalDecision{}}
	s.setActiveConn(c)
	defer func() {
		s.clearActiveConn(c)
		c.cancelTurn()
		c.failPending()
		ws.Close()
	}()
	_ = c.writeJSON(map[string]any{"type": "state", "state": s.state()})
	c.readLoop()
}

// validWSToken reports whether the offered subprotocols include the session
// token alongside the negotiated subprotocol name.
func (s *Server) validWSToken(r *http.Request) bool {
	for _, p := range websocket.Subprotocols(r) {
		if p != wsSubprotocol && tokenEqual(p, s.token) {
			return true
		}
	}
	return false
}

// RequestApproval routes a gated tool call to the connected web client. It
// satisfies core.ApprovalBroker.
func (s *Server) RequestApproval(ctx context.Context, req core.ApprovalRequest) core.ApprovalDecision {
	s.mu.Lock()
	c := s.activeConn
	s.mu.Unlock()
	if c == nil {
		return core.ApprovalDecision{Allow: false, Message: "нет подключённого веб-клиента"}
	}
	return c.requestApproval(ctx, req)
}

// wsConn is one connected web client.
type wsConn struct {
	ws *websocket.Conn
	s  *Server

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]chan core.ApprovalDecision
	cancel  context.CancelFunc
}

type inMsg struct {
	Type         string   `json:"type"`
	Text         string   `json:"text"`
	Attachments  []string `json:"attachments"`
	ID           string   `json:"id"`
	Allow        bool     `json:"allow"`
	RememberRule string   `json:"rememberRule"`
}

func (c *wsConn) writeJSON(v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.WriteJSON(v)
}

func (c *wsConn) readLoop() {
	for {
		var m inMsg
		if err := c.ws.ReadJSON(&m); err != nil {
			return
		}
		switch m.Type {
		case "send":
			c.startTurn(m.Text, m.Attachments)
		case "cancel":
			c.cancelTurn()
			c.failPending()
		case "approval":
			dec := core.ApprovalDecision{Allow: m.Allow, RememberRule: m.RememberRule}
			if !m.Allow {
				dec.Message = "Отклонено пользователем"
			}
			c.deliver(m.ID, dec)
		}
	}
}

func (c *wsConn) startTurn(text string, attachments []string) {
	c.cancelTurn()
	ctx, cancel := context.WithCancel(context.Background())
	c.setCancel(cancel)

	ch, err := c.s.app.SendTurn(ctx, text, attachments)
	if err != nil {
		cancel()
		_ = c.writeJSON(map[string]any{"type": "error", "message": err.Error()})
		return
	}
	go func() {
		for ev := range ch {
			m := eventToMsg(ev)
			// The result event carries this turn's cost; the client shows the
			// running session total, so report the accumulated value instead.
			if ev.Kind == core.EvResult {
				m["cost"] = c.s.app.Cost()
			}
			_ = c.writeJSON(m)
		}
		_ = c.writeJSON(map[string]any{"type": "turn_end"})
		cancel()
	}()
}

func (c *wsConn) setCancel(cancel context.CancelFunc) {
	c.mu.Lock()
	c.cancel = cancel
	c.mu.Unlock()
}

func (c *wsConn) cancelTurn() {
	c.mu.Lock()
	cancel := c.cancel
	c.cancel = nil
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// requestApproval sends an approval request and blocks until the client answers,
// the turn is cancelled, or the connection drops.
func (c *wsConn) requestApproval(ctx context.Context, req core.ApprovalRequest) core.ApprovalDecision {
	id := randID()
	ch := make(chan core.ApprovalDecision, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	msg := map[string]any{
		"type":     "approval_request",
		"id":       id,
		"toolName": req.ToolName,
		"input":    json.RawMessage(req.Input),
		"preview":  core.BuildToolPreview(req.ToolName, req.Input),
	}
	if err := c.writeJSON(msg); err != nil {
		c.discard(id)
		return core.ApprovalDecision{Allow: false, Message: "ошибка отправки запроса"}
	}
	select {
	case dec := <-ch:
		return dec
	case <-ctx.Done():
		c.discard(id)
		return core.ApprovalDecision{Allow: false, Message: "отменено"}
	}
}

func (c *wsConn) deliver(id string, dec core.ApprovalDecision) {
	c.mu.Lock()
	ch := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if ch != nil {
		ch <- dec
	}
}

func (c *wsConn) discard(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// failPending denies every outstanding approval so blocked permission handlers
// unblock when the turn is cancelled or the client disconnects.
func (c *wsConn) failPending() {
	c.mu.Lock()
	pending := c.pending
	c.pending = map[string]chan core.ApprovalDecision{}
	c.mu.Unlock()
	for _, ch := range pending {
		ch <- core.ApprovalDecision{Allow: false, Message: "соединение закрыто"}
	}
}

// eventToMsg maps a core event to the WebSocket wire shape.
func eventToMsg(ev core.Event) map[string]any {
	m := map[string]any{"type": "event", "kind": eventKind(ev.Kind)}
	if ev.Text != "" {
		m["text"] = ev.Text
	}
	if ev.Tool != "" {
		m["tool"] = ev.Tool
	}
	if ev.Model != "" {
		m["model"] = ev.Model
	}
	if ev.SessionID != "" {
		m["sessionId"] = ev.SessionID
	}
	if ev.CostUSD != 0 {
		m["cost"] = ev.CostUSD
	}
	if ev.Attempt != 0 {
		m["attempt"] = ev.Attempt
	}
	if ev.Err != nil {
		m["error"] = ev.Err.Error()
	}
	return m
}

func eventKind(k core.EventKind) string {
	switch k {
	case core.EvText:
		return "text"
	case core.EvToolStart:
		return "tool_start"
	case core.EvSystemInit:
		return "system_init"
	case core.EvResult:
		return "result"
	case core.EvError:
		return "error"
	case core.EvRetry:
		return "retry"
	case core.EvNotice:
		return "notice"
	default:
		return "unknown"
	}
}

func randID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "id"
	}
	return hex.EncodeToString(b)
}
