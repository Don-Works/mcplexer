package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"strings"

	"github.com/don-works/mcplexer/internal/config"
	"github.com/don-works/mcplexer/internal/index"
	"github.com/don-works/mcplexer/internal/memory"
)

// resolveCodeIndexEmbedder is intentionally stricter than memory embedding:
// code vectors are off by default, ignore generic/OpenAI credentials, and may
// only use a loopback OpenAI-compatible endpoint. "auto" merely probes known
// loopback ports and itself has to be explicitly selected.
func resolveCodeIndexEmbedder(ctx context.Context, s config.Settings) (index.Embedder, string) {
	provider := strings.ToLower(strings.TrimSpace(s.CodeIndexEmbedProvider))
	if provider == "" {
		provider = "none"
	}
	switch provider {
	case "none":
		slog.Info("code-index: semantic vectors disabled; source-chunk FTS5 active")
		return index.NoopEmbedder{}, ""
	case "auto":
		if det, ok := memory.DetectLocalEmbedEndpoint(ctx); ok {
			emb, err := newLoopbackCodeEmbedder(det.BaseURL, det.Model)
			if err == nil {
				slog.Info("code-index: explicitly auto-detected loopback embeddings",
					"base_url", det.BaseURL, "model", det.Model)
				return emb, det.Model
			}
		}
		slog.Warn("code-index: auto selected but no healthy loopback embedder found; FTS5-only")
		return index.NoopEmbedder{}, ""
	case "local":
		emb, err := newLoopbackCodeEmbedder(s.CodeIndexEmbedBaseURL, s.CodeIndexEmbedModel)
		if err != nil {
			slog.Warn("code-index: local embedder rejected; FTS5-only", "error", err)
			return index.NoopEmbedder{}, ""
		}
		model := strings.TrimSpace(s.CodeIndexEmbedModel)
		if model == "" {
			model = "local-embedding"
		}
		slog.Info("code-index: loopback semantic embeddings enabled",
			"base_url", s.CodeIndexEmbedBaseURL, "model", model)
		return emb, model
	default:
		slog.Warn("code-index: unknown embedding provider; FTS5-only", "provider", provider)
		return index.NoopEmbedder{}, ""
	}
}

func newLoopbackCodeEmbedder(baseURL, model string) (index.Embedder, error) {
	if err := validateLoopbackEmbedURL(baseURL); err != nil {
		return nil, err
	}
	return memory.NewLocalEmbedder(baseURL, model,
		strings.TrimSpace(os.Getenv("MCPLEXER_CODE_INDEX_EMBED_API_KEY")))
}

func validateLoopbackEmbedURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return fmt.Errorf("code index embedding endpoint must be an absolute loopback URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("code index embedding endpoint must use http or https")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("code index embedding endpoint must not contain credentials, query, or fragment")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("code index embedding endpoint must resolve explicitly to localhost/loopback")
	}
	return nil
}
