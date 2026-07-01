package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestCompressionLedgerRecordAndAggregate(t *testing.T) {
	db := newTestDB(t, "compression")
	ctx := context.Background()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	obs := []store.CompressionObservation{{
		Transform: "json_minify", Lossless: true, Changed: true,
		OrigBytes: 1000, WouldSaveBytes: 400, WouldSaveTokens: 114,
	}}
	for range 3 {
		if err := db.RecordCompression(ctx, "", now, obs); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	agg, err := db.CompressionAggregate(ctx, "", 30, now)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if len(agg.ByTransform) != 1 {
		t.Fatalf("want 1 transform, got %d", len(agg.ByTransform))
	}
	tr := agg.ByTransform[0]
	if tr.Transform != "json_minify" || !tr.Lossless {
		t.Errorf("unexpected transform row: %+v", tr)
	}
	if tr.Samples != 3 {
		t.Errorf("samples=%d want 3", tr.Samples)
	}
	if tr.WouldSaveTokens != 342 { // 114*3, upserted into one daily bucket
		t.Errorf("would tokens=%d want 342", tr.WouldSaveTokens)
	}
	if agg.AppliedSaveTokens != 0 {
		t.Errorf("applied should be 0 in shadow, got %d", agg.AppliedSaveTokens)
	}
	if agg.Samples != 3 {
		t.Errorf("top-level samples=%d want 3", agg.Samples)
	}
	if len(agg.Daily) != 30 {
		t.Fatalf("daily len=%d want 30", len(agg.Daily))
	}
	last := agg.Daily[len(agg.Daily)-1]
	if last.Date != "2026-07-01" || last.WouldSaveTokens != 342 {
		t.Errorf("last daily point wrong: %+v", last)
	}
}

func TestCompressionAppliedAndWorkspaceFilter(t *testing.T) {
	db := newTestDB(t, "compression2")
	ctx := context.Background()
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

	obs := []store.CompressionObservation{{
		Transform: "json_minify", Lossless: true, Changed: true, Applied: true,
		OrigBytes: 1000, WouldSaveBytes: 400, WouldSaveTokens: 114,
		AppliedSaveBytes: 400, AppliedSaveTokens: 114,
	}}
	if err := db.RecordCompression(ctx, "ws1", now, obs); err != nil {
		t.Fatal(err)
	}

	agg, err := db.CompressionAggregate(ctx, "ws1", 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if agg.AppliedSaveTokens != 114 {
		t.Errorf("applied tokens=%d want 114", agg.AppliedSaveTokens)
	}

	other, err := db.CompressionAggregate(ctx, "ws2", 7, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(other.ByTransform) != 0 {
		t.Errorf("workspace filter leaked rows: %+v", other.ByTransform)
	}
}
