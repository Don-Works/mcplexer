// memory_conflicts_test.go — coverage for the memory conflict queue
// (record → dedupe → list → resolve).
package sqlite

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestMemoryConflicts_RecordListResolve(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)

	if err := d.RecordMemoryConflicts(ctx, []store.MemoryConflict{
		{MemoryID: "new1", MemoryName: "n1", CandidateID: "old1", CandidateName: "o1",
			CandidatePreview: "prev", Kind: "duplicate", Reason: "near-identical"},
		{MemoryID: "new1", MemoryName: "n1", CandidateID: "old2", CandidateName: "o2",
			Kind: "related", Reason: "semantic"},
	}); err != nil {
		t.Fatalf("record: %v", err)
	}

	open, err := d.ListOpenMemoryConflicts(ctx, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(open) != 2 {
		t.Fatalf("want 2 open conflicts, got %d", len(open))
	}
	// IDs are generated, names/previews preserved.
	for _, c := range open {
		if c.ID == "" || c.MemoryID != "new1" || c.CreatedAt.IsZero() {
			t.Fatalf("malformed conflict row: %+v", c)
		}
	}

	// Idempotent: re-recording an already-open pair is ignored (partial
	// unique index), so the count stays at 2.
	if err := d.RecordMemoryConflicts(ctx, []store.MemoryConflict{
		{MemoryID: "new1", CandidateID: "old1", Kind: "duplicate"},
	}); err != nil {
		t.Fatalf("re-record: %v", err)
	}
	open, _ = d.ListOpenMemoryConflicts(ctx, 10)
	if len(open) != 2 {
		t.Fatalf("dedupe broken: want 2, got %d", len(open))
	}

	// Resolve one → it drops out of the open list.
	if err := d.ResolveMemoryConflict(ctx, open[0].ID, "superseded"); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	open, _ = d.ListOpenMemoryConflicts(ctx, 10)
	if len(open) != 1 {
		t.Fatalf("after resolve want 1 open, got %d", len(open))
	}

	// After resolving, the same pair can be recorded afresh (the partial
	// unique index only constrains OPEN rows).
	if err := d.RecordMemoryConflicts(ctx, []store.MemoryConflict{
		{MemoryID: "new1", CandidateID: "old1", Kind: "duplicate"},
	}); err != nil {
		t.Fatalf("re-record after resolve: %v", err)
	}
	open, _ = d.ListOpenMemoryConflicts(ctx, 10)
	if len(open) != 2 {
		t.Fatalf("re-open after resolve: want 2, got %d", len(open))
	}
}
