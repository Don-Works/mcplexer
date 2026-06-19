// rerank_provider.go — pluggable cross-encoder rerank provider. A
// cross-encoder scores each (query, document) pair jointly rather than
// embedding them independently, which is the single biggest precision
// lever once bi-encoder retrieval (FTS + vector KNN, RRF-fused) has
// produced a candidate pool. We ship NoopReranker (default, no network)
// and HTTPReranker speaking the common Jina/Cohere/OpenAI-compatible
// rerank request shape, configured via MCPLEXER_RERANK_*.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// RerankProvider re-scores a candidate document set against the query
// with a cross-encoder. Rerank returns one relevance score per input
// document, in input order (higher = more relevant). HasModel returns
// false for the noop fallback so callers skip the rerank stage entirely.
type RerankProvider interface {
	Rerank(ctx context.Context, query string, docs []string) ([]float64, error)
	HasModel() bool
}

// NoopReranker is the no-network default. HasModel is false so Recall
// never invokes Rerank; the fused order is returned unchanged.
type NoopReranker struct{}

// Rerank satisfies RerankProvider but does nothing.
func (NoopReranker) Rerank(_ context.Context, _ string, _ []string) ([]float64, error) {
	return nil, nil
}

// HasModel reports false so callers skip the cross-encoder stage.
func (NoopReranker) HasModel() bool { return false }

// HTTPReranker calls an OpenAI-compatible / Jina / Cohere-style rerank
// endpoint. The request shape ({model, query, documents:[...]}) and the
// response shape ({results:[{index, relevance_score}]}) are shared across
// those vendors, so one implementation covers the common case. Configured
// by MCPLEXER_RERANK_BASE_URL / _MODEL / _API_KEY. Mirrors OpenAIEmbedder:
// 30s timeout, bearer auth, /rerank appended to the base URL.
type HTTPReranker struct {
	APIKey  string
	Model   string
	BaseURL string // full base, e.g. https://api.jina.ai/v1 or http://localhost:1234/v1
	HTTP    *http.Client
}

// ErrNoRerankBaseURL is returned when constructing without a base URL.
var ErrNoRerankBaseURL = errors.New("memory: http reranker requires MCPLEXER_RERANK_BASE_URL")

// NewHTTPReranker constructs a reranker. baseURL is mandatory; model and
// apiKey may be empty (some self-hosted rerank servers need neither).
func NewHTTPReranker(baseURL, model, apiKey string) (*HTTPReranker, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, ErrNoRerankBaseURL
	}
	if model == "" {
		model = "rerank-english-v3.0"
	}
	return &HTTPReranker{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// HasModel reports true whenever a base URL is configured.
func (r *HTTPReranker) HasModel() bool { return r != nil && strings.TrimSpace(r.BaseURL) != "" }

// Rerank POSTs to /rerank and returns a relevance score per document in
// the ORIGINAL input order. The vendor response lists results keyed by
// their original index (often re-sorted by score), so we scatter the
// scores back into input order.
//
// Coverage is enforced: the response MUST carry exactly one usable score
// for EVERY input doc. If it is empty, decodes to no usable results, or
// covers fewer than all docs (missing, out-of-range, or duplicate indices),
// Rerank returns an error rather than a zero-filled slice — a partial
// response silently scoring the uncovered docs 0 would corrupt the ranking
// while the caller believed reranking succeeded. The caller
// (crossEncoderReorder) treats any error as "fall back to the pre-rerank
// order".
func (r *HTTPReranker) Rerank(ctx context.Context, query string, docs []string) ([]float64, error) {
	if r == nil || strings.TrimSpace(r.BaseURL) == "" {
		return nil, ErrNoRerankBaseURL
	}
	if len(docs) == 0 {
		return nil, nil
	}
	reqBody := map[string]any{
		"model":     r.Model,
		"query":     query,
		"documents": docs,
		"top_n":     len(docs),
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("rerank marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(r.BaseURL, "/")+"/rerank",
		bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("rerank req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(r.APIKey) != "" {
		req.Header.Set("Authorization", "Bearer "+r.APIKey)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rerank http: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode >= 400 {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		return nil, fmt.Errorf("rerank http %d: %s", resp.StatusCode, buf.String())
	}
	var out struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("rerank decode: %w", err)
	}
	if len(out.Results) == 0 {
		return nil, fmt.Errorf("rerank: empty results for %d docs", len(docs))
	}
	scores := make([]float64, len(docs))
	seen := make([]bool, len(docs))
	covered := 0
	for _, res := range out.Results {
		if res.Index < 0 || res.Index >= len(docs) {
			return nil, fmt.Errorf("rerank: result index %d out of range [0,%d)",
				res.Index, len(docs))
		}
		if seen[res.Index] {
			return nil, fmt.Errorf("rerank: duplicate result index %d", res.Index)
		}
		seen[res.Index] = true
		scores[res.Index] = res.RelevanceScore
		covered++
	}
	if covered != len(docs) {
		return nil, fmt.Errorf("rerank: response covered %d of %d docs", covered, len(docs))
	}
	return scores, nil
}
