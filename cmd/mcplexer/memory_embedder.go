// memory_embedder.go — resolves the memory embedding provider at daemon
// boot. The goal is that semantic recall "just works": a user running a
// local model server (LM Studio / Ollama / llama.cpp) gets vector recall
// with zero configuration via auto-detection, while env vars and explicit
// settings remain available for headless / cloud setups.
package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/memory"
)

// resolveMemoryEmbedder builds the embedding provider in precedence order:
//  1. Env (MCPLEXER_EMBED_BASE_URL, then MCPLEXER_OPENAI_API_KEY) — the
//     headless / CI override, unchanged from prior behaviour.
//  2. Persisted settings.MemoryEmbedProvider: "none" | "local" | "openai".
//  3. "auto" (default): probe localhost for an OpenAI-compatible embeddings
//     endpoint with a 1536-dim model and use it if found.
//  4. NoopEmbedder (FTS5-only floor) when nothing is available.
//
// Never returns nil.
func resolveMemoryEmbedder(ctx context.Context, s config.Settings) memory.EmbedProvider {
	// 1. Env override (back-compat / headless).
	if base := strings.TrimSpace(os.Getenv("MCPLEXER_EMBED_BASE_URL")); base != "" {
		if e, err := memory.NewLocalEmbedder(base,
			os.Getenv("MCPLEXER_EMBED_MODEL"), os.Getenv("MCPLEXER_EMBED_API_KEY")); err == nil {
			slog.Info("memory: embeddings via env local endpoint", "base_url", base)
			return e
		} else {
			slog.Warn("memory: env local embedder construction failed", "error", err)
		}
	}
	if key := strings.TrimSpace(os.Getenv("MCPLEXER_OPENAI_API_KEY")); key != "" {
		if e, err := memory.NewOpenAIEmbedder(key, "", ""); err == nil {
			slog.Info("memory: embeddings via OpenAI (env key)")
			return e
		} else {
			slog.Warn("memory: openai embedder construction failed", "error", err)
		}
	}

	// 2/3. Persisted settings.
	switch strings.ToLower(strings.TrimSpace(s.MemoryEmbedProvider)) {
	case "none":
		slog.Info("memory: embeddings disabled by settings (FTS5-only)")
		return memory.NoopEmbedder{}
	case "local":
		if e, err := memory.NewLocalEmbedder(s.MemoryEmbedBaseURL, s.MemoryEmbedModel, ""); err == nil {
			slog.Info("memory: embeddings via configured local endpoint",
				"base_url", s.MemoryEmbedBaseURL, "model", s.MemoryEmbedModel)
			return e
		} else {
			slog.Warn("memory: configured local embedder failed — falling back to FTS5-only", "error", err)
		}
	case "openai":
		// Opted in via settings but the key only ever comes from env/secret
		// (never persisted in the settings row). Without it we can't embed.
		slog.Warn("memory: provider=openai but MCPLEXER_OPENAI_API_KEY is unset — FTS5-only")
	case "", "auto":
		if det, ok := memory.DetectLocalEmbedEndpoint(ctx); ok {
			if e, err := memory.NewLocalEmbedder(det.BaseURL, det.Model, ""); err == nil {
				slog.Info("memory: auto-detected local embeddings endpoint",
					"base_url", det.BaseURL, "model", det.Model)
				return e
			}
		}
		slog.Info("memory: no embeddings endpoint detected — recall is keyword-only. " +
			"Start LM Studio/Ollama with an embedding model, or configure one in Settings → Memory.")
	default:
		slog.Warn("memory: unknown memory_embed_provider — FTS5-only", "provider", s.MemoryEmbedProvider)
	}
	return memory.NoopEmbedder{}
}
