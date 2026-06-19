package hammerspoon

import (
	"context"
	"encoding/json"
	"fmt"
)

// MCPServer wraps a Hammerspoon Manager and exposes it as an in-process MCP
// downstream (downstream.InternalBackend-shaped). The gateway discovers +
// routes to it via the standard DownstreamServer machinery.
//
// The wrapper is resilient to m == nil and to a manager backed by nullBridge:
// every call returns a clean MCP error envelope rather than panicking. This
// matches telegram's "tools advertise but don't dispatch" posture for
// integrations that aren't fully configured yet.
type MCPServer struct {
	m *Manager
}

// NewMCPServer constructs the wrapper. Caller registers it with the
// downstream.Manager against the "hammerspoon" server id.
func NewMCPServer(m *Manager) *MCPServer { return &MCPServer{m: m} }

// ListTools returns the Hammerspoon tool surface. Tool names are returned
// without the "hammerspoon__" prefix; mcplexer adds the namespace via the
// DownstreamServer.ToolNamespace field.
func (s *MCPServer) ListTools(_ context.Context) (json.RawMessage, error) {
	tools := alwaysOnTools()
	if s.m != nil && s.m.AllowExecLua() {
		tools = append(tools, execLuaTool())
	}
	out, err := json.Marshal(map[string]any{"tools": tools})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Call dispatches a tool call. Returns a CallToolResult-shaped JSON blob.
func (s *MCPServer) Call(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, error) {
	if s.m == nil {
		return errorResult("Hammerspoon downstream not enabled. Enable it in the mcplexer dashboard."), nil
	}
	switch toolName {
	case "list_windows":
		return s.callListWindows(ctx, args)
	case "focus_app":
		return s.callFocusApp(ctx, args)
	case "screenshot":
		return s.callScreenshot(ctx, args)
	case "send_keys":
		return s.callSendKeys(ctx, args)
	case "notify":
		return s.callNotify(ctx, args)
	case "exec_lua":
		if !s.m.AllowExecLua() {
			return errorResult("exec_lua is disabled. Set HAMMERSPOON_ALLOW_EXEC_LUA=true to enable."), nil
		}
		return s.callExecLua(ctx, args)
	default:
		return errorResult(fmt.Sprintf("unknown hammerspoon tool %q", toolName)), nil
	}
}

// textResult wraps a text string into MCP's CallToolResult shape.
func textResult(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
	})
	return b
}

// errorResult is like textResult but with isError=true.
func errorResult(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": true,
	})
	return b
}
