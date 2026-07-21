package runner

import (
	"testing"

	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
)

func TestWorkerModelThinking(t *testing.T) {
	tests := []struct {
		name   string
		worker *store.Worker
		want   string
	}{
		{name: "nil"},
		{name: "native provider ignores parameter", worker: &store.Worker{
			ModelProvider: models.ProviderOpenAI, ParametersJSON: `{"model_thinking":"disabled"}`,
		}},
		{name: "compat disabled", worker: &store.Worker{
			ModelProvider: models.ProviderOpenAICompat, ParametersJSON: `{"model_thinking":"disabled"}`,
		}, want: "disabled"},
		{name: "compat enabled normalized", worker: &store.Worker{
			ModelProvider: models.ProviderOpenAICompat, ParametersJSON: `{"model_thinking":" Enabled "}`,
		}, want: "enabled"},
		{name: "invalid ignored", worker: &store.Worker{
			ModelProvider: models.ProviderOpenAICompat, ParametersJSON: `{"model_thinking":"sometimes"}`,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workerModelThinking(tt.worker); got != tt.want {
				t.Fatalf("workerModelThinking() = %q, want %q", got, tt.want)
			}
		})
	}
}
