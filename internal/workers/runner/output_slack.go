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

// slackTextSnippet is the soft cap for the `text` field on a Slack
// incoming webhook payload. Longer outputs are truncated into `text`
// and the full body rides along inside `attachments[0].text` so the
// channel preview stays readable without losing data.
const slackTextSnippet = 280

// slackPayload is the subset of the Slack incoming-webhook contract we
// use. The Slack API ignores unknown fields, so the wire format is safe
// to extend if we ever need blocks. The structure is stable enough to
// avoid pulling in slack-go.
type slackPayload struct {
	Text        string            `json:"text"`
	Channel     string            `json:"channel,omitempty"`
	Attachments []slackAttachment `json:"attachments,omitempty"`
}

type slackAttachment struct {
	Color  string       `json:"color,omitempty"`
	Title  string       `json:"title,omitempty"`
	Text   string       `json:"text,omitempty"`
	Fields []slackField `json:"fields,omitempty"`
}

type slackField struct {
	Title string `json:"title"`
	Value string `json:"value"`
	Short bool   `json:"short,omitempty"`
}

// emitSlackWebhookOutput posts a Slack-shaped JSON body to ch.URL. The
// `text` line is the human-readable summary; `attachments[0]` carries
// the full output + a metadata block so operators can pivot from the
// channel to the dashboard run ID without juggling tabs.
func emitSlackWebhookOutput(ctx context.Context, octx outputContext, ch outputChannel) error {
	if strings.TrimSpace(ch.URL) == "" {
		return fmt.Errorf("slack_webhook channel: empty url")
	}
	if err := isSafeOutboundURL(ch.URL); err != nil {
		return fmt.Errorf("slack_webhook channel: unsafe url: %w", err)
	}
	body, err := json.Marshal(buildSlackPayload(octx, ch))
	if err != nil {
		return fmt.Errorf("slack_webhook channel: marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("slack_webhook channel: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	client := octx.httpClient
	if client == nil {
		return fmt.Errorf("slack_webhook channel: nil http client")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("slack_webhook channel: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("slack_webhook channel: http %d: %s",
			resp.StatusCode, strings.TrimSpace(string(preview)))
	}
	return nil
}

// buildSlackPayload assembles the slackPayload. Split out so tests can
// drive it without spinning an HTTP server. Empty prefix collapses to
// just the worker name.
func buildSlackPayload(octx outputContext, ch outputChannel) slackPayload {
	prefix := strings.TrimSpace(ch.Prefix)
	headline := fmt.Sprintf("%s finished", octx.workerName)
	if prefix != "" {
		headline = fmt.Sprintf("%s %s", prefix, headline)
	}
	textSnippet := snippet(octx.output, slackTextSnippet)
	color := slackStatusColor(octx.status)
	att := slackAttachment{
		Color:  color,
		Title:  fmt.Sprintf("Run %s", octx.runID),
		Text:   octx.output,
		Fields: slackMetadataFields(octx),
	}
	return slackPayload{
		Text:        fmt.Sprintf("%s: %s", headline, textSnippet),
		Channel:     strings.TrimSpace(ch.Channel),
		Attachments: []slackAttachment{att},
	}
}

// slackStatusColor maps run.status to a Slack-friendly hex string. Slack
// also accepts the literals "good"/"warning"/"danger"; we use hex so
// terminal renders that don't translate the literals still look right.
func slackStatusColor(status string) string {
	switch status {
	case StatusSuccess:
		return "#36a64f"
	case StatusFailure, StatusCapExceeded:
		return "#d93025"
	case StatusAwaitingApproval:
		return "#f0b400"
	default:
		return "#888888"
	}
}

// slackMetadataFields renders a compact 2-col grid Slack picks up as
// inline metadata under the attachment title.
func slackMetadataFields(octx outputContext) []slackField {
	return []slackField{
		{Title: "Status", Value: octx.status, Short: true},
		{Title: "Cost", Value: fmt.Sprintf("$%.4f", octx.costUSD), Short: true},
		{Title: "Input tokens", Value: fmt.Sprintf("%d", octx.inputTokens), Short: true},
		{Title: "Output tokens", Value: fmt.Sprintf("%d", octx.outputTokens), Short: true},
	}
}

// snippet returns a UTF-8 safe substring of s, suffixed with "…" when
// truncation happened. Used both here and in the GitHub / ClickUp
// channels to keep title lines under their respective ceilings.
func snippet(s string, max int) string {
	s = strings.TrimSpace(s)
	if max <= 0 || len(s) <= max {
		return s
	}
	// Walk back to a rune boundary so we don't slice a multi-byte char.
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}
