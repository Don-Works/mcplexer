package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// roundTripper is the shared HTTP-mocking primitive for the output
// channel tests. Each test injects a function that captures the request
// (URL, headers, body) and returns the desired response, so we exercise
// the marshaling logic end-to-end without a real listener.
type roundTripper struct {
	fn func(*http.Request) (*http.Response, error)
}

func (rt *roundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return rt.fn(req)
}

// mockClient wraps the closure into a usable http.Client.
func mockClient(fn func(*http.Request) (*http.Response, error)) *http.Client {
	return &http.Client{Transport: &roundTripper{fn: fn}}
}

// statusResponse builds a minimal *http.Response for the given status.
// Empty body string → empty io.NopCloser; non-empty wraps a reader.
func statusResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func sampleOutputCtx(client *http.Client) outputContext {
	return outputContext{
		workerID:     "wrk_123",
		workerName:   "smoke-test",
		runID:        "run_abc",
		status:       StatusSuccess,
		output:       "All clear, no issues found.",
		startedAt:    time.Date(2026, 5, 21, 9, 0, 0, 0, time.UTC),
		finishedAt:   time.Date(2026, 5, 21, 9, 0, 12, 0, time.UTC),
		durationMS:   12000,
		inputTokens:  120,
		outputTokens: 60,
		costUSD:      0.0042,
		httpClient:   client,
	}
}

func TestEmitWebhookOutput_WithMetadata(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		captured = req
		capturedBody, _ = io.ReadAll(req.Body)
		return statusResponse(200, ""), nil
	})
	ctx := context.Background()
	ch := outputChannel{
		Type:            "webhook",
		URL:             "https://hooks.example.com/run",
		Headers:         map[string]string{"X-Token": "abc"},
		IncludeMetadata: true,
	}
	if err := emitWebhookOutput(ctx, sampleOutputCtx(client), ch); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if captured == nil {
		t.Fatal("expected captured request")
	}
	if captured.URL.String() != ch.URL {
		t.Fatalf("URL = %q, want %q", captured.URL, ch.URL)
	}
	if got := captured.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := captured.Header.Get("X-Token"); got != "abc" {
		t.Fatalf("X-Token = %q", got)
	}
	var payload webhookPayload
	if err := json.Unmarshal(capturedBody, &payload); err != nil {
		t.Fatalf("unmarshal: %v / %s", err, string(capturedBody))
	}
	if payload.WorkerName != "smoke-test" || payload.RunID != "run_abc" {
		t.Fatalf("payload missing metadata: %+v", payload)
	}
	if payload.CostUSD != 0.0042 {
		t.Fatalf("cost not propagated: %v", payload.CostUSD)
	}
}

func TestEmitWebhookOutput_BareOutput(t *testing.T) {
	var capturedBody []byte
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		capturedBody, _ = io.ReadAll(req.Body)
		return statusResponse(204, ""), nil
	})
	ch := outputChannel{Type: "webhook", URL: "https://hooks.example.com/run"}
	if err := emitWebhookOutput(context.Background(), sampleOutputCtx(client), ch); err != nil {
		t.Fatalf("emit: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(capturedBody, &got); err != nil {
		t.Fatalf("unmarshal: %v / %s", err, string(capturedBody))
	}
	if _, ok := got["worker_name"]; ok {
		t.Fatalf("metadata leaked into bare payload: %v", got)
	}
	if got["output"] != "All clear, no issues found." {
		t.Fatalf("output = %v", got["output"])
	}
}

func TestEmitWebhookOutput_Non2xx(t *testing.T) {
	client := mockClient(func(_ *http.Request) (*http.Response, error) {
		return statusResponse(503, "service unavailable"), nil
	})
	ch := outputChannel{Type: "webhook", URL: "https://hooks.example.com/run"}
	err := emitWebhookOutput(context.Background(), sampleOutputCtx(client), ch)
	if err == nil {
		t.Fatal("expected error on 503")
	}
	if !strings.Contains(err.Error(), "503") {
		t.Fatalf("error doesn't mention status: %v", err)
	}
}

func TestEmitWebhookOutput_TransportError(t *testing.T) {
	want := errors.New("boom")
	client := mockClient(func(_ *http.Request) (*http.Response, error) {
		return nil, want
	})
	ch := outputChannel{Type: "webhook", URL: "https://hooks.example.com/run"}
	err := emitWebhookOutput(context.Background(), sampleOutputCtx(client), ch)
	if err == nil {
		t.Fatal("expected transport error")
	}
}

func TestEmitWebhookOutput_EmptyURL(t *testing.T) {
	ch := outputChannel{Type: "webhook"}
	err := emitWebhookOutput(context.Background(), sampleOutputCtx(nil), ch)
	if err == nil || !strings.Contains(err.Error(), "empty url") {
		t.Fatalf("want empty-url error, got %v", err)
	}
}
