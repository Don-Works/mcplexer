package models

import (
	"context"
	"net/http"
)

const openAIDefaultEndpoint = "https://api.openai.com/v1/chat/completions"

// openAIAdapter is the native OpenAI Chat Completions adapter.
type openAIAdapter struct {
	apiKey   string
	modelID  string
	endpoint string
	client   *http.Client
}

func newOpenAIAdapter(apiKey, modelID, endpoint string, client *http.Client) *openAIAdapter {
	if endpoint == "" {
		endpoint = openAIDefaultEndpoint
	}
	return &openAIAdapter{apiKey: apiKey, modelID: modelID, endpoint: endpoint, client: client}
}

// Send delegates to the shared chatCompletions helper.
func (a *openAIAdapter) Send(ctx context.Context, req SendRequest) (*SendResponse, error) {
	return chatCompletions(ctx, a.client, a.endpoint, a.apiKey, a.modelID, req, "")
}
