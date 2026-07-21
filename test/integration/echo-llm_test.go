// echo-llm_test.go — coverage for the consolidate-mode response path
// added for scenario_memory_consolidator (D2) + D7.4 grading. The
// default ("ok") path stays trivial enough to not need its own test;
// any drift there breaks every consumer immediately.
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postChat is a small helper that drives one POST against handleChat
// and decodes the response. Returns the assistant content for
// assertion.
func postChat(t *testing.T, url string, body chatRequest, headers map[string]string) string {
	t.Helper()
	buf, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out chatCompletionResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(out.Choices) == 0 {
		t.Fatalf("no choices in response")
	}
	return out.Choices[0].Message.Content
}

// newEchoServer spins up the same mux echo-llm's main() builds so the
// path-suffix routing exercises the production wiring (not a hand-
// rolled test mux that could drift).
func newEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", handleChat)
	mux.HandleFunc("/v1/chat/completions/consolidate", handleChat)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestEchoLLM_DefaultModeReturnsOK verifies the default path stays
// "ok" — the existing harness depends on this constant for the cheap
// liveness check workers do at run start.
func TestEchoLLM_DefaultModeReturnsOK(t *testing.T) {
	srv := newEchoServer(t)
	got := postChat(t, srv.URL+"/v1/chat/completions", chatRequest{
		Model:    "echo",
		Messages: []chatMessage{{Role: "user", Content: "anything"}},
	}, nil)
	if got != "ok" {
		t.Errorf("default mode content = %q, want \"ok\"", got)
	}
}

// TestEchoLLM_ConsolidateModeViaPath verifies the path-suffix
// activation: a POST to /v1/chat/completions/consolidate produces the
// deterministic merged digest.
func TestEchoLLM_ConsolidateModeViaPath(t *testing.T) {
	srv := newEchoServer(t)
	got := postChat(t, srv.URL+"/v1/chat/completions/consolidate", chatRequest{
		Model: "echo",
		Messages: []chatMessage{
			{Role: "system", Content: "ignored — system not user"},
			{Role: "user", Content: "note A"},
			{Role: "user", Content: "note B"},
			{Role: "user", Content: "note C"},
		},
	}, nil)
	want := "MERGED: note A | note B | note C"
	if got != want {
		t.Errorf("consolidate digest = %q, want %q", got, want)
	}
}

// TestEchoLLM_ConsolidateModeViaHeader verifies the header activation
// path: the default URL with X-Mcplexer-Echo-Mode: consolidate also
// produces the merged digest. Equivalent to the path-suffix path.
func TestEchoLLM_ConsolidateModeViaHeader(t *testing.T) {
	srv := newEchoServer(t)
	got := postChat(t, srv.URL+"/v1/chat/completions", chatRequest{
		Model: "echo",
		Messages: []chatMessage{
			{Role: "user", Content: "alpha"},
			{Role: "user", Content: "beta"},
		},
	}, map[string]string{"X-Mcplexer-Echo-Mode": "consolidate"})
	want := "MERGED: alpha | beta"
	if got != want {
		t.Errorf("header-activated digest = %q, want %q", got, want)
	}
}

// TestEchoLLM_ConsolidateModeDeterminism verifies repeated calls
// produce identical output for identical input — the harness's
// cached-fixture grading relies on this contract.
func TestEchoLLM_ConsolidateModeDeterminism(t *testing.T) {
	srv := newEchoServer(t)
	req := chatRequest{
		Model: "echo",
		Messages: []chatMessage{
			{Role: "user", Content: "x"},
			{Role: "user", Content: "y"},
		},
	}
	a := postChat(t, srv.URL+"/v1/chat/completions/consolidate", req, nil)
	b := postChat(t, srv.URL+"/v1/chat/completions/consolidate", req, nil)
	if a != b {
		t.Errorf("non-deterministic: %q != %q", a, b)
	}
	if !strings.HasPrefix(a, "MERGED: ") {
		t.Errorf("digest missing MERGED: prefix: %q", a)
	}
}

// TestEchoLLM_ConsolidateEmptyInput verifies the no-user-message edge
// case: emit canonical "MERGED: " body so the adapter still sees a
// valid completion.
func TestEchoLLM_ConsolidateEmptyInput(t *testing.T) {
	srv := newEchoServer(t)
	got := postChat(t, srv.URL+"/v1/chat/completions/consolidate", chatRequest{
		Model: "echo",
		// Only system messages — no user content.
		Messages: []chatMessage{{Role: "system", Content: "noop"}},
	}, nil)
	want := "MERGED: "
	if got != want {
		t.Errorf("empty-input digest = %q, want %q", got, want)
	}
}

// TestEchoLLM_ConsolidateDigestUnit isolates consolidateDigest from
// the HTTP surface so the grading-cache contract is locked in at the
// unit level too. If any future refactor moves the formatter, this
// test pins the wire shape.
func TestEchoLLM_ConsolidateDigestUnit(t *testing.T) {
	tests := []struct {
		name string
		msgs []chatMessage
		want string
	}{
		{
			name: "single user msg",
			msgs: []chatMessage{{Role: "user", Content: "solo"}},
			want: "MERGED: solo",
		},
		{
			name: "trims whitespace",
			msgs: []chatMessage{
				{Role: "user", Content: "  padded  "},
				{Role: "user", Content: "next"},
			},
			want: "MERGED: padded | next",
		},
		{
			name: "skips empty",
			msgs: []chatMessage{
				{Role: "user", Content: ""},
				{Role: "user", Content: "kept"},
			},
			want: "MERGED: kept",
		},
		{
			name: "all-empty → canonical empty",
			msgs: []chatMessage{{Role: "user", Content: " "}},
			want: "MERGED: ",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := consolidateDigest(tc.msgs)
			if got != tc.want {
				t.Errorf("digest = %q, want %q", got, tc.want)
			}
		})
	}
}
