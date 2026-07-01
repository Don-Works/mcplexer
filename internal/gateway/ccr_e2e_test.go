package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/compression"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestCCREndToEnd exercises the full reversible-lossy loop against a REAL
// sqlite store: on-mode compression truncates an oversize payload + stashes the
// original, the kill-switch confirms the marker resolves, and mcpx__retrieve
// hands back the exact original bytes — with a graceful miss on an unknown key.
func TestCCREndToEnd(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "ccr.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()

	h := &handler{store: db, compression: newCompressionPipeline()}

	big := strings.Repeat("data line with content 0123456789\n", 400) // ~13.6KB > 8KB threshold
	textJSON, _ := json.Marshal(big)
	env := json.RawMessage(`{"content":[{"type":"text","text":` + string(textJSON) + `}]}`)

	compressed, obs := h.compression.Process(compression.ModeOn, nil, env)
	if string(compressed) == string(env) {
		t.Fatal("expected on-mode compression to truncate the oversize payload")
	}
	if len(compressed) >= len(env) {
		t.Errorf("compressed not smaller: %d >= %d", len(compressed), len(env))
	}

	// Persist stash (as the handler does) then confirm the kill-switch passes.
	h.persistCCR(ctx, obs)
	if !h.ccrMarkersResolve(ctx, compressed) {
		t.Fatal("kill-switch: markers did not resolve after persisting stash")
	}

	// The model expands the marker via mcpx__retrieve → exact original back.
	keys := compression.ParseCCRKeys(string(compressed))
	if len(keys) != 1 {
		t.Fatalf("expected exactly 1 marker key, got %d", len(keys))
	}
	res, rpcErr := h.handleRetrieve(ctx, json.RawMessage(`{"key":"`+keys[0]+`"}`))
	if rpcErr != nil {
		t.Fatalf("retrieve error: %v", rpcErr)
	}
	if retrievedText(t, res) != big {
		t.Error("retrieved original does not match the input text")
	}

	// Cache miss returns a graceful re-run message, never a bare error.
	miss, rpcErr := h.handleRetrieve(ctx, json.RawMessage(`{"key":"deadbeefdeadbeefdeadbeef"}`))
	if rpcErr != nil {
		t.Fatalf("miss should not error: %v", rpcErr)
	}
	if !strings.Contains(retrievedText(t, miss), "Re-run") {
		t.Error("miss path should return a graceful re-run message")
	}
}

// TestCCRKillSwitchDetectsMissingMarker proves the kill-switch flags a marker
// whose original was never persisted — the case that forces a bypass-to-original.
func TestCCRKillSwitchDetectsMissingMarker(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "ks.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()

	h := &handler{store: db}
	fake := json.RawMessage(`{"content":[{"type":"text","text":"head [[ccr key=aaaaaaaaaaaaaaaaaaaaaaaa bytes=99]] tail"}]}`)
	if h.ccrMarkersResolve(ctx, fake) {
		t.Fatal("kill-switch must report unresolvable for an unpersisted marker")
	}
}

// TestApplyCompressionMeasuresCodeModeOutput proves the shared helper (now also
// hooked onto execute_code output) records a measurement in shadow while
// returning the output unchanged — this is what makes the dashboard populate
// for slim-surface / code-mode usage, where nothing hits the downstream seam.
func TestApplyCompressionMeasuresCodeModeOutput(t *testing.T) {
	h := &handler{compression: newCompressionPipeline()} // nil store+settings → shadow, in-memory only
	pretty, _ := json.MarshalIndent(map[string]any{
		"items": []map[string]any{{"id": 1, "name": "alpha"}, {"id": 2, "name": "beta"}, {"id": 3, "name": "gamma"}},
		"meta":  map[string]any{"total": 3, "page": 1, "note": "a code-mode print() output blob"},
	}, "", "  ")
	textJSON, _ := json.Marshal(string(pretty))
	env := json.RawMessage(`{"content":[{"type":"text","text":` + string(textJSON) + `}]}`)
	if len(env) < compressionMinBytes {
		t.Fatalf("fixture too small (%d) to exercise the pipeline", len(env))
	}

	out := h.applyCompression(context.Background(), env)
	if string(out) != string(env) {
		t.Fatal("shadow mode must return the code-mode output unchanged")
	}
	if h.ContextCostStats().Compression.Samples == 0 {
		t.Fatal("applyCompression must record a measurement for code-mode output")
	}
}

func retrievedText(t *testing.T, result json.RawMessage) string {
	t.Helper()
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(result, &out); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if len(out.Content) == 0 {
		t.Fatalf("no content in result: %s", result)
	}
	return out.Content[0].Text
}
