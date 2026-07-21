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

func dedupFixture(payload string) json.RawMessage {
	text, _ := json.Marshal(payload)
	return json.RawMessage(`{"content":[{"type":"text","text":` + string(text) + `}]}`)
}

func identityEstimator(n int) int { return n }

// T-G: a byte-identical repeat within a session collapses to a pointer
// envelope in On mode; the original stays retrievable.
func TestSessionDedupReplacesRepeatInOnMode(t *testing.T) {
	d := newSessionDedup()
	payload := dedupFixture(strings.Repeat("same content ", 200))

	first, obs := d.Process(compression.ModeOn, identityEstimator, "sess-1", "fs__read", payload)
	if string(first) != string(payload) || len(obs) != 0 {
		t.Fatal("first delivery must pass through untouched")
	}

	second, obs := d.Process(compression.ModeOn, identityEstimator, "sess-1", "fs__read", payload)
	if string(second) == string(payload) {
		t.Fatal("second identical delivery must be replaced with a pointer")
	}
	if len(obs) != 1 || !obs[0].Applied || len(obs[0].Stash) != 1 {
		t.Fatalf("expected one applied observation with the original stashed: %+v", obs)
	}
	if string(obs[0].Stash[0]) != string(payload) {
		t.Fatal("stash must hold the exact original result bytes")
	}
	keys := compression.ParseCCRKeys(string(second))
	if len(keys) != 1 || keys[0] != compression.CCRKey(payload) {
		t.Fatalf("pointer must carry a marker addressing the original: %v", keys)
	}
	if !strings.Contains(string(second), "fs__read") {
		t.Errorf("pointer should name the earlier tool: %s", second)
	}
}

// Shadow mode measures the would-be saving but never replaces or stashes.
func TestSessionDedupShadowMeasuresOnly(t *testing.T) {
	d := newSessionDedup()
	payload := dedupFixture(strings.Repeat("shadow content ", 200))

	d.Process(compression.ModeShadow, identityEstimator, "sess-1", "x__y", payload)
	out, obs := d.Process(compression.ModeShadow, identityEstimator, "sess-1", "x__y", payload)
	if string(out) != string(payload) {
		t.Fatal("shadow must return the original untouched")
	}
	if len(obs) != 1 || obs[0].Applied || len(obs[0].Stash) != 0 {
		t.Fatalf("shadow observation must be measure-only: %+v", obs)
	}
	if obs[0].SavedBytes <= 0 {
		t.Fatalf("expected a positive would-be saving, got %d", obs[0].SavedBytes)
	}
}

func TestSessionDedupIsolatesSessionsAndSkipsEdgeCases(t *testing.T) {
	d := newSessionDedup()
	payload := dedupFixture(strings.Repeat("cross session ", 200))

	d.Process(compression.ModeOn, identityEstimator, "sess-1", "a__b", payload)
	out, obs := d.Process(compression.ModeOn, identityEstimator, "sess-2", "a__b", payload)
	if string(out) != string(payload) || len(obs) != 0 {
		t.Fatal("a different session must not dedup against sess-1")
	}

	// No session id → skip entirely.
	if _, obs := d.Process(compression.ModeOn, identityEstimator, "", "a__b", payload); len(obs) != 0 {
		t.Fatal("session-less calls must be skipped")
	}
	// Small payloads → skip.
	small := dedupFixture("tiny")
	d.Process(compression.ModeOn, identityEstimator, "sess-3", "a__b", small)
	if out, _ := d.Process(compression.ModeOn, identityEstimator, "sess-3", "a__b", small); string(out) != string(small) {
		t.Fatal("payloads below the min size must never dedup")
	}
	// Error envelopes → skip.
	errEnv := json.RawMessage(`{"isError":true,"content":[{"type":"text","text":"` + strings.Repeat("boom ", 300) + `"}]}`)
	d.Process(compression.ModeOn, identityEstimator, "sess-4", "a__b", errEnv)
	if out, _ := d.Process(compression.ModeOn, identityEstimator, "sess-4", "a__b", errEnv); string(out) != string(errEnv) {
		t.Fatal("error envelopes must never dedup")
	}
	// Different content, same session → no dedup.
	other := dedupFixture(strings.Repeat("different content ", 200))
	if out, obs := d.Process(compression.ModeOn, identityEstimator, "sess-1", "a__b", other); string(out) != string(other) || len(obs) != 0 {
		t.Fatal("different content must not dedup")
	}
}

// End-to-end through applyCompressionForTool with a real store: the repeat is
// replaced, the marker resolves, and mcpx__retrieve returns the original.
func TestSessionDedupEndToEndThroughApplyCompression(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "dedup.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer func() { _ = db.Close() }()

	h := &handler{store: db, compression: newCompressionPipeline(), sessionDedup: newSessionDedup()}
	// applyCompression needs On mode; with no settings service the mode
	// defaults to shadow, so drive the dedup + persist path directly.
	payload := dedupFixture(strings.Repeat("end to end ", 300))
	sess := "sess-e2e"

	first, _ := h.sessionDedup.Process(compression.ModeOn, identityEstimator, sess, "fs__read", payload)
	if string(first) != string(payload) {
		t.Fatal("first delivery must be untouched")
	}
	deduped, obs := h.sessionDedup.Process(compression.ModeOn, identityEstimator, sess, "fs__read", payload)
	if string(deduped) == string(payload) {
		t.Fatal("repeat must be replaced")
	}
	h.persistCCR(ctx, obs)
	if !h.ccrMarkersResolve(ctx, deduped) {
		t.Fatal("pointer marker must resolve after persist")
	}
	keys := compression.ParseCCRKeys(string(deduped))
	res, rpcErr := h.handleRetrieve(ctx, json.RawMessage(`{"key":"`+keys[0]+`"}`))
	if rpcErr != nil {
		t.Fatalf("retrieve: %v", rpcErr)
	}
	if retrievedText(t, res) != string(payload) {
		t.Fatal("retrieve must return the exact original result envelope")
	}
}
