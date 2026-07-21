package main

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/config"
)

func TestValidateLoopbackEmbedURL(t *testing.T) {
	for _, raw := range []string{
		"http://localhost:1234/v1", "http://127.0.0.1:11434/v1", "http://[::1]:8080/v1",
	} {
		if err := validateLoopbackEmbedURL(raw); err != nil {
			t.Errorf("validateLoopbackEmbedURL(%q) = %v", raw, err)
		}
	}
	for _, raw := range []string{
		"", "https://api.openai.com/v1", "http://192.168.1.2:1234/v1",
		"file:///tmp/embed", "http://user:pass@localhost:1234/v1", "http://localhost:1234/v1?q=x",
	} {
		if err := validateLoopbackEmbedURL(raw); err == nil {
			t.Errorf("validateLoopbackEmbedURL(%q) unexpectedly accepted non-loopback/unsafe URL", raw)
		}
	}
}

func TestResolveCodeIndexEmbedderDefaultsToNoopAndIgnoresOpenAI(t *testing.T) {
	t.Setenv("MCPLEXER_OPENAI_API_KEY", "must-not-be-used-for-code")
	emb, model := resolveCodeIndexEmbedder(context.Background(), config.DefaultSettings())
	if emb.HasModel() || model != "" {
		t.Fatalf("default resolver enabled code embeddings: model=%q active=%v", model, emb.HasModel())
	}
}

func TestResolveCodeIndexEmbedderRejectsRemoteConfiguredURL(t *testing.T) {
	s := config.DefaultSettings()
	s.CodeIndexEmbedProvider = "local"
	s.CodeIndexEmbedBaseURL = "https://embeddings.example.com/v1"
	s.CodeIndexEmbedModel = "code-model"
	emb, model := resolveCodeIndexEmbedder(context.Background(), s)
	if emb.HasModel() || model != "" {
		t.Fatalf("remote endpoint was enabled: model=%q active=%v", model, emb.HasModel())
	}
}
