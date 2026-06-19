package models

import (
	"context"
	"net/http"
	"strings"
)

// openAICompatAdapter targets any OpenAI-compatible /chat/completions
// endpoint (DeepSeek, Mistral, Together, Groq, OpenRouter, Ollama, etc.).
// Wire format is identical to OpenAI; only the base URL differs.
type openAICompatAdapter struct {
	apiKey   string
	modelID  string
	endpoint string
	client   *http.Client
}

func newOpenAICompatAdapter(apiKey, modelID, baseURL string, client *http.Client) *openAICompatAdapter {
	return &openAICompatAdapter{
		apiKey:   apiKey,
		modelID:  modelID,
		endpoint: resolveCompatEndpoint(baseURL),
		client:   client,
	}
}

// resolveCompatEndpoint accepts either a fully-qualified chat/completions
// URL or a base URL (with or without /v1) and returns the full endpoint.
func resolveCompatEndpoint(base string) string {
	trimmed := strings.TrimRight(base, "/")
	if strings.HasSuffix(trimmed, "/chat/completions") {
		return trimmed
	}
	if strings.HasSuffix(trimmed, "/v1") {
		return trimmed + "/chat/completions"
	}
	return trimmed + "/v1/chat/completions"
}

// Send delegates to the shared chatCompletions helper. apiKey may be ""
// for endpoints that don't require auth (local Ollama, etc.).
func (a *openAICompatAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
	return chatCompletions(ctx, a.client, a.endpoint, a.apiKey, a.modelID, req)
}
