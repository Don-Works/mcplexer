package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// MCPServer wraps a Telegram Manager and exposes it as an in-process MCP
// downstream server (downstream.InternalBackend-shaped). The gateway
// discovers + routes to it via the standard DownstreamServer machinery, so
// per-workspace enablement is controlled by route rules like any other
// integration.
type MCPServer struct {
	m *Manager
}

// NewMCPServer constructs the wrapper. Caller registers it with the
// downstream.Manager against the "telegram" server id.
func NewMCPServer(m *Manager) *MCPServer { return &MCPServer{m: m} }

// ListTools returns the Telegram tool surface. Shape mirrors a tools/list
// response: {"tools":[{name, description, inputSchema, ...}]}.
func (s *MCPServer) ListTools(_ context.Context) (json.RawMessage, error) {
	if s.m == nil {
		return json.RawMessage(`{"tools":[]}`), nil
	}
	return json.RawMessage(toolsListJSON), nil
}

// Call dispatches a tool call. Returns a CallToolResult-shaped JSON blob.
func (s *MCPServer) Call(ctx context.Context, toolName string, args json.RawMessage) (json.RawMessage, error) {
	if s.m == nil {
		return errorResult("telegram manager not configured"), nil
	}
	switch toolName {
	case "send_message":
		return s.callSendMessage(ctx, args)
	case "broadcast":
		return s.callBroadcast(ctx, args)
	case "list_chats":
		return s.callListChats(ctx)
	case "request_pairing":
		return s.callRequestPairing(ctx, args)
	default:
		return errorResult(fmt.Sprintf("unknown telegram tool %q", toolName)), nil
	}
}

func (s *MCPServer) callSendMessage(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		ChatID   string `json:"chat_id"`
		Text     string `json:"text"`
		Priority string `json:"priority"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return errorResult(err.Error()), nil
	}
	if req.ChatID == "" || req.Text == "" {
		return errorResult("chat_id and text are required"), nil
	}
	if err := s.m.SendByChatID(ctx, req.ChatID, req.Text, req.Priority); err != nil {
		return errorResult("send failed: " + err.Error()), nil
	}
	return textResult(fmt.Sprintf("Sent message to chat %s.", req.ChatID)), nil
}

func (s *MCPServer) callBroadcast(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		WorkspaceID string `json:"workspace_id"`
		Text        string `json:"text"`
		Priority    string `json:"priority"`
	}
	if err := json.Unmarshal(args, &req); err != nil {
		return errorResult(err.Error()), nil
	}
	if req.Text == "" {
		return errorResult("text is required"), nil
	}
	if req.WorkspaceID == "" {
		return errorResult("workspace_id is required (the agent's current workspace id)"), nil
	}
	n, err := s.m.BroadcastWorkspace(ctx, req.WorkspaceID, req.Text, req.Priority)
	if err != nil {
		return errorResult("broadcast failed: " + err.Error()), nil
	}
	return textResult(fmt.Sprintf("Broadcast delivered to %d chat(s).", n)), nil
}

func (s *MCPServer) callListChats(ctx context.Context) (json.RawMessage, error) {
	chats, err := s.m.store.ListTelegramChats(ctx)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	if len(chats) == 0 {
		return textResult("No chats paired. Call request_pairing to generate a code, then send /start <code> to the bot in Telegram."), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d chat(s) paired:\n", len(chats))
	for _, c := range chats {
		status := "active"
		if !c.Active {
			status = "inactive"
		}
		fmt.Fprintf(&b, "- id=%s title=%q type=%s workspace=%s status=%s min_priority=%s last_seen=%s\n",
			c.ID, c.Title, c.ChatType, c.WorkspaceID, status, c.MinPriority,
			c.LastSeenAt.Format("2006-01-02 15:04:05"))
	}
	return textResult(b.String()), nil
}

func (s *MCPServer) callRequestPairing(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var req struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &req)
	}
	if req.WorkspaceID == "" {
		return errorResult("workspace_id is required — pass the id of the workspace the chat should be paired to"), nil
	}
	if !s.m.HasClient() {
		return errorResult("telegram bot token is not configured (set it under Credentials for the telegram server)"), nil
	}
	p, err := s.m.CreatePairing(ctx, "telegram", req.WorkspaceID, "", 0)
	if err != nil {
		return errorResult(err.Error()), nil
	}
	body := fmt.Sprintf("Pairing code: %s\nExpires: %s\n\nThe user sends this in Telegram:\n  /start %s",
		p.Code, p.ExpiresAt.Format("2006-01-02 15:04:05"), p.Code)
	return textResult(body), nil
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

// toolsListJSON is the static tool schema returned to tools/list. Tool names
// are relative (no "telegram__" prefix); MCPlexer adds the namespace based on
// the DownstreamServer.ToolNamespace field.
const toolsListJSON = `{
  "tools": [
    {
      "name": "send_message",
      "description": "Send a message to a specific Telegram chat bound to this MCPlexer workspace. chat_id is the internal MCPlexer id from list_chats, not the raw Telegram id.",
      "inputSchema": {
        "type": "object",
        "properties": {
          "chat_id": {"type": "string"},
          "text": {"type": "string"},
          "priority": {"type": "string", "enum": ["critical", "high", "normal", "low"]}
        },
        "required": ["chat_id", "text"]
      }
    },
    {
      "name": "broadcast",
      "description": "Send a message to every active Telegram chat bound to the given workspace. Use sparingly.",
      "inputSchema": {
        "type": "object",
        "properties": {
          "workspace_id": {"type": "string"},
          "text": {"type": "string"},
          "priority": {"type": "string", "enum": ["critical", "high", "normal", "low"]}
        },
        "required": ["workspace_id", "text"]
      }
    },
    {
      "name": "list_chats",
      "description": "List Telegram chats currently paired to MCPlexer (across all workspaces). Returns id, title, chat type, workspace, min_priority, and last-seen timestamp.",
      "inputSchema": {"type": "object", "properties": {}}
    },
    {
      "name": "request_pairing",
      "description": "Generate a pairing code so a user can bind their Telegram chat to the given workspace. They send /start <code> to the bot. Codes expire after 10 minutes.",
      "inputSchema": {
        "type": "object",
        "properties": {"workspace_id": {"type": "string"}},
        "required": ["workspace_id"]
      }
    }
  ]
}`
