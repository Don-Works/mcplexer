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

// clickupAPIBase is the v2 API root for the public ClickUp REST API. A
// constant rather than a struct field so tests can override it via
// outputContext if we ever expose mock servers — but the contract is
// stable enough that we keep it package-private until that's needed.
const clickupAPIBase = "https://api.clickup.com/api/v2"

// clickupTaskNameLimit is the soft ceiling the UI imposes on task
// names. The API itself accepts longer strings but truncates in the
// list view, so we proactively snip here.
const clickupTaskNameLimit = 100

// clickupCreateTaskRequest is the JSON body we POST to
// /list/{list_id}/task. Markdown_content lets the description render
// formatted output in the ClickUp web app.
type clickupCreateTaskRequest struct {
	Name            string `json:"name"`
	MarkdownContent string `json:"markdown_content,omitempty"`
	Status          string `json:"status,omitempty"`
}

// emitClickUpTaskOutput creates a ClickUp task in ch.ListID, reading the
// API token from the secret scope named by ch.SecretScopeID (key
// "api_key"). The runner does NOT reuse the daemon-wide ClickUp tool —
// keeps the dependency graph clean and means a Worker can ship to a
// different workspace than the daemon's default config.
func emitClickUpTaskOutput(ctx context.Context, octx outputContext, ch outputChannel) error {
	if strings.TrimSpace(ch.ListID) == "" {
		return fmt.Errorf("clickup_task channel: empty list_id")
	}
	token, err := resolveChannelSecret(ctx, octx, ch.SecretScopeID, "clickup_task")
	if err != nil {
		return err
	}
	body, err := json.Marshal(buildClickUpRequest(octx, ch))
	if err != nil {
		return fmt.Errorf("clickup_task channel: marshal: %w", err)
	}
	url := fmt.Sprintf("%s/list/%s/task", clickupAPIBase, ch.ListID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("clickup_task channel: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", token)
	client := octx.httpClient
	if client == nil {
		return fmt.Errorf("clickup_task channel: nil http client")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("clickup_task channel: post: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("clickup_task channel: http %d: %s",
			resp.StatusCode, strings.TrimSpace(string(preview)))
	}
	return nil
}

// buildClickUpRequest renders the task body. Pulled out for testing so
// we can assert on the wire shape without a live HTTP roundtrip.
func buildClickUpRequest(octx outputContext, ch outputChannel) clickupCreateTaskRequest {
	prefix := strings.TrimSpace(ch.NamePrefix)
	name := fmt.Sprintf("%s · run %s", octx.workerName, shortRunID(octx.runID))
	if prefix != "" {
		name = fmt.Sprintf("%s %s", prefix, name)
	}
	return clickupCreateTaskRequest{
		Name:            snippet(name, clickupTaskNameLimit),
		MarkdownContent: clickupDescription(octx),
	}
}

// clickupDescription renders the run output + a small metadata footer
// as markdown so the operator gets a readable task body, not a wall of
// raw text. Cost / token counts are useful when triaging a noisy
// inbox of automated tasks.
func clickupDescription(octx outputContext) string {
	var b strings.Builder
	b.WriteString(octx.output)
	b.WriteString("\n\n---\n")
	fmt.Fprintf(&b, "- Worker: `%s`\n", octx.workerName)
	fmt.Fprintf(&b, "- Run ID: `%s`\n", octx.runID)
	fmt.Fprintf(&b, "- Status: `%s`\n", octx.status)
	fmt.Fprintf(&b, "- Cost: $%.4f\n", octx.costUSD)
	fmt.Fprintf(&b, "- Tokens: %d in / %d out\n", octx.inputTokens, octx.outputTokens)
	return b.String()
}

// shortRunID returns the trailing 8 chars of a ULID/UUID — enough to
// disambiguate tasks in a list without dumping the full identifier.
func shortRunID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[len(id)-8:]
}

// resolveChannelSecret looks up an API token from the secret scope
// named by scopeID, expecting key="api_key". Returns a wrapped error
// when the scope is missing, the key absent, or the secrets adapter is
// nil — all of which surface as a channel-level alert.
func resolveChannelSecret(ctx context.Context, octx outputContext, scopeID, channelType string) (string, error) {
	if strings.TrimSpace(scopeID) == "" {
		return "", fmt.Errorf("%s channel: empty secret_scope_id", channelType)
	}
	if octx.secrets == nil {
		return "", fmt.Errorf("%s channel: no SecretReader wired", channelType)
	}
	v, err := octx.secrets.Get(ctx, scopeID, "api_key")
	if err != nil {
		return "", fmt.Errorf("%s channel: read api_key: %w", channelType, err)
	}
	if len(v) == 0 {
		return "", fmt.Errorf("%s channel: empty api_key", channelType)
	}
	return string(v), nil
}
