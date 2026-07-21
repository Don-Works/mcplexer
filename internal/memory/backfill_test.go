// backfill_test.go — coverage for the embeddings backfill: an existing
// corpus written before a vector provider was wired becomes fully
// embedded (and thus semantically searchable) after a backfill run.
package memory_test

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestBackfillEmbeddings_EmbedsExistingCorpus(t *testing.T) {
	ctx := context.Background()
	d, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	// Seed three memories directly via the store — no vectors (simulates a
	// corpus that predates the embedder being wired).
	contents := []string{
		"deploy region is eu-west-1 for GDPR residency",
		"payment flow retry uses idempotency keys",
		"user prefers dark mode in the dashboard",
	}
	for i, c := range contents {
		e := &store.MemoryEntry{
			Name:    string(rune('a'+i)) + "-seed",
			Kind:    store.MemoryKindNote,
			Content: c,
		}
		if err := d.WriteMemory(ctx, e); err != nil {
			t.Fatalf("seed write %d: %v", i, err)
		}
	}

	pending, total, err := d.CountMemoriesNeedingEmbedding(ctx)
	if err != nil {
		t.Fatalf("count pre: %v", err)
	}
	if pending != 3 || total != 3 {
		t.Fatalf("pre-backfill pending=%d total=%d, want 3/3", pending, total)
	}

	// Wire a vector service over the SAME db and backfill. Batch < total so
	// the multi-batch loop is exercised.
	svc := memory.NewService(d, fakeEmbedder{model: "fake-1536"}, nil)
	t.Cleanup(func() { _ = svc.Close() })

	done, err := svc.BackfillEmbeddings(ctx, 2)
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if done != 3 {
		t.Fatalf("backfilled %d rows, want 3", done)
	}

	pending, _, err = d.CountMemoriesNeedingEmbedding(ctx)
	if err != nil {
		t.Fatalf("count post: %v", err)
	}
	if pending != 0 {
		t.Fatalf("post-backfill pending=%d, want 0", pending)
	}

	st := svc.BackfillStatus(ctx)
	if !st.EmbedderActive || st.Pending != 0 || st.Embedded != 3 || st.Total != 3 {
		t.Fatalf("status=%+v, want active/0/3/3", st)
	}

	// A second backfill is a no-op (nothing pending) and never errors.
	if done, err := svc.BackfillEmbeddings(ctx, 2); err != nil || done != 0 {
		t.Fatalf("idempotent re-run: done=%d err=%v, want 0/nil", done, err)
	}
}

func TestBackfillEmbeddings_NoEmbedderIsAnError(t *testing.T) {
	ctx := context.Background()
	d, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	svc := memory.NewService(d, memory.NoopEmbedder{}, nil)
	t.Cleanup(func() { _ = svc.Close() })

	if _, err := svc.BackfillEmbeddings(ctx, 0); err == nil {
		t.Fatal("expected error when no embedder is configured")
	}
	if svc.StartBackfillAsync(ctx) {
		t.Fatal("StartBackfillAsync must no-op without a vector provider")
	}
	if st := svc.BackfillStatus(ctx); st.EmbedderActive {
		t.Fatalf("EmbedderActive should be false, got %+v", st)
	}
}

// TestSetEmbedder_HotSwapActivatesVectorPath proves the runtime swap: a
// service constructed FTS-only flips to vector-active after SetEmbedder,
// with no reconstruction.
func TestSetEmbedder_HotSwapActivatesVectorPath(t *testing.T) {
	ctx := context.Background()
	d, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	svc := memory.NewService(d, memory.NoopEmbedder{}, nil)
	t.Cleanup(func() { _ = svc.Close() })

	if svc.EmbedderActive() {
		t.Fatal("expected FTS-only at construction")
	}
	svc.SetEmbedder(fakeEmbedder{model: "fake-1536"})
	if !svc.EmbedderActive() {
		t.Fatal("expected vector-active after SetEmbedder")
	}
	// Swapping back to nil restores the FTS-only floor.
	svc.SetEmbedder(nil)
	if svc.EmbedderActive() {
		t.Fatal("expected FTS-only after SetEmbedder(nil)")
	}
}
