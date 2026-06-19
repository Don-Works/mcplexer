package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestPruneAuditRecords drives the retention DELETE under every shape
// the nightly job actually encounters: empty table (idle daemon),
// all-new (nothing to prune), all-old (full sweep), and a mix
// (boundary stays, older rows go).
func TestPruneAuditRecords(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-90 * 24 * time.Hour)

	tests := []struct {
		name        string
		createdAts  []time.Time
		before      time.Time
		wantDeleted int64
		wantTotal   int
	}{
		{
			name:        "empty",
			createdAts:  nil,
			before:      cutoff,
			wantDeleted: 0,
			wantTotal:   0,
		},
		{
			name: "all newer than cutoff",
			createdAts: []time.Time{
				now.Add(-1 * 24 * time.Hour),
				now.Add(-30 * 24 * time.Hour),
				now.Add(-89 * 24 * time.Hour),
			},
			before:      cutoff,
			wantDeleted: 0,
			wantTotal:   3,
		},
		{
			name: "all older than cutoff",
			createdAts: []time.Time{
				now.Add(-100 * 24 * time.Hour),
				now.Add(-180 * 24 * time.Hour),
				now.Add(-365 * 24 * time.Hour),
			},
			before:      cutoff,
			wantDeleted: 3,
			wantTotal:   0,
		},
		{
			name: "mixed — boundary stays, older rows go",
			createdAts: []time.Time{
				now.Add(-1 * 24 * time.Hour),    // keep
				now.Add(-89 * 24 * time.Hour),   // keep (newer than cutoff)
				now.Add(-91 * 24 * time.Hour),   // delete
				now.Add(-200 * 24 * time.Hour),  // delete
				now.Add(-1000 * 24 * time.Hour), // delete
			},
			before:      cutoff,
			wantDeleted: 3,
			wantTotal:   2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			for i, ts := range tt.createdAts {
				r := &store.AuditRecord{
					Timestamp: ts,
					CreatedAt: ts,
					ToolName:  "test__tool",
					Status:    "success",
					LatencyMs: 10 + i,
				}
				if err := db.InsertAuditRecord(ctx, r); err != nil {
					t.Fatalf("insert row %d: %v", i, err)
				}
			}

			got, err := db.PruneAuditRecords(ctx, tt.before)
			if err != nil {
				t.Fatalf("prune: %v", err)
			}
			if got != tt.wantDeleted {
				t.Fatalf("deleted = %d, want %d", got, tt.wantDeleted)
			}

			_, total, err := db.QueryAuditRecords(ctx, store.AuditFilter{Limit: 100})
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			if total != tt.wantTotal {
				t.Fatalf("post-prune total = %d, want %d", total, tt.wantTotal)
			}
		})
	}
}

// TestPruneAuditRecordsIdempotent calls Prune twice and checks the
// second call deletes 0 rows. Real-world the nightly job runs every
// 24h on an already-pruned table — that no-op path must be free of
// side effects.
func TestPruneAuditRecordsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	old := time.Now().UTC().Add(-200 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		r := &store.AuditRecord{
			Timestamp: old,
			CreatedAt: old,
			ToolName:  "test__tool",
			Status:    "success",
		}
		if err := db.InsertAuditRecord(ctx, r); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	cutoff := time.Now().UTC().Add(-90 * 24 * time.Hour)
	first, err := db.PruneAuditRecords(ctx, cutoff)
	if err != nil {
		t.Fatalf("first prune: %v", err)
	}
	if first != 5 {
		t.Fatalf("first prune = %d, want 5", first)
	}

	second, err := db.PruneAuditRecords(ctx, cutoff)
	if err != nil {
		t.Fatalf("second prune: %v", err)
	}
	if second != 0 {
		t.Fatalf("second prune = %d, want 0", second)
	}
}
