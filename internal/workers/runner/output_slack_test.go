package runner

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestEmitSlackWebhookOutput_Shape(t *testing.T) {
	var captured *http.Request
	var capturedBody []byte
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		captured = req
		capturedBody, _ = io.ReadAll(req.Body)
		return statusResponse(200, "ok"), nil
	})
	ch := outputChannel{
		Type:    "slack_webhook",
		URL:     "https://hooks.example.invalid/services/T/B/X",
		Channel: "#alerts",
		Prefix:  "[smoke]",
	}
	if err := emitSlackWebhookOutput(context.Background(), sampleOutputCtx(client), ch); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if captured.URL.String() != ch.URL {
		t.Fatalf("URL = %q", captured.URL)
	}
	var got slackPayload
	if err := json.Unmarshal(capturedBody, &got); err != nil {
		t.Fatalf("unmarshal: %v / %s", err, string(capturedBody))
	}
	if !strings.HasPrefix(got.Text, "[smoke] smoke-test finished") {
		t.Fatalf("Text = %q", got.Text)
	}
	if got.Channel != "#alerts" {
		t.Fatalf("Channel = %q", got.Channel)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %d", len(got.Attachments))
	}
	att := got.Attachments[0]
	if !strings.Contains(att.Text, "All clear") {
		t.Fatalf("attachment text missing output: %q", att.Text)
	}
	if att.Color != "#36a64f" {
		t.Fatalf("status color = %q", att.Color)
	}
	if len(att.Fields) != 4 {
		t.Fatalf("fields = %d", len(att.Fields))
	}
}

func TestEmitSlackWebhookOutput_LongOutputTruncatesText(t *testing.T) {
	long := strings.Repeat("x", 600)
	var capturedBody []byte
	client := mockClient(func(req *http.Request) (*http.Response, error) {
		capturedBody, _ = io.ReadAll(req.Body)
		return statusResponse(200, ""), nil
	})
	octx := sampleOutputCtx(client)
	octx.output = long
	ch := outputChannel{Type: "slack_webhook", URL: "https://hooks.example.invalid/services/x"}
	if err := emitSlackWebhookOutput(context.Background(), octx, ch); err != nil {
		t.Fatalf("emit: %v", err)
	}
	var got slackPayload
	_ = json.Unmarshal(capturedBody, &got)
	if !strings.HasSuffix(got.Text, "…") {
		t.Fatalf("Text not truncated: %q", got.Text)
	}
	if !strings.Contains(got.Attachments[0].Text, long) {
		t.Fatalf("full output not in attachment")
	}
}

func TestSlackStatusColor(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{StatusSuccess, "#36a64f"},
		{StatusFailure, "#d93025"},
		{StatusCapExceeded, "#d93025"},
		{StatusAwaitingApproval, "#f0b400"},
		{"other", "#888888"},
	}
	for _, c := range cases {
		if got := slackStatusColor(c.status); got != c.want {
			t.Errorf("slackStatusColor(%q) = %q, want %q", c.status, got, c.want)
		}
	}
}

func TestEmitSlackWebhookOutput_EmptyURL(t *testing.T) {
	ch := outputChannel{Type: "slack_webhook"}
	err := emitSlackWebhookOutput(context.Background(), sampleOutputCtx(nil), ch)
	if err == nil || !strings.Contains(err.Error(), "empty url") {
		t.Fatalf("want empty-url error, got %v", err)
	}
}
