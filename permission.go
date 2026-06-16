package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	permServerName = "permctl"
	permToolName   = "approve"
	// mcpProtocolVersion is the fallback when the client does not announce one.
	mcpProtocolVersion = "2025-06-18"
)

// permPromptTool is the mcp__<server>__<tool> name passed to --permission-prompt-tool.
func permPromptTool() string { return "mcp__" + permServerName + "__" + permToolName }

// ApprovalDecision is the outcome the modal returns for one tool request.
type ApprovalDecision struct {
	Allow        bool
	UpdatedInput json.RawMessage // echoed back to Claude on allow ("" = original input)
	Message      string          // reason shown to Claude on deny
}

// ApprovalRequest carries the tool details from the permission tool to the UI
// and back via Reply.
type ApprovalRequest struct {
	ToolUseID string
	ToolName  string
	Input     json.RawMessage
	Reply     chan ApprovalDecision
}

// approvalReqMsg delivers an ApprovalRequest into the Bubble Tea update loop.
type approvalReqMsg struct{ req *ApprovalRequest }

// Decider resolves an approval request. The UI variant blocks until the user
// answers; tests inject a synchronous function.
type Decider func(ApprovalRequest) ApprovalDecision

// PermissionServer is an in-process MCP server (streamable HTTP, loopback only)
// exposing a single `approve` tool. Claude is pointed at it through
// --permission-prompt-tool / --mcp-config, so every gated tool call is routed
// back to the TUI that owns the modal.
type PermissionServer struct {
	decide Decider

	ln  net.Listener
	srv *http.Server
	mu  sync.Mutex
}

// NewPermissionServer creates a server with the given decision function.
func NewPermissionServer(decide Decider) *PermissionServer {
	return &PermissionServer{decide: decide}
}

// SetUIDecider routes approvals through the Bubble Tea program: it injects an
// approvalReqMsg and blocks the handler goroutine until the model replies.
func (p *PermissionServer) SetUIDecider(send func(tea.Msg)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.decide = func(req ApprovalRequest) ApprovalDecision {
		req.Reply = make(chan ApprovalDecision, 1)
		send(approvalReqMsg{&req})
		return <-req.Reply
	}
}

// Start binds an ephemeral loopback port and serves until Stop.
func (p *PermissionServer) Start() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	p.ln = ln
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", p.handle)
	p.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = p.srv.Serve(ln) }()
	return nil
}

// Stop shuts the server down.
func (p *PermissionServer) Stop() {
	if p.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = p.srv.Shutdown(ctx)
}

// Addr returns the bound address (host:port), empty before Start.
func (p *PermissionServer) Addr() string {
	if p.ln == nil {
		return ""
	}
	return p.ln.Addr().String()
}

// MCPConfigJSON returns the inline --mcp-config value pointing at this server.
func (p *PermissionServer) MCPConfigJSON() string {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			permServerName: map[string]any{
				"type": "http",
				"url":  fmt.Sprintf("http://%s/mcp", p.Addr()),
			},
		},
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

func (p *PermissionServer) ask(req ApprovalRequest) ApprovalDecision {
	p.mu.Lock()
	d := p.decide
	p.mu.Unlock()
	if d == nil {
		return ApprovalDecision{Allow: false, Message: "no approver configured"}
	}
	return d(req)
}

// ---- JSON-RPC over streamable HTTP ----------------------------------------

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func (p *PermissionServer) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// No server-initiated SSE stream is offered.
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req rpcReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeRPCError(w, nil, -32700, "parse error")
		return
	}
	notification := len(req.ID) == 0 || string(req.ID) == "null"

	switch req.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &params)
		pv := params.ProtocolVersion
		if pv == "" {
			pv = mcpProtocolVersion
		}
		w.Header().Set("Mcp-Session-Id", permServerName)
		writeRPCResult(w, req.ID, map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": permServerName, "version": "1"},
		})
	case "notifications/initialized", "notifications/cancelled":
		w.WriteHeader(http.StatusAccepted)
	case "ping":
		writeRPCResult(w, req.ID, map[string]any{})
	case "tools/list":
		writeRPCResult(w, req.ID, map[string]any{"tools": []any{approveToolDef()}})
	case "tools/call":
		p.handleToolsCall(w, req)
	default:
		if notification {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		writeRPCError(w, req.ID, -32601, "method not found: "+req.Method)
	}
}

func (p *PermissionServer) handleToolsCall(w http.ResponseWriter, req rpcReq) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	_ = json.Unmarshal(req.Params, &params)

	var args struct {
		ToolName  string          `json:"tool_name"`
		Input     json.RawMessage `json:"input"`
		ToolUseID string          `json:"tool_use_id"`
	}
	_ = json.Unmarshal(params.Arguments, &args)

	decision := p.ask(ApprovalRequest{
		ToolUseID: args.ToolUseID,
		ToolName:  args.ToolName,
		Input:     args.Input,
	})

	writeRPCResult(w, req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": permResultJSON(decision, args.Input)}},
	})
}

// permResultJSON renders the allow/deny payload Claude expects as tool output.
func permResultJSON(d ApprovalDecision, input json.RawMessage) string {
	if d.Allow {
		ui := d.UpdatedInput
		if len(ui) == 0 {
			ui = input
		}
		if len(ui) == 0 {
			ui = json.RawMessage("{}")
		}
		b, _ := json.Marshal(map[string]json.RawMessage{
			"behavior":     json.RawMessage(`"allow"`),
			"updatedInput": ui,
		})
		return string(b)
	}
	msg := d.Message
	if msg == "" {
		msg = "Denied by user"
	}
	b, _ := json.Marshal(map[string]any{"behavior": "deny", "message": msg})
	return string(b)
}

func approveToolDef() map[string]any {
	return map[string]any{
		"name":        permToolName,
		"description": "Approve or deny a tool invocation requested by Claude.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tool_name":   map[string]any{"type": "string"},
				"input":       map[string]any{"type": "object"},
				"tool_use_id": map[string]any{"type": "string"},
			},
			"required": []string{"tool_name", "input"},
		},
	}
}

func writeRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": msg},
	})
}
