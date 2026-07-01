package gateway

import (
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/compression"
)

// TestCompressionShadowMeasurementIntegration proves the measure-first path
// end-to-end within the gateway: in shadow mode the pipeline returns the tool
// result byte-identical (zero accuracy risk) while the per-transform would-be
// savings accrue in the context-cost stats that back the dashboard.
func TestCompressionShadowMeasurementIntegration(t *testing.T) {
	h := &handler{compression: newCompressionPipeline()}

	// A pretty-printed JSON array-of-objects tool result — the whitespace is
	// exactly what json_minify recovers losslessly.
	rows := make([]map[string]any, 0, 20)
	for i := range 20 {
		rows = append(rows, map[string]any{"id": i, "name": "item", "ok": true})
	}
	pretty, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	textJSON, _ := json.Marshal(string(pretty))
	env := json.RawMessage(`{"content":[{"type":"text","text":` + string(textJSON) + `}]}`)
	if len(env) < compressionMinBytes {
		t.Fatalf("fixture too small (%d bytes) to exercise the pipeline", len(env))
	}

	out, obs := h.compression.Process(compression.ModeShadow, env)
	if string(out) != string(env) {
		t.Fatalf("shadow mode MUST return the tool result untouched")
	}
	h.recordCompression(obs)

	stats := h.ContextCostStats()
	ts, ok := stats.Compression.ByTransform["json_minify"]
	if !ok {
		t.Fatalf("expected json_minify stats to be recorded")
	}
	if !ts.Lossless {
		t.Errorf("json_minify should report Lossless=true")
	}
	if ts.WouldSaveBytes == 0 || ts.WouldSaveTokens == 0 {
		t.Errorf("expected non-zero would-be savings in shadow, got bytes=%d tokens=%d", ts.WouldSaveBytes, ts.WouldSaveTokens)
	}
	if stats.Compression.AppliedSaveBytes != 0 {
		t.Errorf("shadow mode must apply nothing, got AppliedSaveBytes=%d", stats.Compression.AppliedSaveBytes)
	}
	if stats.Compression.Samples != 1 {
		t.Errorf("expected 1 sample, got %d", stats.Compression.Samples)
	}
}
