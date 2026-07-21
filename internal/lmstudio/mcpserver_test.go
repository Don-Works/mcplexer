package lmstudio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type callResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func decodeResult(t *testing.T, raw json.RawMessage) callResult {
	t.Helper()
	var r callResult
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatalf("decode call result: %v (%s)", err, raw)
	}
	return r
}

func TestListToolsAdvertisesSurface(t *testing.T) {
	t.Parallel()
	s := NewMCPServer(&Manager{})
	raw, err := s.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	var body struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("tools list is not valid JSON: %v", err)
	}
	want := []string{
		"status", "start_server", "stop_server", "list_models",
		"load_model", "unload_model", "download_model",
	}
	if len(body.Tools) != len(want) {
		t.Fatalf("tool count = %d, want %d", len(body.Tools), len(want))
	}
	for i, w := range want {
		if body.Tools[i].Name != w {
			t.Errorf("tool[%d] = %q, want %q", i, body.Tools[i].Name, w)
		}
	}
}

func TestCallUnknownTool(t *testing.T) {
	t.Parallel()
	s := NewMCPServer(&Manager{enabled: true})
	raw, _ := s.Call(context.Background(), "evil_tool", json.RawMessage(`{}`))
	r := decodeResult(t, raw)
	if !r.IsError || !strings.Contains(r.Content[0].Text, "unknown lmstudio tool") {
		t.Errorf("unknown tool: got %+v", r)
	}
}

func TestCallLoadModelRequiresModel(t *testing.T) {
	t.Parallel()
	s := NewMCPServer(&Manager{enabled: true, lmsPath: "/nonexistent/lms"})
	raw, _ := s.Call(context.Background(), "load_model", json.RawMessage(`{}`))
	r := decodeResult(t, raw)
	if !r.IsError || !strings.Contains(r.Content[0].Text, "model is required") {
		t.Errorf("load_model without model: got %+v", r)
	}
}

func TestCallStatusReportsServerAndDelegationHint(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"qwen/qwen3-8b"}]}`))
	}))
	defer srv.Close()

	s := NewMCPServer(&Manager{
		enabled: true, lmsPath: "/nonexistent/lms",
		endpoint: srv.URL, client: srv.Client(),
	})
	raw, _ := s.Call(context.Background(), "status", json.RawMessage(`{}`))
	r := decodeResult(t, raw)
	if r.IsError {
		t.Fatalf("status: unexpected isError: %+v", r)
	}
	text := r.Content[0].Text
	for _, want := range []string{
		"NOT AVAILABLE", // lms binary deliberately unresolvable
		"UP",            // server probe via httptest
		"qwen/qwen3-8b", // loaded model surfaced
		"openai_compat", // delegation hint present
		srv.URL + "/v1", // delegation endpoint hint
	} {
		if !strings.Contains(text, want) {
			t.Errorf("status text missing %q:\n%s", want, text)
		}
	}
}

func TestMCPServerDisabledReturnsToolError(t *testing.T) {
	s := NewMCPServer(&Manager{enabled: false, endpoint: defaultEndpoint})
	raw, err := s.Call(context.Background(), "status", nil)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	var out struct {
		IsError bool `json:"isError"`
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.IsError {
		t.Fatalf("isError = false, want true")
	}
	if len(out.Content) != 1 || !strings.Contains(out.Content[0].Text, AllowEnvVar) {
		t.Fatalf("unexpected error content: %+v", out.Content)
	}
}

func TestModelIDPatternRejectsShellSyntax(t *testing.T) {
	valid := []string{"qwen/qwen3-8b", "llama-3.2-1b-instruct@q4_k_m"}
	for _, model := range valid {
		if !modelIDPattern.MatchString(model) {
			t.Fatalf("modelIDPattern rejected %q", model)
		}
	}

	invalid := []string{"", "../model", "model;rm -rf /", "model $(whoami)", "|cat"}
	for _, model := range invalid {
		if modelIDPattern.MatchString(model) {
			t.Fatalf("modelIDPattern accepted %q", model)
		}
	}
}
