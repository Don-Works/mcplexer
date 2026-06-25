// embed_detect.go — zero-config discovery of a local OpenAI-compatible
// embeddings endpoint so semantic memory recall "just works" for users
// running a local model server (LM Studio, Ollama, llama.cpp) without
// touching env vars or the dashboard. The daemon calls
// DetectLocalEmbedEndpoint on boot when the embed provider is "auto" and
// no explicit endpoint is configured.
//
// Correctness over guesswork: a candidate model is only accepted after we
// actually embed a probe string and confirm the vector is exactly EmbedDim
// (1536) wide — memories_vec is FLOAT[1536] and a mismatched model would
// corrupt recall. Detection is fast: a bounded candidate list with short
// per-request timeouts.
package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// localEmbedCandidates are the OpenAI-compatible /v1 base URLs probed in
// priority order. LM Studio (1234) and the llama.cpp server (8080) speak
// /v1/embeddings directly; Ollama (11434) exposes an OpenAI-compatible
// shim under /v1.
var localEmbedCandidates = []string{
	"http://localhost:1234/v1",
	"http://127.0.0.1:1234/v1",
	"http://localhost:11434/v1",
	"http://localhost:8080/v1",
}

// DetectedEmbedder is a verified local embedding endpoint + model.
type DetectedEmbedder struct {
	BaseURL string
	Model   string
}

// DetectLocalEmbedEndpoint probes the well-known local servers for a loaded
// embedding model that emits EmbedDim-wide vectors. Returns the first
// verified (base, model) pair; ok=false when nothing suitable is reachable.
func DetectLocalEmbedEndpoint(ctx context.Context) (DetectedEmbedder, bool) {
	client := &http.Client{Timeout: 1500 * time.Millisecond}
	for _, base := range localEmbedCandidates {
		models, err := listLocalModels(ctx, client, base)
		if err != nil || len(models) == 0 {
			continue
		}
		for _, m := range orderEmbedModelsFirst(models) {
			if probeEmbedDim(ctx, base, m) {
				return DetectedEmbedder{BaseURL: base, Model: m}, true
			}
		}
	}
	return DetectedEmbedder{}, false
}

// listLocalModels GETs <base>/models and returns the advertised model ids.
func listLocalModels(ctx context.Context, client *http.Client, base string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(base, "/")+"/models", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode >= 400 {
		return nil, nil
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}

// orderEmbedModelsFirst sorts ids whose name looks like an embedding model
// ("embed" substring) to the front, so the likely candidate is probed
// first while still falling back to every advertised model.
func orderEmbedModelsFirst(models []string) []string {
	var embed, rest []string
	for _, m := range models {
		if strings.Contains(strings.ToLower(m), "embed") {
			embed = append(embed, m)
		} else {
			rest = append(rest, m)
		}
	}
	return append(embed, rest...)
}

// probeEmbedDim confirms (base, model) actually produces an EmbedDim-wide
// vector. Reuses the local embedder so the exact recall-time request shape
// (dimensions=EmbedDim) is what's validated; a non-1536 model fails the
// embedder's own dim check and is rejected here.
func probeEmbedDim(ctx context.Context, base, model string) bool {
	e, err := NewLocalEmbedder(base, model, "")
	if err != nil {
		return false
	}
	pctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	vecs, _, err := e.Embed(pctx, []string{"ping"})
	return err == nil && len(vecs) == 1 && len(vecs[0]) == EmbedDim
}
