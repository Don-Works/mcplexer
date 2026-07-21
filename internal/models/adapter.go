// Package models provides a provider-agnostic abstraction over outbound
// LLM API calls used by the Workers feature.
//
// Each concrete adapter (Anthropic, OpenAI, OpenAI-compatible) implements
// ModelAdapter by translating to/from the provider's wire format. The
// caller is responsible for fetching API keys from AuthScope before
// constructing an adapter; this package never touches secret storage.
package models

import "context"

// Role identifies the speaker for a single conversation turn.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// Stop reasons normalized across providers.
const (
	StopEndTurn      = "end_turn"
	StopToolUse      = "tool_use"
	StopMaxTokens    = "max_tokens"
	StopStopSequence = "stop_sequence"
	StopOther        = "other"
)

// Message is a single conversation turn. The fields used depend on Role:
//
//   - RoleSystem / RoleUser:     Content
//   - RoleAssistant:             Content and/or ToolCalls
//   - RoleTool:                  ToolUseID + ToolResult (Content unused)
type Message struct {
	Role       Role
	Content    string
	ToolCalls  []ToolCall
	ToolUseID  string
	ToolResult string
}

// ToolSchema describes a tool the model may choose to call.
type ToolSchema struct {
	Name        string
	Description string
	// InputSchema is a JSON-Schema object describing the tool's input.
	InputSchema map[string]any
}

// ToolCall is a model-issued invocation of a tool.
type ToolCall struct {
	// ID is the provider-issued correlation id. Echo it back on the
	// matching RoleTool turn via ToolUseID.
	ID    string
	Name  string
	Input map[string]any
}

// SendRequest is one round-trip of input to the model.
type SendRequest struct {
	System    string
	Messages  []Message
	Tools     []ToolSchema
	MaxTokens int
	Stop      []string
	// WorkspacePath is the filesystem root of the worker's bound
	// workspace (workspaces.root_path). Subprocess-style adapters
	// (claude_cli, opencode_cli) set it as cmd.Dir so the model's own
	// MCP client connections back into mcplexer get bound to the right
	// workspace via stdio CWD inference. Empty means "no workspace
	// scoping" — the subprocess inherits the daemon's CWD and lands in
	// the Global workspace.
	WorkspacePath string
	// Stream is an optional callback for receiving streaming events.
	// When non-nil, adapters that support streaming call it for each
	// text delta as the subprocess emits output.
	Stream func(SendStreamEvent)
}

// SendResponse is the assistant's reply for a single round-trip.
type SendResponse struct {
	// Text is the assistant's text content. May be empty if the model
	// only returned tool calls.
	Text         string
	ToolCalls    []ToolCall
	InputTokens  int
	OutputTokens int
	// CostUSD is the adapter-reported authoritative cost for this call.
	// Adapters that can't compute it leave this at 0 — the caller then
	// falls back to EstimateCostUSD against the pricing table. Used by
	// providers that orchestrate their own billing (e.g. claude_cli,
	// where the CLI emits total_cost_usd in its result envelope and the
	// pricing model is opaque from outside).
	CostUSD float64
	// StopReason is one of the Stop* constants above.
	StopReason string
}

// SendStreamEventKind identifies the type of streaming event.
type SendStreamEventKind string

const (
	SendStreamTextDelta SendStreamEventKind = "text_delta"
)

// SendStreamEvent is emitted by adapters that support streaming.
type SendStreamEvent struct {
	Kind    SendStreamEventKind
	Text    string
	Source  string
	RawType string
}

// ModelAdapter is the common interface across LLM providers.
type ModelAdapter interface {
	Send(ctx context.Context, req SendRequest) (*SendResponse, error)
}
