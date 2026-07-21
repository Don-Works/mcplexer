package models

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// This file holds the shared OpenAI Chat Completions wire format used by
// both the native OpenAI adapter and the openai_compat adapter. Both call
// chatCompletions; the only thing that differs is the endpoint URL.

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters"`
}

type openAITool struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIThinking struct {
	Type string `json:"type"`
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    string           `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

type openAIRequest struct {
	Model     string          `json:"model"`
	Messages  []openAIMessage `json:"messages"`
	Tools     []openAITool    `json:"tools,omitempty"`
	MaxTokens int             `json:"max_tokens,omitempty"`
	Stop      []string        `json:"stop,omitempty"`
	Thinking  *openAIThinking `json:"thinking,omitempty"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
}

type openAIChoice struct {
	Message      openAIMessage `json:"message"`
	FinishReason string        `json:"finish_reason"`
}

type openAIResponse struct {
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage"`
}

type openAIErrorBody struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	} `json:"error"`
}

// chatCompletions executes one round-trip against any OpenAI-compatible
// /v1/chat/completions endpoint. apiKey may be empty (some local servers
// don't require auth); the Authorization header is omitted in that case.
func chatCompletions(
	ctx context.Context,
	client *http.Client,
	endpoint, apiKey, modelID string,
	req SendRequest,
	thinking string,
) (*SendResponse, error) {
	body, err := buildOpenAIRequest(modelID, req, thinking)
	if err != nil {
		return nil, fmt.Errorf("openai: build request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("openai: new request: %w", err)
	}
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("content-type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openai: http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("openai: read body: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, openAIHTTPError(resp.StatusCode, raw)
	}

	var parsed openAIResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, fmt.Errorf("openai: decode response: %w", err)
	}
	return decodeOpenAIResponse(&parsed)
}

// buildOpenAIRequest serializes the common SendRequest into the OpenAI
// chat completions wire format.
func buildOpenAIRequest(modelID string, req SendRequest, thinking string) ([]byte, error) {
	msgs := make([]openAIMessage, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, openAIMessage{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		converted, err := openAIMessageFor(m)
		if err != nil {
			return nil, err
		}
		msgs = append(msgs, converted)
	}
	or := openAIRequest{
		Model:     modelID,
		Messages:  msgs,
		Tools:     openAITools(req.Tools),
		MaxTokens: req.MaxTokens,
		Stop:      req.Stop,
	}
	if thinking != "" {
		or.Thinking = &openAIThinking{Type: thinking}
	}
	return json.Marshal(or)
}

// openAIMessageFor converts one common Message into an OpenAI message.
func openAIMessageFor(m Message) (openAIMessage, error) {
	switch m.Role {
	case RoleUser:
		return openAIMessage{Role: "user", Content: m.Content}, nil
	case RoleAssistant:
		out := openAIMessage{Role: "assistant", Content: m.Content}
		for _, tc := range m.ToolCalls {
			args, err := json.Marshal(tc.Input)
			if err != nil {
				return openAIMessage{}, fmt.Errorf("openai: marshal tool input: %w", err)
			}
			call := openAIToolCall{ID: tc.ID, Type: "function"}
			call.Function.Name = tc.Name
			call.Function.Arguments = string(args)
			out.ToolCalls = append(out.ToolCalls, call)
		}
		return out, nil
	case RoleTool:
		return openAIMessage{
			Role:       "tool",
			Content:    m.ToolResult,
			ToolCallID: m.ToolUseID,
		}, nil
	case RoleSystem:
		return openAIMessage{Role: "system", Content: m.Content}, nil
	default:
		return openAIMessage{}, fmt.Errorf("openai: unknown role %q", m.Role)
	}
}

func openAITools(tools []ToolSchema) []openAITool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]openAITool, len(tools))
	for i, t := range tools {
		out[i] = openAITool{
			Type: "function",
			Function: openAIFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.InputSchema,
			},
		}
	}
	return out
}

func decodeOpenAIResponse(parsed *openAIResponse) (*SendResponse, error) {
	if len(parsed.Choices) == 0 {
		return nil, fmt.Errorf("openai: response had no choices")
	}
	choice := parsed.Choices[0]
	resp := &SendResponse{
		Text:         choice.Message.Content,
		StopReason:   normalizeOpenAIStop(choice.FinishReason),
		InputTokens:  parsed.Usage.PromptTokens,
		OutputTokens: parsed.Usage.CompletionTokens,
	}
	for _, tc := range choice.Message.ToolCalls {
		input, err := parseToolArgs(tc.Function.Arguments)
		if err != nil {
			return nil, fmt.Errorf("openai: tool call %q args: %w", tc.Function.Name, err)
		}
		resp.ToolCalls = append(resp.ToolCalls, ToolCall{
			ID:    tc.ID,
			Name:  tc.Function.Name,
			Input: input,
		})
	}
	return resp, nil
}

// parseToolArgs decodes the JSON-string arguments returned by OpenAI tool
// calls. Empty strings decode to an empty map.
func parseToolArgs(args string) (map[string]any, error) {
	if args == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(args), &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func normalizeOpenAIStop(s string) string {
	switch s {
	case "stop":
		return StopEndTurn
	case "tool_calls", "function_call":
		return StopToolUse
	case "length":
		return StopMaxTokens
	case "content_filter":
		return StopOther
	default:
		return StopOther
	}
}

func openAIHTTPError(status int, raw []byte) error {
	var body openAIErrorBody
	if err := json.Unmarshal(raw, &body); err == nil && body.Error.Message != "" {
		return fmt.Errorf("openai: http %d: %s: %s", status, body.Error.Type, body.Error.Message)
	}
	return fmt.Errorf("openai: http %d: %s", status, truncate(string(raw), 256))
}
