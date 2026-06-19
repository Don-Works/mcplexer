package models

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenAISendTextOnly(t *testing.T) {
	body := `{
		"choices":[{"message":{"role":"assistant","content":"hello!"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":11,"completion_tokens":22}
	}`
	var seen struct {
		body map[string]any
		auth string
		ct   string
		url  string
	}
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &seen.body)
		seen.auth = req.Header.Get("Authorization")
		seen.ct = req.Header.Get("content-type")
		seen.url = req.URL.String()
		return jsonResponse(200, body), nil
	})
	a := newOpenAIAdapter("sk-openai", "gpt-4o", "", client)
	resp, err := a.Send(context.Background(), SendRequest{
		System:   "be brief",
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "hello!" {
		t.Errorf("text = %q", resp.Text)
	}
	if resp.InputTokens != 11 || resp.OutputTokens != 22 {
		t.Errorf("tokens = (%d,%d)", resp.InputTokens, resp.OutputTokens)
	}
	if resp.StopReason != StopEndTurn {
		t.Errorf("stop = %q", resp.StopReason)
	}
	if seen.auth != "Bearer sk-openai" {
		t.Errorf("auth header = %q", seen.auth)
	}
	if seen.ct != "application/json" {
		t.Errorf("content-type = %q", seen.ct)
	}
	if seen.url != openAIDefaultEndpoint {
		t.Errorf("url = %q", seen.url)
	}
	// System should be the first message with role=system.
	msgs, _ := seen.body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	first, _ := msgs[0].(map[string]any)
	if first["role"] != "system" || first["content"] != "be brief" {
		t.Errorf("first msg = %+v", first)
	}
}

func TestOpenAIToolCallRoundTrip(t *testing.T) {
	body := `{
		"choices":[{"message":{"role":"assistant","content":"","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"id\":42}"}}
		]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":3,"completion_tokens":4}
	}`
	var captured map[string]any
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &captured)
		return jsonResponse(200, body), nil
	})
	a := newOpenAIAdapter("sk", "gpt-4o", "", client)
	resp, err := a.Send(context.Background(), SendRequest{
		Tools: []ToolSchema{{
			Name:        "lookup",
			Description: "find an item",
			InputSchema: map[string]any{"type": "object"},
		}},
		Messages: []Message{
			{Role: RoleUser, Content: "find 42"},
			{Role: RoleAssistant, Content: "checking", ToolCalls: []ToolCall{
				{ID: "call_prev", Name: "lookup", Input: map[string]any{"id": 1.0}},
			}},
			{Role: RoleTool, ToolUseID: "call_prev", ToolResult: "done"},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.StopReason != StopToolUse {
		t.Errorf("stop = %q", resp.StopReason)
	}
	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].Name != "lookup" {
		t.Fatalf("tool calls = %+v", resp.ToolCalls)
	}
	if got, _ := resp.ToolCalls[0].Input["id"].(float64); got != 42 {
		t.Errorf("input.id = %v", resp.ToolCalls[0].Input["id"])
	}

	// Tools array must use the OpenAI function-call shape.
	tools, _ := captured["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %d", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	if tool["type"] != "function" {
		t.Errorf("tool.type = %v", tool["type"])
	}
	fn, _ := tool["function"].(map[string]any)
	if fn["name"] != "lookup" || fn["description"] != "find an item" {
		t.Errorf("function = %+v", fn)
	}
	if _, ok := fn["parameters"].(map[string]any); !ok {
		t.Errorf("function.parameters missing: %+v", fn)
	}
}

func TestOpenAIHTTPError(t *testing.T) {
	body := `{"error":{"message":"no model","type":"invalid_request"}}`
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(404, body), nil
	})
	a := newOpenAIAdapter("sk", "gpt-x", "", client)
	_, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "no model") {
		t.Errorf("error = %v", err)
	}
}

func TestOpenAIHTTPError5xx(t *testing.T) {
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(503, "down"), nil
	})
	a := newOpenAIAdapter("sk", "gpt-x", "", client)
	_, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenAIEmptyToolArgsDecodes(t *testing.T) {
	body := `{
		"choices":[{"message":{"role":"assistant","content":"","tool_calls":[
			{"id":"c","type":"function","function":{"name":"noop","arguments":""}}
		]},"finish_reason":"tool_calls"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1}
	}`
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(200, body), nil
	})
	a := newOpenAIAdapter("sk", "gpt-4o", "", client)
	resp, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(resp.ToolCalls) != 1 || len(resp.ToolCalls[0].Input) != 0 {
		t.Errorf("tool input = %+v", resp.ToolCalls)
	}
}
