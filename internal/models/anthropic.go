package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const (
	anthropicDefaultEndpoint = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion      = "2023-06-01"
	// Anthropic requires max_tokens; pick a safe default when caller omits.
	anthropicDefaultMaxTokens = 4096
)

// anthropicAdapter implements ModelAdapter against the Anthropic Messages API.
type anthropicAdapter struct {
	apiKey   string
	modelID  string
	endpoint string
	client   *http.Client
}

func newAnthropicAdapter(apiKey, modelID, endpoint string, client *http.Client) *anthropicAdapter {
	if endpoint == "" {
		endpoint = anthropicDefaultEndpoint
	}
	return &anthropicAdapter{apiKey: apiKey, modelID: modelID, endpoint: endpoint, client: client}
}

// cacheControl is the ephemeral prompt-cache marker.
type cacheControl struct {
	Type string `json:"type"`
}

type anthropicSystemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type anthropicTool struct {
	Name         string         `json:"name"`
	Description  string         `json:"description,omitempty"`
	InputSchema  map[string]any `json:"input_schema"`
	CacheControl *cacheControl  `json:"cache_control,omitempty"`
}

type anthropicContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text,omitempty"`
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Input     map[string]any `json:"input,omitempty"`
	ToolUseID string         `json:"tool_use_id,omitempty"`
	Content   string         `json:"content,omitempty"`
}

type anthropicMessage struct {
	Role    string                  `json:"role"`
	Content []anthropicContentBlock `json:"content"`
}

type anthropicRequest struct {
	Model         string                 `json:"model"`
	MaxTokens     int                    `json:"max_tokens"`
	System        []anthropicSystemBlock `json:"system,omitempty"`
	Messages      []anthropicMessage     `json:"messages"`
	Tools         []anthropicTool        `json:"tools,omitempty"`
	StopSequences []string               `json:"stop_sequences,omitempty"`
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

type anthropicResponse struct {
	Content    []anthropicContentBlock `json:"content"`
	StopReason string                  `json:"stop_reason"`
	Usage      anthropicUsage          `json:"usage"`
}

type anthropicErrorBody struct {
	Type  string `json:"type"`
	Error struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Send executes one round-trip against the Anthropic Messages API.
func (a *anthropicAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
	body, err := a.buildRequest(req)
	if err != nil {
		return nil, fmt.Errorf("anthropic: build request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, a.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("anthropic: new request: %w", err)
	}
	httpReq.Header.Set("x-api-key", a.apiKey)
	httpReq.Header.Set("anthropic-version", anthropicAPIVersion)
	httpReq.Header.Set("content-type", "application/json")

	resp, err := a.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("anthropic: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("anthropic: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, anthropicHTTPError(resp.StatusCode, raw)
	}

	var parsed anthropicResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("anthropic: decode response: %w", err)
	}
	return decodeAnthropicResponse(&parsed), nil
}

// buildRequest serializes the common SendRequest into Anthropic's wire format.
func (a *anthropicAdapter) buildRequest(req SendRequest) ([]byte, error) {
	maxTok := req.MaxTokens
	if maxTok <= 0 {
		maxTok = anthropicDefaultMaxTokens
	}
	ar := anthropicRequest{
		Model:         a.modelID,
		MaxTokens:     maxTok,
		Messages:      anthropicMessages(req.Messages),
		Tools:         anthropicTools(req.Tools),
		StopSequences: req.Stop,
	}
	if req.System != "" {
		ar.System = []anthropicSystemBlock{{
			Type:         "text",
			Text:         req.System,
			CacheControl: &cacheControl{Type: "ephemeral"},
		}}
	}
	return json.Marshal(ar)
}

func anthropicHTTPError(status int, raw []byte) error {
	var body anthropicErrorBody
	if err := json.Unmarshal(raw, &body); err == nil && body.Error.Message != "" {
		return fmt.Errorf("anthropic: http %d: %s: %s", status, body.Error.Type, body.Error.Message)
	}
	return fmt.Errorf("anthropic: http %d: %s", status, truncate(string(raw), 256))
}
