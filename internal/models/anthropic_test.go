package models

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAnthropicSendTextOnly(t *testing.T) {
	body := `{
		"content": [{"type": "text", "text": "hello there"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 12, "output_tokens": 34}
	}`
	var seen struct {
		body    map[string]any
		headers map[string]string
	}
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &seen.body)
		seen.headers = map[string]string{
			"x-api-key":         req.Header.Get("x-api-key"),
			"anthropic-version": req.Header.Get("anthropic-version"),
			"content-type":      req.Header.Get("content-type"),
		}
		return jsonResponse(200, body), nil
	})

	a := newAnthropicAdapter("sk-test", "claude-opus-4-7", "", client)
	resp, err := a.Send(context.Background(), SendRequest{
		System:   "you are helpful",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "hello there" {
		t.Errorf("text = %q, want %q", resp.Text, "hello there")
	}
	if resp.InputTokens != 12 || resp.OutputTokens != 34 {
		t.Errorf("tokens = (%d,%d), want (12,34)", resp.InputTokens, resp.OutputTokens)
	}
	if resp.StopReason != StopEndTurn {
		t.Errorf("stop = %q, want %q", resp.StopReason, StopEndTurn)
	}
	if got := seen.headers["x-api-key"]; got != "sk-test" {
		t.Errorf("x-api-key = %q", got)
	}
	if got := seen.headers["anthropic-version"]; got != anthropicAPIVersion {
		t.Errorf("anthropic-version = %q", got)
	}
	if got := seen.headers["content-type"]; got != "application/json" {
		t.Errorf("content-type = %q", got)
	}
}

func TestAnthropicSendToolCall(t *testing.T) {
	body := `{
		"content": [
			{"type": "text", "text": "let me check"},
			{"type": "tool_use", "id": "toolu_1", "name": "lookup", "input": {"id": 42}}
		],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 5, "output_tokens": 7, "cache_read_input_tokens": 100, "cache_creation_input_tokens": 20}
	}`
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(200, body), nil
	})
	a := newAnthropicAdapter("sk", "claude-opus-4-7", "", client)
	resp, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "look it up"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(resp.ToolCalls))
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "toolu_1" || tc.Name != "lookup" {
		t.Errorf("tool = %+v", tc)
	}
	if got, _ := tc.Input["id"].(float64); got != 42 {
		t.Errorf("input.id = %v", tc.Input["id"])
	}
	// Cache tokens should be added into InputTokens.
	if resp.InputTokens != 125 {
		t.Errorf("input tokens = %d, want 125", resp.InputTokens)
	}
	if resp.OutputTokens != 7 {
		t.Errorf("output tokens = %d, want 7", resp.OutputTokens)
	}
}

func TestAnthropicHTTPError(t *testing.T) {
	body := `{"type":"error","error":{"type":"invalid_request_error","message":"bad model"}}`
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(400, body), nil
	})
	a := newAnthropicAdapter("sk", "claude-x", "", client)
	_, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "400") || !strings.Contains(err.Error(), "bad model") {
		t.Errorf("error = %v", err)
	}
}

func TestAnthropicHTTPError5xx(t *testing.T) {
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(502, "upstream gone"), nil
	})
	a := newAnthropicAdapter("sk", "claude-x", "", client)
	_, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Fatalf("err = %v, want 502", err)
	}
}

func TestAnthropicCacheControlOnSystemAndTools(t *testing.T) {
	var captured map[string]any
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &captured)
		return jsonResponse(200,
			`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		), nil
	})
	a := newAnthropicAdapter("sk", "claude-opus-4-7", "", client)
	_, err := a.Send(context.Background(), SendRequest{
		System: "sys prompt",
		Tools: []ToolSchema{
			{Name: "a", InputSchema: map[string]any{"type": "object"}},
			{Name: "b", InputSchema: map[string]any{"type": "object"}},
		},
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	// System block should have cache_control.
	sys, _ := captured["system"].([]any)
	if len(sys) != 1 {
		t.Fatalf("system blocks = %d", len(sys))
	}
	sysObj, _ := sys[0].(map[string]any)
	if _, ok := sysObj["cache_control"]; !ok {
		t.Errorf("system block missing cache_control: %+v", sysObj)
	}
	// Last tool should carry cache_control; earlier tools should not.
	tools, _ := captured["tools"].([]any)
	if len(tools) != 2 {
		t.Fatalf("tools = %d", len(tools))
	}
	first, _ := tools[0].(map[string]any)
	last, _ := tools[1].(map[string]any)
	if _, ok := first["cache_control"]; ok {
		t.Errorf("first tool unexpectedly has cache_control")
	}
	if _, ok := last["cache_control"]; !ok {
		t.Errorf("last tool missing cache_control: %+v", last)
	}
}

func TestAnthropicEncodesToolResultAndAssistantTurns(t *testing.T) {
	var captured map[string]any
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &captured)
		return jsonResponse(200,
			`{"content":[{"type":"text","text":"done"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		), nil
	})
	a := newAnthropicAdapter("sk", "claude-opus-4-7", "", client)
	_, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{
			{Role: RoleUser, Content: "look it up"},
			{Role: RoleAssistant, Content: "checking", ToolCalls: []ToolCall{
				{ID: "tu_1", Name: "lookup", Input: map[string]any{"id": 1.0}},
			}},
			{Role: RoleTool, ToolUseID: "tu_1", ToolResult: "value=42"},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	msgs, _ := captured["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %d, want 3", len(msgs))
	}
	asst, _ := msgs[1].(map[string]any)
	if asst["role"] != "assistant" {
		t.Errorf("msg[1].role = %v", asst["role"])
	}
	asstContent, _ := asst["content"].([]any)
	if len(asstContent) != 2 {
		t.Errorf("assistant content blocks = %d, want 2", len(asstContent))
	}
	toolUser, _ := msgs[2].(map[string]any)
	if toolUser["role"] != "user" {
		t.Errorf("msg[2].role = %v, want user", toolUser["role"])
	}
}

func TestAnthropicDefaultMaxTokensApplied(t *testing.T) {
	var captured map[string]any
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &captured)
		return jsonResponse(200,
			`{"content":[],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`,
		), nil
	})
	a := newAnthropicAdapter("sk", "claude-opus-4-7", "", client)
	_, _ = a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	mt, _ := captured["max_tokens"].(float64)
	if int(mt) != anthropicDefaultMaxTokens {
		t.Errorf("max_tokens = %v, want %d", mt, anthropicDefaultMaxTokens)
	}
}
