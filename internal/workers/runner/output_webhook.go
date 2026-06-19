package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// webhookPayload is the JSON body POSTed when IncludeMetadata=true. The
// shape is deliberately flat so downstream consumers (Make, n8n, Zapier,
// home-rolled HTTP listeners) can pluck fields without nested
// navigation. When IncludeMetadata=false the body collapses to {output}.
type webhookPayload struct {
	WorkerName   string  `json:"worker_name,omitempty"`
	WorkerID     string  `json:"worker_id,omitempty"`
	RunID        string  `json:"run_id,omitempty"`
	Status       string  `json:"status,omitempty"`
	Output       string  `json:"output"`
	StartedAt    string  `json:"started_at,omitempty"`
	FinishedAt   string  `json:"finished_at,omitempty"`
	DurationMS   int64   `json:"duration_ms,omitempty"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

// emitWebhookOutput posts the run output as JSON to ch.URL with any
// caller-supplied headers. A 2xx status is "delivered"; everything else
// returns an error so the channel-error handler emits a mesh alert and
// records the failure in slog. Network / timeout errors propagate the
// underlying *url.Error message so operators can debug from the alert
// content alone.
func emitWebhookOutput(ctx context.Context, octx outputContext, ch outputChannel) error {
	if strings.TrimSpace(ch.URL) == "" {
		return fmt.Errorf("webhook channel: empty url")
	}
	if err := isSafeOutboundURL(ch.URL); err != nil {
		return fmt.Errorf("webhook channel: unsafe url: %w", err)
	}
	body, err := buildWebhookBody(octx, ch)
	if err != nil {
		return fmt.Errorf("webhook channel: build body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook channel: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range ch.Headers {
		req.Header.Set(k, v)
	}
	client := octx.httpClient
	if client == nil {
		return fmt.Errorf("webhook channel: nil http client")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook channel: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webhook channel: http %d: %s", resp.StatusCode, strings.TrimSpace(string(preview)))
	}
	return nil
}

// buildWebhookBody renders the JSON body for the webhook channel. When
// IncludeMetadata is false we ship only {output} so callers can keep a
// dead-simple HTTP listener; when true the full envelope rides along.
func buildWebhookBody(octx outputContext, ch outputChannel) ([]byte, error) {
	if !ch.IncludeMetadata {
		return json.Marshal(struct {
			Output string `json:"output"`
		}{Output: octx.output})
	}
	p := webhookPayload{
		WorkerName:   octx.workerName,
		WorkerID:     octx.workerID,
		RunID:        octx.runID,
		Status:       octx.status,
		Output:       octx.output,
		StartedAt:    octx.startedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		FinishedAt:   octx.finishedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
		DurationMS:   octx.durationMS,
		InputTokens:  octx.inputTokens,
		OutputTokens: octx.outputTokens,
		CostUSD:      octx.costUSD,
	}
	return json.Marshal(p)
}
