package models

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestOpenAICompatSendUsesProvidedEndpoint(t *testing.T) {
	body := `{
		"choices":[{"message":{"role":"assistant","content":"yo"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":2,"completion_tokens":3}
	}`
	var seenURL string
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		seenURL = req.URL.String()
		return jsonResponse(200, body), nil
	})
	a := newOpenAICompatAdapter("sk", "deepseek-chat",
		"https://api.deepseek.com", client)
	resp, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.Text != "yo" {
		t.Errorf("text = %q", resp.Text)
	}
	want := "https://api.deepseek.com/v1/chat/completions"
	if seenURL != want {
		t.Errorf("url = %q, want %q", seenURL, want)
	}
}

func TestOpenAICompatToolsStructured(t *testing.T) {
	body := `{
		"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1}
	}`
	var captured map[string]any
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &captured)
		return jsonResponse(200, body), nil
	})
	a := newOpenAICompatAdapter("sk", "deepseek-chat",
		"https://api.deepseek.com/v1", client)
	_, err := a.Send(context.Background(), SendRequest{
		Tools: []ToolSchema{
			{Name: "search", Description: "search docs", InputSchema: map[string]any{"type": "object"}},
		},
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	tools, _ := captured["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %d", len(tools))
	}
	tool, _ := tools[0].(map[string]any)
	fn, _ := tool["function"].(map[string]any)
	if tool["type"] != "function" || fn["name"] != "search" {
		t.Errorf("tool = %+v", tool)
	}
}

func TestOpenAICompatOmitsAuthWhenKeyEmpty(t *testing.T) {
	body := `{
		"choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],
		"usage":{"prompt_tokens":1,"completion_tokens":1}
	}`
	var sawAuth bool
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		if req.Header.Get("Authorization") != "" {
			sawAuth = true
		}
		return jsonResponse(200, body), nil
	})
	a := newOpenAICompatAdapter("", "llama3",
		"http://localhost:11434", client)
	_, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if sawAuth {
		t.Error("Authorization header should be omitted when APIKey is empty")
	}
}

func TestOpenAICompatHTTPError(t *testing.T) {
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		return jsonResponse(500, `{"error":{"message":"boom"}}`), nil
	})
	a := newOpenAICompatAdapter("sk", "m", "https://example.com/v1", client)
	_, err := a.Send(context.Background(), SendRequest{
		Messages: []Message{{Role: RoleUser, Content: "hi"}},
	})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v", err)
	}
}

func TestResolveCompatEndpoint(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://api.deepseek.com", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.deepseek.com/", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.deepseek.com/v1", "https://api.deepseek.com/v1/chat/completions"},
		{"https://api.deepseek.com/v1/", "https://api.deepseek.com/v1/chat/completions"},
		{"https://example.com/v1/chat/completions", "https://example.com/v1/chat/completions"},
		{"http://localhost:11434", "http://localhost:11434/v1/chat/completions"},
	}
	for _, c := range cases {
		got := resolveCompatEndpoint(c.in)
		if got != c.want {
			t.Errorf("resolveCompatEndpoint(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
