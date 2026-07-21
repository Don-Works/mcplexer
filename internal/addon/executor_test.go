package addon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/netguard"
)

// mockAuthHeaders returns a static Authorization header.
func mockAuthHeaders(_ context.Context, _ string) (http.Header, error) {
	h := http.Header{}
	h.Set("Authorization", "Bearer test-token-123")
	return h, nil
}

func TestExecutor_Execute(t *testing.T) {
	allowExecutorPrivateFetch(t)
	tests := []struct {
		name          string
		tool          ResolvedTool
		args          string
		serverHandler http.HandlerFunc
		wantErr       string
		wantIsError   bool
		wantContains  string
		checkRequest  func(t *testing.T, r *http.Request, body []byte)
	}{
		{
			name: "GET with URL params",
			tool: ResolvedTool{
				ToolDef: ToolDef{
					Method: "GET",
					URL:    "{{base}}/chat/{{channel_id}}/messages",
				},
				FullName: "test__get_messages",
			},
			args: `{"channel_id": "ch_123"}`,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`{"messages": []}`))
			},
			wantContains: `"messages"`,
			checkRequest: func(t *testing.T, r *http.Request, _ []byte) {
				if r.Method != "GET" {
					t.Errorf("method = %q, want GET", r.Method)
				}
				if !strings.HasSuffix(r.URL.Path, "/chat/ch_123/messages") {
					t.Errorf("path = %q, want ending /chat/ch_123/messages", r.URL.Path)
				}
			},
		},
		{
			name: "POST with body from remaining args",
			tool: ResolvedTool{
				ToolDef: ToolDef{
					Method: "POST",
					URL:    "{{base}}/chat/{{channel_id}}/message",
				},
				FullName: "test__send_message",
			},
			args: `{"channel_id": "ch_456", "content": "hello", "type": "text"}`,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id": "msg_1"}`))
			},
			wantContains: `"id"`,
			checkRequest: func(t *testing.T, r *http.Request, body []byte) {
				if r.Method != "POST" {
					t.Errorf("method = %q, want POST", r.Method)
				}
				if r.Header.Get("Content-Type") != "application/json" {
					t.Errorf("content-type = %q, want application/json", r.Header.Get("Content-Type"))
				}
				// Body should contain remaining args (content, type) but NOT channel_id.
				var bodyMap map[string]any
				if err := json.Unmarshal(body, &bodyMap); err != nil {
					t.Fatalf("unmarshal body: %v", err)
				}
				if _, ok := bodyMap["channel_id"]; ok {
					t.Error("body should not contain channel_id (consumed by URL)")
				}
				if bodyMap["content"] != "hello" {
					t.Errorf("body content = %v, want hello", bodyMap["content"])
				}
				if bodyMap["type"] != "text" {
					t.Errorf("body type = %v, want text", bodyMap["type"])
				}
			},
		},
		{
			name: "query param substitution",
			tool: ResolvedTool{
				ToolDef: ToolDef{
					Method: "GET",
					URL:    "{{base}}/items",
					QueryParams: map[string]string{
						"status": "{{status}}",
						"limit":  "{{limit}}",
					},
				},
				FullName: "test__list_items",
			},
			args: `{"status": "active"}`,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`[]`))
			},
			wantContains: "[]",
			checkRequest: func(t *testing.T, r *http.Request, _ []byte) {
				if r.URL.Query().Get("status") != "active" {
					t.Errorf("query status = %q, want active", r.URL.Query().Get("status"))
				}
				// limit was not provided, should be skipped.
				if r.URL.Query().Has("limit") {
					t.Error("query should not contain limit (not provided)")
				}
			},
		},
		{
			name: "auth header injection",
			tool: ResolvedTool{
				ToolDef: ToolDef{
					Method: "GET",
					URL:    "{{base}}/secure",
				},
				FullName: "test__secure",
			},
			args: `{}`,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`ok`))
			},
			wantContains: "ok",
			checkRequest: func(t *testing.T, r *http.Request, _ []byte) {
				if r.Header.Get("Authorization") != "Bearer test-token-123" {
					t.Errorf("auth = %q, want Bearer test-token-123", r.Header.Get("Authorization"))
				}
			},
		},
		{
			name: "static headers from tool def",
			tool: ResolvedTool{
				ToolDef: ToolDef{
					Method: "GET",
					URL:    "{{base}}/custom",
					Headers: map[string]string{
						"X-Custom": "my-value",
					},
				},
				FullName: "test__custom_header",
			},
			args: `{}`,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`ok`))
			},
			wantContains: "ok",
			checkRequest: func(t *testing.T, r *http.Request, _ []byte) {
				if r.Header.Get("X-Custom") != "my-value" {
					t.Errorf("X-Custom = %q, want my-value", r.Header.Get("X-Custom"))
				}
			},
		},
		{
			name: "4xx error response",
			tool: ResolvedTool{
				ToolDef: ToolDef{
					Method: "GET",
					URL:    "{{base}}/not-found",
				},
				FullName: "test__not_found",
			},
			args: `{}`,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"error": "not found"}`))
			},
			wantIsError:  true,
			wantContains: "HTTP 404",
		},
		{
			name: "5xx error response",
			tool: ResolvedTool{
				ToolDef: ToolDef{
					Method: "POST",
					URL:    "{{base}}/fail",
				},
				FullName: "test__server_error",
			},
			args: `{}`,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`internal server error`))
			},
			wantIsError:  true,
			wantContains: "HTTP 500",
		},
		{
			name: "body_mapping none skips body",
			tool: ResolvedTool{
				ToolDef: ToolDef{
					Method:      "POST",
					URL:         "{{base}}/no-body",
					BodyMapping: "none",
				},
				FullName: "test__no_body",
			},
			args: `{"extra_field": "ignored"}`,
			serverHandler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(`ok`))
			},
			wantContains: "ok",
			checkRequest: func(t *testing.T, r *http.Request, body []byte) {
				if len(body) > 0 {
					t.Errorf("expected empty body, got %q", body)
				}
			},
		},
		{
			name: "missing URL param returns error",
			tool: ResolvedTool{
				ToolDef: ToolDef{
					Method: "GET",
					URL:    "{{base}}/items/{{item_id}}",
				},
				FullName: "test__missing_param",
			},
			args:    `{}`,
			wantErr: "missing required url param",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lastReq *http.Request
			var lastBody []byte

			var handler http.HandlerFunc
			if tt.serverHandler != nil {
				handler = func(w http.ResponseWriter, r *http.Request) {
					lastReq = r.Clone(r.Context())
					lastBody, _ = io.ReadAll(r.Body)
					_ = r.Body.Close()
					tt.serverHandler(w, r)
				}
			}

			srv := httptest.NewServer(handler)
			defer srv.Close()

			// Inject the test server base URL into the args.
			args := strings.ReplaceAll(tt.args, "{}", "{}")
			tool := tt.tool
			tool.URL = strings.ReplaceAll(tool.URL, "{{base}}", srv.URL)

			executor := NewExecutor(mockAuthHeaders)

			result, err := executor.Execute(
				context.Background(),
				&tool,
				"test-scope",
				json.RawMessage(args),
			)

			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %q, want containing %q", err, tt.wantErr)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			var ctr callToolResult
			if err := json.Unmarshal(result, &ctr); err != nil {
				t.Fatalf("unmarshal result: %v", err)
			}

			if ctr.IsError != tt.wantIsError {
				t.Errorf("IsError = %v, want %v", ctr.IsError, tt.wantIsError)
			}

			if tt.wantContains != "" {
				if len(ctr.Content) == 0 {
					t.Fatal("expected content, got none")
				}
				if !strings.Contains(ctr.Content[0].Text, tt.wantContains) {
					t.Errorf("content = %q, want containing %q", ctr.Content[0].Text, tt.wantContains)
				}
			}

			if tt.checkRequest != nil && lastReq != nil {
				tt.checkRequest(t, lastReq, lastBody)
			}
		})
	}
}

func TestExecutor_ResponseTruncation(t *testing.T) {
	allowExecutorPrivateFetch(t)
	// Create a response larger than 200KB.
	bigBody := strings.Repeat("x", 250*1024)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(bigBody))
	}))
	defer srv.Close()

	tool := &ResolvedTool{
		ToolDef: ToolDef{
			Method: "GET",
			URL:    srv.URL + "/big",
		},
		FullName: "test__big_response",
	}

	executor := NewExecutor(mockAuthHeaders)
	result, err := executor.Execute(
		context.Background(),
		tool,
		"test-scope",
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	var ctr callToolResult
	if err := json.Unmarshal(result, &ctr); err != nil {
		t.Fatal(err)
	}

	if len(ctr.Content) == 0 {
		t.Fatal("expected content")
	}

	text := ctr.Content[0].Text
	if !strings.Contains(text, "[truncated at 200KB]") {
		t.Error("expected truncation notice in response")
	}

	// Should be roughly 200KB + truncation notice, not the full 250KB.
	if len(text) > 210*1024 {
		t.Errorf("truncated response too large: %d bytes", len(text))
	}
}

func TestExecutor_AuthError(t *testing.T) {
	failAuth := func(_ context.Context, _ string) (http.Header, error) {
		return nil, fmt.Errorf("oauth token expired")
	}

	tool := &ResolvedTool{
		ToolDef: ToolDef{
			Method: "GET",
			URL:    "https://example.com/test",
		},
		FullName: "test__auth_fail",
	}

	executor := NewExecutor(failAuth)
	_, err := executor.Execute(
		context.Background(),
		tool,
		"test-scope",
		json.RawMessage(`{}`),
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "oauth token expired") {
		t.Errorf("error = %q, want containing 'oauth token expired'", err)
	}
}

func TestExecutor_RejectsPrivateHostByDefault(t *testing.T) {
	t.Setenv(netguard.AllowPrivateFetchEnv, "")
	tool := &ResolvedTool{
		ToolDef:  ToolDef{Method: "GET", URL: "http://127.0.0.1:65535/leak"},
		FullName: "test__private",
	}
	executor := NewExecutor(nil)
	_, err := executor.Execute(context.Background(), tool, "", json.RawMessage(`{}`))
	if err == nil || !strings.Contains(err.Error(), "addon request") {
		t.Fatalf("expected addon request netguard error, got %v", err)
	}
}

func TestExecutor_RequestAuthSeesFinalRequestAndBody(t *testing.T) {
	allowExecutorPrivateFetch(t)
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.Copy(io.Discard, r.Body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	var signedBody string
	requestAuth := func(_ context.Context, scopeID string, req *http.Request, body []byte) error {
		if scopeID != "hawk-scope" {
			t.Fatalf("scopeID = %q, want hawk-scope", scopeID)
		}
		if req.Method != http.MethodPost {
			t.Fatalf("method = %q, want POST", req.Method)
		}
		if req.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("Content-Type = %q, want application/json", req.Header.Get("Content-Type"))
		}
		signedBody = string(body)
		req.Header.Set("Authorization", "Hawk signed")
		return nil
	}

	tool := &ResolvedTool{
		ToolDef:  ToolDef{Method: "POST", URL: srv.URL + "/things/{{id}}"},
		FullName: "test__create_thing",
	}
	executor := NewExecutorWithRequestAuth(mockAuthHeaders, requestAuth)
	_, err := executor.Execute(
		context.Background(),
		tool,
		"hawk-scope",
		json.RawMessage(`{"id":"abc","name":"thing"}`),
	)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotAuth != "Hawk signed" {
		t.Fatalf("Authorization = %q, want Hawk signed", gotAuth)
	}
	if signedBody != `{"name":"thing"}` {
		t.Fatalf("signed body = %q, want compact JSON body", signedBody)
	}
}

func TestExecutor_PUTWithBody(t *testing.T) {
	allowExecutorPrivateFetch(t)
	var receivedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		_ = r.Body.Close()
		if r.Method != "PUT" {
			t.Errorf("method = %q, want PUT", r.Method)
		}
		_, _ = w.Write([]byte(`{"updated": true}`))
	}))
	defer srv.Close()

	tool := &ResolvedTool{
		ToolDef: ToolDef{
			Method: "PUT",
			URL:    srv.URL + "/items/{{item_id}}",
		},
		FullName: "test__update_item",
	}

	executor := NewExecutor(mockAuthHeaders)
	result, err := executor.Execute(
		context.Background(),
		tool,
		"test-scope",
		json.RawMessage(`{"item_id": "42", "name": "updated", "status": "done"}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Verify body contains remaining args (name, status) but not item_id.
	var bodyMap map[string]any
	if err := json.Unmarshal(receivedBody, &bodyMap); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if _, ok := bodyMap["item_id"]; ok {
		t.Error("body should not contain item_id (consumed by URL)")
	}
	if bodyMap["name"] != "updated" {
		t.Errorf("body name = %v, want updated", bodyMap["name"])
	}

	var ctr callToolResult
	if err := json.Unmarshal(result, &ctr); err != nil {
		t.Fatal(err)
	}
	if ctr.IsError {
		t.Error("expected IsError = false")
	}
}

