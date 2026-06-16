// Package permctl runs the in-process MCP permission server that Claude calls
// over loopback HTTP. It is transport: it forwards each gated tool call to a
// decider (the core App) and returns the allow/deny verdict to Claude.
package permctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/AlexandrKhromov2005/claude-code-linux-ui/internal/core"
)

const (
	serverName = "permctl"
	toolName   = "approve"
	// mcpProtocolVersion is the fallback when the client does not announce one.
	mcpProtocolVersion = "2025-06-18"
)

// Decider resolves an approval request. The core App's HandleApproval satisfies
// this signature.
type Decider func(core.ApprovalRequest) core.ApprovalDecision

// Server is an MCP server (streamable HTTP, loopback only) exposing a single
// `approve` tool. It satisfies core.PermissionService.
type Server struct {
	decide Decider

	ln  net.Listener
	srv *http.Server
	mu  sync.Mutex
}

// New creates a server with the given decision function.
func New(decide Decider) *Server { return &Server{decide: decide} }

// Start binds an ephemeral loopback port and serves until Stop.
func (p *Server) Start() error {
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
func (p *Server) Stop() {
	if p.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = p.srv.Shutdown(ctx)
}

// Addr returns the bound address (host:port), empty before Start.
func (p *Server) Addr() string {
	if p.ln == nil {
		return ""
	}
	return p.ln.Addr().String()
}

// PromptTool returns the mcp__server__tool name for --permission-prompt-tool.
func (p *Server) PromptTool() string { return "mcp__" + serverName + "__" + toolName }

// MCPConfigJSON returns the inline --mcp-config value pointing at this server.
func (p *Server) MCPConfigJSON() string {
	cfg := map[string]any{
		"mcpServers": map[string]any{
			serverName: map[string]any{
				"type": "http",
				"url":  fmt.Sprintf("http://%s/mcp", p.Addr()),
			},
		},
	}
	b, _ := json.Marshal(cfg)
	return string(b)
}

func (p *Server) ask(req core.ApprovalRequest) core.ApprovalDecision {
	p.mu.Lock()
	d := p.decide
	p.mu.Unlock()
	if d == nil {
		return core.ApprovalDecision{Allow: false, Message: "no approver configured"}
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

func (p *Server) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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
		w.Header().Set("Mcp-Session-Id", serverName)
		writeRPCResult(w, req.ID, map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": serverName, "version": "1"},
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

func (p *Server) handleToolsCall(w http.ResponseWriter, req rpcReq) {
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

	decision := p.ask(core.ApprovalRequest{
		ToolUseID: args.ToolUseID,
		ToolName:  args.ToolName,
		Input:     args.Input,
	})

	writeRPCResult(w, req.ID, map[string]any{
		"content": []any{map[string]any{"type": "text", "text": resultJSON(decision, args.Input)}},
	})
}

// resultJSON renders the allow/deny payload Claude expects as tool output.
func resultJSON(d core.ApprovalDecision, input json.RawMessage) string {
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
		"name":        toolName,
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
