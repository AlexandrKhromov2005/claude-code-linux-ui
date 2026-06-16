package core

import (
	"context"
	"encoding/json"
)

// ApprovalRequest is a gated tool invocation that needs a user decision. It is
// produced by the permission service and routed to the connected client through
// the broker.
type ApprovalRequest struct {
	ToolUseID string          `json:"tool_use_id"`
	ToolName  string          `json:"tool_name"`
	Input     json.RawMessage `json:"input"`
}

// ApprovalDecision is the answer a client returns for a request.
type ApprovalDecision struct {
	Allow        bool            `json:"allow"`
	UpdatedInput json.RawMessage `json:"updated_input,omitempty"`
	Message      string          `json:"message,omitempty"`
	// RememberRule, when non-empty on an allow, is stored as a project allow-rule
	// so the same pattern is not asked again.
	RememberRule string `json:"remember_rule,omitempty"`
}

// ApprovalBroker is implemented by each client (TUI, web) so the core can ask it
// for a decision. The call blocks until the user answers.
type ApprovalBroker interface {
	RequestApproval(ctx context.Context, req ApprovalRequest) ApprovalDecision
}

// PermissionService is the transport that Claude calls to request approval. The
// concrete implementation (an MCP server over HTTP) lives outside the core; the
// core only needs its connection details to wire up agent-mode invocations.
type PermissionService interface {
	Addr() string
	PromptTool() string
	MCPConfigJSON() string
}