func TestSubstituteURL_PathEscaping(t *testing.T) {
	consumed := map[string]bool{}
	got, err := substituteURL("https://api.example.com/users/{{id}}", map[string]any{
		"id": "alice/bob",
	}, consumed)
	if err != nil {
		t.Fatalf("substituteURL: %v", err)
	}
	want := "https://api.example.com/users/alice%2Fbob"
	if got != want {
		t.Errorf("got %q, want %q (slash should be percent-encoded)", got, want)
	}
}

func TestSubstituteURL_RejectsTraversal(t *testing.T) {
	cases := []map[string]any{
		{"id": "../../admin"},
		{"id": "..%2Fadmin"},
		{"id": "user\nset-cookie: x"},
		{"id": "user\rinjection"},
	}
	for i, args := range cases {
		_, err := substituteURL("https://api.example.com/users/{{id}}", args, map[string]bool{})
		if err == nil {
			t.Errorf("case %d (%v): expected rejection, got nil", i, args)
		}
	}
}

// TestExecutor_IntegerPathParam guards the regression where a large integer
// path param (e.g. an id of 55215543) was decoded as float64 and formatted by
// "%v" as scientific notation ("5.5215543e+07"), corrupting the URL into a
// 404. UseNumber decoding must preserve the exact integer text.
func TestExecutor_IntegerPathParam(t *testing.T) {
	allowExecutorPrivateFetch(t)
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tool := ResolvedTool{
		ToolDef:  ToolDef{Method: "GET", URL: srv.URL + "/timeslips/{{id}}"},
		FullName: "test__get_timeslip",
	}
	executor := NewExecutor(mockAuthHeaders)
	_, err := executor.Execute(
		context.Background(), &tool, "test-scope",
		json.RawMessage(`{"id": 55215543}`),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotPath != "/timeslips/55215543" {
		t.Errorf("path = %q, want /timeslips/55215543 (large int must not become scientific notation)", gotPath)
	}
}

// TestExecutor_IntegerBodyParam confirms integer body params stay integers
// (not float64-stringified) once args are decoded with UseNumber.
func TestExecutor_IntegerBodyParam(t *testing.T) {
	allowExecutorPrivateFetch(t)
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tool := ResolvedTool{
		ToolDef:  ToolDef{Method: "POST", URL: srv.URL + "/things"},
		FullName: "test__create_thing",
	}
	executor := NewExecutor(mockAuthHeaders)
	_, err := executor.Execute(
		context.Background(), &tool, "test-scope",
		json.RawMessage(`{"count": 55215543}`),
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(gotBody), `"count":55215543`) {
		t.Errorf("body = %q, want integer count preserved as 55215543", gotBody)
	}
}

func TestAppendQueryParams_EscapesValues(t *testing.T) {
	got := appendQueryParams(
		"https://api.example.com/search",
		map[string]string{"q": "{{q}}"},
		map[string]any{"q": "hello world&foo=bar"},
		map[string]bool{},
	)
	if !strings.Contains(got, "q=hello+world%26foo%3Dbar") &&
		!strings.Contains(got, "q=hello%20world%26foo%3Dbar") {
		t.Errorf("query value not properly escaped: %q", got)
	}
}

func allowExecutorPrivateFetch(t *testing.T) {
	t.Helper()
	t.Setenv(netguard.AllowPrivateFetchEnv, "1")
}
