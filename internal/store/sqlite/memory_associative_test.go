// memory_associative_test.go — coverage for the associative-recall axis
// (RelatedEntities + BuildEntityGraph, AR1 + AR3).
package sqlite

import (
	"context"
	"sort"
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

func TestBuildEntityGraph_NodesAndEdges(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	// Build a tiny graph: task:T <-> person:alice (weight 2),
	// task:T <-> place:berlin (weight 1), person:alice <-> place:berlin
	// (weight 1).
	id1 := mustWrite(t, d, "m1", ws)
	id2 := mustWrite(t, d, "m2", ws)
	id3 := mustWrite(t, d, "m3", ws)
	for _, id := range []string{id1, id2} {
		_ = d.LinkMemoryEntity(ctx, id, store.EntityRef{Kind: "task", ID: "T"}, "")
		_ = d.LinkMemoryEntity(ctx, id, store.EntityRef{Kind: "person", ID: "alice"}, "")
	}
	_ = d.LinkMemoryEntity(ctx, id3, store.EntityRef{Kind: "task", ID: "T"}, "")
	_ = d.LinkMemoryEntity(ctx, id3, store.EntityRef{Kind: "place", ID: "berlin"}, "")
	_ = d.LinkMemoryEntity(ctx, id3, store.EntityRef{Kind: "person", ID: "alice"}, "")

	g, err := d.BuildEntityGraph(ctx,
		store.SkillScope{WorkspaceIDs: []string{ws}}, 200, 0)
	if err != nil {
		t.Fatalf("BuildEntityGraph: %v", err)
	}
	if len(g.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d: %+v", len(g.Nodes), g.Nodes)
	}
	// Sort edges deterministically for assertion.
	sort.Slice(g.Edges, func(i, j int) bool {
		if g.Edges[i].Source != g.Edges[j].Source {
			return g.Edges[i].Source < g.Edges[j].Source
		}
		return g.Edges[i].Target < g.Edges[j].Target
	})
	want := []store.EntityEdge{
		{Source: "person:alice", Target: "place:berlin", Weight: 1},
		{Source: "person:alice", Target: "task:t", Weight: 3},
		{Source: "place:berlin", Target: "task:t", Weight: 1},
	}
	if len(g.Edges) != len(want) {
		t.Fatalf("edges: want %d, got %d (%+v)", len(want), len(g.Edges), g.Edges)
	}
	for i, w := range want {
		if g.Edges[i] != w {
			t.Errorf("edge[%d]=%+v want %+v", i, g.Edges[i], w)
		}
	}
	if g.Truncated {
		t.Errorf("Truncated unexpectedly true")
	}
}

func TestBuildEntityGraph_NodeCapTruncates(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	// Make 4 distinct entities; cap at 2 → top 2 by memory_count keep,
	// edges involving dropped nodes go too.
	id1 := mustWrite(t, d, "m1", ws)
	for _, e := range []store.EntityRef{
		{Kind: "task", ID: "HOT"},
		{Kind: "person", ID: "alice"},
		{Kind: "place", ID: "berlin"},
		{Kind: "org", ID: "acme"},
	} {
		_ = d.LinkMemoryEntity(ctx, id1, e, "")
	}
	// Make HOT and alice doubly-linked so they outrank the others.
	id2 := mustWrite(t, d, "m2", ws)
	_ = d.LinkMemoryEntity(ctx, id2, store.EntityRef{Kind: "task", ID: "HOT"}, "")
	_ = d.LinkMemoryEntity(ctx, id2, store.EntityRef{Kind: "person", ID: "alice"}, "")

	g, err := d.BuildEntityGraph(ctx,
		store.SkillScope{WorkspaceIDs: []string{ws}}, 2, 0)
	if err != nil {
		t.Fatalf("BuildEntityGraph: %v", err)
	}
	if len(g.Nodes) != 2 || !g.Truncated {
		t.Fatalf("cap broken: nodes=%d truncated=%v", len(g.Nodes), g.Truncated)
	}
	for _, edge := range g.Edges {
		if edge.Source != "person:alice" && edge.Source != "task:hot" {
			t.Fatalf("edge touches dropped node: %+v", edge)
		}
		if edge.Target != "person:alice" && edge.Target != "task:hot" {
			t.Fatalf("edge touches dropped node: %+v", edge)
		}
	}
}

func TestBuildEntityGraph_MinWeight(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	id1 := mustWrite(t, d, "m1", ws)
	_ = d.LinkMemoryEntity(ctx, id1, store.EntityRef{Kind: "task", ID: "T"}, "")
	_ = d.LinkMemoryEntity(ctx, id1, store.EntityRef{Kind: "person", ID: "alice"}, "")
	// Single co-link → weight 1. minWeight=2 should prune the edge but
	// keep both nodes.
	g, err := d.BuildEntityGraph(ctx,
		store.SkillScope{WorkspaceIDs: []string{ws}}, 200, 2)
	if err != nil {
		t.Fatalf("BuildEntityGraph: %v", err)
	}
	if len(g.Nodes) != 2 {
		t.Fatalf("want 2 nodes, got %d", len(g.Nodes))
	}
	if len(g.Edges) != 0 {
		t.Fatalf("minWeight=2 should prune weight=1 edge, got %+v", g.Edges)
	}
}

// TestBuildEntityGraph_DenseStoreBoundedToNodeSet exercises the
// json_each-bounded edge join against a denser store: many entities, each
// co-linked, with a hard nodeCap that drops the long tail. The edge query
// must return ONLY edges whose BOTH endpoints are in the retained node set
// — i.e. the json_each predicate (not a Go-side post-filter) does the
// bounding. A dropped high-degree node must not appear in any edge.
func TestBuildEntityGraph_DenseStoreBoundedToNodeSet(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-dense"

	// Two HOT entities co-link in many memories so they outrank everything.
	hotA := store.EntityRef{Kind: "task", ID: "HOTA"}
	hotB := store.EntityRef{Kind: "person", ID: "hotb"}
	for i := 0; i < 6; i++ {
		id := mustWrite(t, d, "hot-"+string(rune('a'+i)), ws)
		_ = d.LinkMemoryEntity(ctx, id, hotA, "")
		_ = d.LinkMemoryEntity(ctx, id, hotB, "")
	}
	// A spread of cold entities, each co-linked with hotA in one memory.
	// These pull the cold:* nodes below the cap and create edges that touch
	// a retained node (hotA) but a DROPPED node (cold:*) — those edges must
	// be excluded.
	cold := []store.EntityRef{
		{Kind: "place", ID: "c1"}, {Kind: "place", ID: "c2"},
		{Kind: "org", ID: "c3"}, {Kind: "org", ID: "c4"},
		{Kind: "skill", ID: "c5"},
	}
	for _, c := range cold {
		id := mustWrite(t, d, "cold-"+c.ID, ws)
		_ = d.LinkMemoryEntity(ctx, id, hotA, "")
		_ = d.LinkMemoryEntity(ctx, id, c, "")
	}

	// Cap at 2 → only the two HOT nodes survive.
	g, err := d.BuildEntityGraph(ctx,
		store.SkillScope{WorkspaceIDs: []string{ws}}, 2, 0)
	if err != nil {
		t.Fatalf("BuildEntityGraph: %v", err)
	}
	if len(g.Nodes) != 2 || !g.Truncated {
		t.Fatalf("cap: nodes=%d truncated=%v, want 2/true", len(g.Nodes), g.Truncated)
	}
	keep := map[string]struct{}{}
	for _, n := range g.Nodes {
		keep[n.Kind+":"+n.ID] = struct{}{}
	}
	if len(g.Edges) != 1 {
		t.Fatalf("want exactly 1 edge (hotA<->hotB), got %d: %+v", len(g.Edges), g.Edges)
	}
	for _, e := range g.Edges {
		if _, ok := keep[e.Source]; !ok {
			t.Fatalf("edge source %q not in retained node set: %+v", e.Source, e)
		}
		if _, ok := keep[e.Target]; !ok {
			t.Fatalf("edge target %q not in retained node set: %+v", e.Target, e)
		}
	}
	// The single retained edge weight = 6 (the six hot-* co-link memories).
	if g.Edges[0].Weight != 6 {
		t.Fatalf("retained edge weight = %d, want 6", g.Edges[0].Weight)
	}
}

func TestBuildEntityGraph_EmptyScope(t *testing.T) {
	d := newMemDB(t)
	g, err := d.BuildEntityGraph(context.Background(),
		store.SkillScope{WorkspaceIDs: []string{"ws-none"}}, 200, 0)
	if err != nil {
		t.Fatalf("BuildEntityGraph: %v", err)
	}
	if len(g.Nodes) != 0 || len(g.Edges) != 0 {
		t.Fatalf("empty scope should produce empty graph, got %+v", g)
	}
}
