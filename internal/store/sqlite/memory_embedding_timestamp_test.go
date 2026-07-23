// memory_embedding_timestamp_test.go — regression coverage for the
// invariant that UpsertMemoryEmbedding never mutates memories.updated_at.
//
// updated_at is the sole input to the recall recency signal
// (internal/memory/rank.go), to the freshness/decay buckets in
// memory_stats.go, and to the ORDER BY of ListMemories. An embedding is
// derived data: writing one is not an edit of the memory. When the upsert
// stamped updated_at = now(), a re-embed pass over an existing corpus
// (internal/memory/backfill.go — model change, version bump, repair)
// rewrote the apparent age of every row it touched and flattened the
// recency signal corpus-wide.
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestUpsertMemoryEmbeddingPreservesUpdatedAt asserts updated_at survives
// an embedding upsert byte-for-byte across the shapes production hits:
// first embed, re-embed under a new model, and a version bump.
func TestUpsertMemoryEmbeddingPreservesUpdatedAt(t *testing.T) {
	ctx := context.Background()
	backdate := time.Now().UTC().AddDate(0, 0, -90).Truncate(time.Second)

	type upsert struct {
		model   string
		version int
		val     float32
	}
	cases := []struct {
		name        string
		upserts     []upsert
		wantModel   string
		wantVersion int
	}{
		{
			name:        "first embed",
			upserts:     []upsert{{model: "embed-a", version: 1, val: 0.1}},
			wantModel:   "embed-a",
			wantVersion: 1,
		},
		{
			name: "re-embed same model (backfill repair)",
			upserts: []upsert{
				{model: "embed-a", version: 1, val: 0.1},
				{model: "embed-a", version: 1, val: 0.2},
			},
			wantModel:   "embed-a",
			wantVersion: 1,
		},
		{
			name: "model change",
			upserts: []upsert{
				{model: "embed-a", version: 1, val: 0.1},
				{model: "embed-b", version: 1, val: 0.3},
			},
			wantModel:   "embed-b",
			wantVersion: 1,
		},
		{
			name: "version bump",
			upserts: []upsert{
				{model: "embed-a", version: 1, val: 0.1},
				{model: "embed-a", version: 2, val: 0.4},
			},
			wantModel:   "embed-a",
			wantVersion: 2,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newMemDB(t)
			e := &store.MemoryEntry{Name: "aged", Content: "an old decision"}
			if err := d.WriteMemory(ctx, e); err != nil {
				t.Fatalf("WriteMemory: %v", err)
			}
			backdateUpdatedAt(t, d, e.ID, backdate)

			for i, u := range tc.upserts {
				err := d.UpsertMemoryEmbedding(
					ctx, e.ID, u.model, u.version, makeVec(memoryVecDim, u.val))
				if err != nil {
					t.Fatalf("upsert %d: %v", i, err)
				}
			}

			got, err := d.GetMemory(ctx, e.ID)
			if err != nil {
				t.Fatalf("GetMemory: %v", err)
			}
			if !got.UpdatedAt.Equal(backdate) {
				t.Fatalf("updated_at mutated by embedding upsert: got %s, want %s",
					got.UpdatedAt, backdate)
			}
			if got.EmbedModel != tc.wantModel {
				t.Fatalf("embed_model: got %q, want %q", got.EmbedModel, tc.wantModel)
			}
			if got.EmbedVersion != tc.wantVersion {
				t.Fatalf("embed_version: got %d, want %d", got.EmbedVersion, tc.wantVersion)
			}
		})
	}
}

// TestUpsertMemoryEmbeddingKeepsMemoryOrdering pins the user-visible
// consequence: a backfill that embeds an OLD memory must not float it to
// the top of ListMemories (ORDER BY updated_at DESC) ahead of genuinely
// newer, still-unembedded rows.
func TestUpsertMemoryEmbeddingKeepsMemoryOrdering(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	now := time.Now().UTC().Truncate(time.Second)

	rows := []struct {
		name string
		age  time.Duration
	}{
		{name: "newest", age: 1 * time.Hour},
		{name: "middle", age: 48 * time.Hour},
		{name: "oldest", age: 240 * time.Hour},
	}
	ids := make(map[string]string, len(rows))
	for _, r := range rows {
		e := &store.MemoryEntry{Name: r.name, Content: "content for " + r.name}
		if err := d.WriteMemory(ctx, e); err != nil {
			t.Fatalf("write %s: %v", r.name, err)
		}
		backdateUpdatedAt(t, d, e.ID, now.Add(-r.age))
		ids[r.name] = e.ID
	}

	// Backfill order is oldest-first (ListMemoriesNeedingEmbedding), so
	// embed in exactly that order.
	for i, name := range []string{"oldest", "middle", "newest"} {
		err := d.UpsertMemoryEmbedding(
			ctx, ids[name], "embed-a", 1, makeVec(memoryVecDim, float32(i+1)/10))
		if err != nil {
			t.Fatalf("embed %s: %v", name, err)
		}
	}

	got, err := d.ListMemories(ctx, store.MemoryFilter{
		Scope: store.SkillScope{IncludeAll: true},
	})
	if err != nil {
		t.Fatalf("ListMemories: %v", err)
	}
	want := []string{"newest", "middle", "oldest"}
	if len(got) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].Name != w {
			t.Fatalf("ordering disturbed by embedding: got %s, want %s",
				namesOf(got), want)
		}
	}
}

// TestUpsertMemoryEmbeddingRejectsUnknownMemory keeps the not-found
// contract intact now that the UPDATE no longer carries a timestamp arg —
// checkRowsAffected still has to see zero rows for a missing id.
func TestUpsertMemoryEmbeddingRejectsUnknownMemory(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	err := d.UpsertMemoryEmbedding(
		ctx, "01NOPE", "embed-a", 1, makeVec(memoryVecDim, 0.5))
	if err == nil {
		t.Fatal("expected an error for an unknown memory id")
	}
}
