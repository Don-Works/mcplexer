package gateway

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/codemode"
	"github.com/don-works/mcplexer/internal/compression"
	"github.com/don-works/mcplexer/internal/store"
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

func TestCodeModeBuiltinToolsIncludeRetrieve(t *testing.T) {
	h := &handler{}
	for _, tool := range h.codeModeBuiltinTools() {
		if tool.Name == "mcpx__retrieve" {
			return
		}
	}
	t.Fatal("mcpx__retrieve missing from execute_code sandbox tool catalog")
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

// TestCCRTouchOnReadExtendsTTL (F5): each ccrGet hit renews the entry's TTL,
// so markers in long or resumed sessions keep resolving as long as they are
// actually being used.
func TestCCRTouchOnReadExtendsTTL(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "ttl.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	h := &handler{store: db}

	original := []byte("touch me")
	key := compression.CCRKey(original)
	// Seed with a nearly-expired TTL, as if the entry were 118 minutes old.
	soon := time.Now().Add(2 * time.Minute)
	if err := db.SetCodeState(ctx, &store.CodeStateEntry{
		WorkspaceID: ccrWorkspace, Key: key,
		ValueJSON: original, TTLExpiresAt: &soon,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if _, ok := h.ccrGet(ctx, key); !ok {
		t.Fatal("expected a hit on the unexpired entry")
	}
	e, err := db.GetCodeState(ctx, ccrWorkspace, key)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if e.TTLExpiresAt == nil || time.Until(*e.TTLExpiresAt) < 100*time.Minute {
		t.Fatalf("TTL was not renewed on read: expires %v", e.TTLExpiresAt)
	}
}

// TestStructuredDedupHarnessGate (F3): structured_dedup only runs for clients
// known to forward structuredContent into the model context; anything
// unrecognized keeps the text copy.
func TestStructuredDedupHarnessGate(t *testing.T) {
	for client, forwards := range map[string]bool{
		"claude-code":          true,
		"Claude Code (cli)":    true,
		"claude-ai-web":        true,
		"chatgpt-desktop":      true,
		"cursor":               false,
		"windsurf":             false,
		"grok-cli":             false,
		"gemini-cli":           false,
		"pi-coding-agent":      false,
		"":                     false,
		"some-unknown-harness": false,
	} {
		if got := clientForwardsStructuredContent(client); got != forwards {
			t.Errorf("clientForwardsStructuredContent(%q) = %v, want %v", client, got, forwards)
		}
	}
}

// TestStashOverflowOutputRoundTrip (T-F): a capped execute_code print stream
// is stashed in CCR, a marker is appended to the output, and mcpx__retrieve
// returns the full original bytes.
func TestStashOverflowOutputRoundTrip(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "ovf.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()
	h := &handler{store: db}

	displayed := strings.Repeat("visible ", 100)
	overflow := strings.Repeat("hidden ", 300)
	result := &codemode.ExecutionResult{
		Output:                 displayed + "\n[output truncated]",
		OutputTruncated:        true,
		OutputRaw:              displayed,
		OutputOverflow:         []byte(overflow),
		OutputOverflowComplete: true,
	}
	h.stashOverflowOutput(ctx, result)

	keys := compression.ParseCCRKeys(result.Output)
	if len(keys) != 1 {
		t.Fatalf("expected exactly 1 overflow marker in output, got %d:\n%s", len(keys), result.Output)
	}
	res, rpcErr := h.handleRetrieve(ctx, json.RawMessage(`{"key":"`+keys[0]+`"}`))
	if rpcErr != nil {
		t.Fatalf("retrieve error: %v", rpcErr)
	}
	if retrievedText(t, res) != displayed+overflow {
		t.Fatal("retrieved bytes must reconstruct the full print stream")
	}
}

// TestStashOverflowOutputNoMarkerWithoutStore: when the stash cannot be
// persisted, NO marker may be emitted — a dangling marker is worse than a
// plain truncation notice.
func TestStashOverflowOutputNoMarkerWithoutStore(t *testing.T) {
	h := &handler{} // nil store → ccrPut fails
	result := &codemode.ExecutionResult{
		Output:          "x\n[output truncated]",
		OutputTruncated: true,
		OutputRaw:       "x",
		OutputOverflow:  []byte("lost"),
	}
	h.stashOverflowOutput(context.Background(), result)
	if len(compression.ParseCCRKeys(result.Output)) != 0 {
		t.Fatalf("marker emitted without a persisted stash:\n%s", result.Output)
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
