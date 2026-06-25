// memory_associative_test.go — coverage for the associative-recall axis
// (RelatedEntities, AR1).
package sqlite

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestRelatedEntities_AggregatesCoLinks(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"

	// 3 memories link task:T. Two of them also link person:alice,
	// one also links place:berlin, and one has no other links.
	idAB := mustWrite(t, d, "m-AB", ws)
	idAC := mustWrite(t, d, "m-AC", ws)
	idA := mustWrite(t, d, "m-A", ws)
	for _, id := range []string{idAB, idAC, idA} {
		_ = d.LinkMemoryEntity(ctx, id, store.EntityRef{Kind: "task", ID: "T"}, "")
	}
	_ = d.LinkMemoryEntity(ctx, idAB, store.EntityRef{Kind: "person", ID: "alice"}, "")
	_ = d.LinkMemoryEntity(ctx, idAC, store.EntityRef{Kind: "person", ID: "alice"}, "")
	_ = d.LinkMemoryEntity(ctx, idAC, store.EntityRef{Kind: "place", ID: "berlin"}, "")

	related, err := d.RelatedEntities(ctx,
		store.EntityRef{Kind: "task", ID: "T"},
		store.SkillScope{WorkspaceIDs: []string{ws}},
		20)
	if err != nil {
		t.Fatalf("RelatedEntities: %v", err)
	}
	if len(related) != 2 {
		t.Fatalf("expected 2 related entities, got %d: %+v", len(related), related)
	}
	// Top entry must be person:alice with shared_count = 2.
	top := related[0]
	if top.Kind != "person" || top.ID != "alice" || top.SharedCount != 2 {
		t.Fatalf("top related = %+v, want person:alice count=2", top)
	}
	// Second is place:berlin with shared_count = 1.
	second := related[1]
	if second.Kind != "place" || second.ID != "berlin" || second.SharedCount != 1 {
		t.Fatalf("second related = %+v, want place:berlin count=1", second)
	}
}

func TestRelatedEntities_ExcludesSelf(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	id := mustWrite(t, d, "m", ws)
	// Same entity linked twice with different roles — must not show as related.
	for _, role := range []string{"subject", "mentioned"} {
		_ = d.LinkMemoryEntity(ctx, id,
			store.EntityRef{Kind: "task", ID: "T", Role: role}, "")
	}
	related, _ := d.RelatedEntities(ctx,
		store.EntityRef{Kind: "task", ID: "T"},
		store.SkillScope{WorkspaceIDs: []string{ws}}, 20)
	for _, r := range related {
		if r.Kind == "task" && r.ID == "t" {
			t.Fatalf("self leaked into related: %+v", r)
		}
	}
}

func TestRelatedEntities_ScopeRespected(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsA := "ws-A"
	wsB := "ws-B"
	idA := mustWrite(t, d, "m-A", wsA)
	idB := mustWrite(t, d, "m-B", wsB)
	for _, id := range []string{idA, idB} {
		_ = d.LinkMemoryEntity(ctx, id, store.EntityRef{Kind: "task", ID: "T"}, "")
		_ = d.LinkMemoryEntity(ctx, id, store.EntityRef{Kind: "person", ID: "alice"}, "")
	}
	// Scope only wsA — alice should appear with count=1 (only from wsA).
	related, _ := d.RelatedEntities(ctx,
		store.EntityRef{Kind: "task", ID: "T"},
		store.SkillScope{WorkspaceIDs: []string{wsA}}, 20)
	if len(related) != 1 || related[0].Kind != "person" || related[0].SharedCount != 1 {
		t.Fatalf("scope filter broken: %+v", related)
	}
}

func TestRelatedEntities_ExcludesInvalidated(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	wsPtr := ws
	// fact-style memory that gets superseded — both versions link task:T
	// and person:alice, but the invalidated row should NOT contribute to
	// the co-link count.
	first := &store.MemoryEntry{
		Name: "pref", Kind: store.MemoryKindFact, Content: "v1",
		WorkspaceID: &wsPtr,
	}
	if err := d.WriteMemory(ctx, first); err != nil {
		t.Fatalf("first: %v", err)
	}
	_ = d.LinkMemoryEntity(ctx, first.ID, store.EntityRef{Kind: "task", ID: "T"}, "")
	_ = d.LinkMemoryEntity(ctx, first.ID, store.EntityRef{Kind: "person", ID: "alice"}, "")
	second := &store.MemoryEntry{
		Name: "pref", Kind: store.MemoryKindFact, Content: "v2",
		WorkspaceID: &wsPtr,
	}
	if err := d.WriteMemory(ctx, second); err != nil {
		t.Fatalf("second: %v", err)
	}
	// Second row carries no links → after invalidation of v1, task:T has
	// no live links at all, so RelatedEntities for task:T is empty.
	related, _ := d.RelatedEntities(ctx,
		store.EntityRef{Kind: "task", ID: "T"},
		store.SkillScope{WorkspaceIDs: []string{ws}}, 20)
	if len(related) != 0 {
		t.Fatalf("invalidated co-link leaked: %+v", related)
	}
}

func TestRelatedEntities_RejectsEmpty(t *testing.T) {
	d := newMemDB(t)
	if _, err := d.RelatedEntities(context.Background(),
		store.EntityRef{Kind: "", ID: "x"},
		store.SkillScope{}, 10); err == nil {
		t.Fatal("empty kind: expected error")
	}
}
