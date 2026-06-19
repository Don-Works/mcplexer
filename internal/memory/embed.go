// embed.go — pluggable embedding provider interface. v1 ships two
// implementations: NoopEmbedder (default, no network) and OpenAIEmbedder
// (opt-in once the user sets an API key). Wiring decides which to use.
//
// Design constraint: every embedder produces 1536-dim vectors so the
// memories_vec table dimension is fixed. OpenAI text-embedding-3-small
// is 1536 natively. Future providers must either match natively or be
// projected/truncated to 1536.
//
// # Local embeddings (LM Studio / Ollama / any OpenAI-compatible server)
//
// OpenAIEmbedder honours a BaseURL override, so a local OpenAI-compatible
// /embeddings endpoint (LM Studio, llama.cpp server, Ollama's OpenAI
// shim) works without code changes — point MCPLEXER_EMBED_BASE_URL at it
// and set MCPLEXER_EMBED_MODEL. The API key is optional for local
// servers (most accept any/empty bearer); NewLocalEmbedder injects a
// sentinel so HasModel stays true. CRITICAL: memories_vec is
// FLOAT[1536]. The local model MUST emit exactly 1536-dim vectors — Embed
// returns a clear dim-mismatch error otherwise. We do NOT support
// dynamic or variable dimensions; pick a 1536-dim local model.
//
// # Cross-encoder rerank (RerankProvider)
//
// On top of bi-encoder retrieval (FTS + vector KNN fused with RRF) an
// optional cross-encoder rerank gives the single biggest precision lever:
// it scores each (query, doc) pair jointly. RerankProvider is pluggable
// like EmbedProvider — NoopReranker (default, HasModel=false) and one
// HTTP implementation (HTTPReranker) speaking the common Jina/Cohere/
// OpenAI-compatible rerank shape, configured via MCPLEXER_RERANK_*.
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

// EmbedDim is the fixed vector dimension every provider must produce.
// Tied to migration 058's memories_vec FLOAT[1536] declaration.
const EmbedDim = 1536

// EmbedProvider returns embeddings for one or more input strings,
// alongside the canonical model name used. HasModel returns false when
// the provider is the noop fallback — callers use it to skip the
// vector branch entirely.
type EmbedProvider interface {
	Embed(ctx context.Context, inputs []string) (vectors [][]float32, model string, err error)
	HasModel() bool
}

// NoopEmbedder is the no-network fallback. Embed always returns an
// empty slice; HasModel is false so callers route around it.
type NoopEmbedder struct{}

// Embed satisfies EmbedProvider but produces no vectors.
func (NoopEmbedder) Embed(_ context.Context, _ []string) ([][]float32, string, error) {
	return nil, "", nil
}

// HasModel reports false so callers skip the vector branch.
func (NoopEmbedder) HasModel() bool { return false }

// OpenAIEmbedder calls the OpenAI embeddings endpoint with
// text-embedding-3-small (1536 dims). API key is required at
// construction time; an empty key returns ErrNoOpenAIKey.
type OpenAIEmbedder struct {
	APIKey  string
	Model   string // defaults to "text-embedding-3-small"
	BaseURL string // defaults to https://api.openai.com/v1
	HTTP    *http.Client
}

// ErrNoOpenAIKey is returned when constructing without an API key.
var ErrNoOpenAIKey = errors.New("memory: openai embedder requires API key")

// NewOpenAIEmbedder constructs an embedder. apiKey is mandatory.
// baseURL + model fall back to canonical defaults when empty.
func NewOpenAIEmbedder(apiKey, baseURL, model string) (*OpenAIEmbedder, error) {
	if strings.TrimSpace(apiKey) == "" {
		return nil, ErrNoOpenAIKey
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	return &OpenAIEmbedder{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: baseURL,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
	}, nil
}

// localEmbedKeySentinel is injected when a local embedder is configured
// without an API key, so HasModel (which keys off APIKey != "") still
// reports true. Local OpenAI-compatible servers ignore the bearer token.
const localEmbedKeySentinel = "local-no-key"

// NewLocalEmbedder constructs an embedder pointed at a local
// OpenAI-compatible /embeddings endpoint (LM Studio, llama.cpp, Ollama's
// OpenAI shim). baseURL is mandatory; model defaults to a generic name
// when empty; apiKey is optional (a sentinel is injected so the provider
// stays "active"). The model MUST emit 1536-dim vectors — see the package
// doc + Embed's dim check; a mismatch returns a clear error at recall
// time rather than corrupting the fixed-dimension vector table.
func NewLocalEmbedder(baseURL, model, apiKey string) (*OpenAIEmbedder, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, errors.New("memory: local embedder requires MCPLEXER_EMBED_BASE_URL")
	}
	if strings.TrimSpace(apiKey) == "" {
		apiKey = localEmbedKeySentinel
	}
	if model == "" {
		model = "local-embedding"
	}
	return &OpenAIEmbedder{
		APIKey:  apiKey,
		Model:   model,
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// HasModel reports true so callers exercise the vector path.
func (e *OpenAIEmbedder) HasModel() bool { return e != nil && e.APIKey != "" }

// Embed POSTs to /embeddings and returns the vector slice in input
// order. dimensions=EmbedDim is sent explicitly so text-embedding-3-large
// (3072 native) can be downscaled to fit the same table.
func (e *OpenAIEmbedder) Embed(ctx context.Context, inputs []string) ([][]float32, string, error) {
	if e == nil || e.APIKey == "" {
		return nil, "", ErrNoOpenAIKey
	}
	if len(inputs) == 0 {
		return nil, e.Model, nil
	}
	reqBody := map[string]any{
		"model":      e.Model,
		"input":      inputs,
		"dimensions": EmbedDim,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, "", fmt.Errorf("embed marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(e.BaseURL, "/")+"/embeddings",
		bytes.NewReader(body))
	if err != nil {
		return nil, "", fmt.Errorf("embed req: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.APIKey)
	resp, err := e.HTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("embed http: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode >= 400 {
		buf := new(bytes.Buffer)
		_, _ = buf.ReadFrom(resp.Body)
		return nil, "", fmt.Errorf("embed http %d: %s", resp.StatusCode, buf.String())
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", fmt.Errorf("embed decode: %w", err)
	}
	vecs := make([][]float32, 0, len(out.Data))
	for _, d := range out.Data {
		if len(d.Embedding) != EmbedDim {
			return nil, "", fmt.Errorf("embed dim mismatch: got %d, want %d",
				len(d.Embedding), EmbedDim)
		}
		vecs = append(vecs, d.Embedding)
	}
	return vecs, e.Model, nil
}
