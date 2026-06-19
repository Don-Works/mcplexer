package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestAuditRecord_ActorColumnsRoundTrip exercises the columns added in
// migration 053. Round-trips one populated row + one row with all three
// fields left empty to confirm the NOT NULL DEFAULT ” contract holds
// for legacy emit sites that haven't been updated yet.
func TestAuditRecord_ActorColumnsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	rows := []store.AuditRecord{
		{
			ID:            "rec-populated",
			Timestamp:     time.Now().UTC(),
			ToolName:      "worker_run.started",
			Status:        "ok",
			ActorKind:     "worker",
			ActorID:       "wrk-123",
			CorrelationID: "run-abc",
		},
		{
			ID:        "rec-empty",
			Timestamp: time.Now().UTC().Add(-time.Minute),
			ToolName:  "legacy.event",
			Status:    "ok",
		},
	}
	for i := range rows {
		if err := db.InsertAuditRecord(ctx, &rows[i]); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	got, _, err := db.QueryAuditRecords(ctx, store.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	var populated, empty *store.AuditRecord
	for i := range got {
		switch got[i].ID {
		case "rec-populated":
			populated = &got[i]
		case "rec-empty":
			empty = &got[i]
		}
	}
	if populated == nil || empty == nil {
		t.Fatalf("missing one or both rows: %+v", got)
	}

	if populated.ActorKind != "worker" {
		t.Errorf("ActorKind = %q, want worker", populated.ActorKind)
	}
	if populated.ActorID != "wrk-123" {
		t.Errorf("ActorID = %q, want wrk-123", populated.ActorID)
	}
	if populated.CorrelationID != "run-abc" {
		t.Errorf("CorrelationID = %q, want run-abc", populated.CorrelationID)
	}

	if empty.ActorKind != "" || empty.ActorID != "" || empty.CorrelationID != "" {
		t.Errorf("expected empty actor fields on legacy row, got %+v", empty)
	}
}
