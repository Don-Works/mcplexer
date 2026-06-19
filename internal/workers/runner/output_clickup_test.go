package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

// fakeSecretsForChannel is a minimal SecretReader for the HTTP channel
// tests. The runner-level fakeSecrets lives in runner_test.go and uses
// package runner_test, so we keep a duplicate here in package runner.
type fakeSecretsForChannel struct {
	value []byte
	err   error
}

func (f *fakeSecretsForChannel) Get(_ context.Context, _, _ string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.value, nil
}

func TestEmitClickUpTaskOutput_Shape(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		captured = req
		capturedBody, _ = io.ReadAll(req.Body)
		return statusResponse(200, `{"id":"abc"}`), nil
	})
	octx := sampleOutputCtx(client)
	octx.secrets = &fakeSecretsForChannel{value: []byte("pk_456")}
	ch := outputChannel{
		Type:          "clickup_task",
		ListID:        "9001",
		SecretScopeID: "scope-clickup",
		NamePrefix:    "[worker]",
	}
	if err := emitClickUpTaskOutput(context.Background(), octx, ch); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if !strings.HasSuffix(captured.URL.Path, "/list/9001/task") {
		t.Fatalf("URL path = %q", captured.URL.Path)
	}
	if got := captured.Header.Get("Authorization"); got != "pk_456" {
		t.Fatalf("Authorization = %q", got)
	}
	var got clickupCreateTaskRequest
	if err := json.Unmarshal(capturedBody, &got); err != nil {
		t.Fatalf("unmarshal: %v / %s", err, string(capturedBody))
	}
	if !strings.HasPrefix(got.Name, "[worker] smoke-test") {
		t.Fatalf("Name = %q", got.Name)
	}
	if !strings.Contains(got.MarkdownContent, "All clear") {
		t.Fatalf("MarkdownContent missing output: %q", got.MarkdownContent)
	}
	if !strings.Contains(got.MarkdownContent, "Cost: $0.0042") {
		t.Fatalf("MarkdownContent missing cost: %q", got.MarkdownContent)
	}
}

func TestEmitClickUpTaskOutput_EmptyListID(t *testing.T) {
	ch := outputChannel{Type: "clickup_task", SecretScopeID: "x"}
	err := emitClickUpTaskOutput(context.Background(), sampleOutputCtx(nil), ch)
	if err == nil || !strings.Contains(err.Error(), "empty list_id") {
		t.Fatalf("want empty-list_id error, got %v", err)
	}
}

func TestEmitClickUpTaskOutput_MissingSecret(t *testing.T) {
	ch := outputChannel{Type: "clickup_task", ListID: "9001", SecretScopeID: "x"}
	octx := sampleOutputCtx(nil)
	octx.secrets = &fakeSecretsForChannel{value: []byte{}}
	err := emitClickUpTaskOutput(context.Background(), octx, ch)
	if err == nil || !strings.Contains(err.Error(), "empty api_key") {
		t.Fatalf("want empty-api_key error, got %v", err)
	}
}

func TestEmitClickUpTaskOutput_NoSecretReader(t *testing.T) {
	ch := outputChannel{Type: "clickup_task", ListID: "9001", SecretScopeID: "x"}
	err := emitClickUpTaskOutput(context.Background(), sampleOutputCtx(nil), ch)
	if err == nil || !strings.Contains(err.Error(), "no SecretReader") {
		t.Fatalf("want no-SecretReader error, got %v", err)
	}
}

func TestEmitClickUpTaskOutput_Non2xx(t *testing.T) {
	client := mockClient(func(_ *http.Request) (*http.Response, error) {
		return statusResponse(401, `{"err":"bad token"}`), nil
	})
	octx := sampleOutputCtx(client)
	octx.secrets = &fakeSecretsForChannel{value: []byte("bad")}
	ch := outputChannel{Type: "clickup_task", ListID: "9001", SecretScopeID: "x"}
	err := emitClickUpTaskOutput(context.Background(), octx, ch)
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("want 401 error, got %v", err)
	}
}

func TestShortRunID(t *testing.T) {
	if got := shortRunID("ABC"); got != "ABC" {
		t.Fatalf("short id = %q", got)
	}
	if got := shortRunID("0123456789ABCDEF"); got != "89ABCDEF" {
		t.Fatalf("tail = %q", got)
	}
}
