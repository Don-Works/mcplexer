// memory_entities_test.go — coverage for migration 076's entity-link
// surface: Link/Unlink/ListMemoryEntities/ListEntities and the AND/OR
// composition in MemoryFilter.{Entities,EntitiesAny}.
package sqlite

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// helper — write one memory + return its id.
func mustWrite(t *testing.T, d *DB, name, ws string) string {
	t.Helper()
	e := &store.MemoryEntry{Name: name, Content: name + "-body"}
	if ws != "" {
		e.WorkspaceID = &ws
	}
	if err := d.WriteMemory(context.Background(), e); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return e.ID
}

func TestLinkMemoryEntityRoundtrip(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	id := mustWrite(t, d, "n1", "ws-1")

	ref := store.EntityRef{Kind: "task", ID: "01KSG-T"}
	if err := d.LinkMemoryEntity(ctx, id, ref, "sess-A"); err != nil {
		t.Fatalf("link: %v", err)
	}
	rows, err := d.ListMemoryEntities(ctx, id)
	if err != nil {
		t.Fatalf("list links: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 link, got %d", len(rows))
	}
	got := rows[0]
	if got.EntityKind != "task" || got.EntityID != "01ksg-t" {
		t.Fatalf("normalisation: kind=%q id=%q (want lower-cased)",
			got.EntityKind, got.EntityID)
	}
	if got.Role != "subject" {
		t.Fatalf("default role = %q, want 'subject'", got.Role)
	}
	if got.CreatedBy != "sess-A" {
		t.Fatalf("createdBy = %q", got.CreatedBy)
	}
}

func TestLinkMemoryEntityIdempotent(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	id := mustWrite(t, d, "n1", "ws-1")
	ref := store.EntityRef{Kind: "task", ID: "T1"}

	for i := 0; i < 3; i++ {
		if err := d.LinkMemoryEntity(ctx, id, ref, ""); err != nil {
			t.Fatalf("link %d: %v", i, err)
		}
	}
	rows, _ := d.ListMemoryEntities(ctx, id)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row (idempotent), got %d", len(rows))
	}
}

func TestLinkMemoryEntityRejectsEmpty(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	id := mustWrite(t, d, "n1", "")
	if err := d.LinkMemoryEntity(ctx, id, store.EntityRef{Kind: "", ID: "x"}, ""); err == nil {
		t.Fatal("empty kind: expected error")
	}
	if err := d.LinkMemoryEntity(ctx, id, store.EntityRef{Kind: "task", ID: ""}, ""); err == nil {
		t.Fatal("empty id: expected error")
	}
}

func TestUnlinkMemoryEntityRoleSpecific(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	id := mustWrite(t, d, "n1", "ws-1")
	// Two links to the same entity with different roles.
	for _, role := range []string{"subject", "mentioned"} {
		_ = d.LinkMemoryEntity(ctx, id,
			store.EntityRef{Kind: "person", ID: "alice@x", Role: role}, "")
	}
	rows, _ := d.ListMemoryEntities(ctx, id)
	if len(rows) != 2 {
		t.Fatalf("setup: expected 2 rows, got %d", len(rows))
	}
	// Drop just the "subject" role.
	if err := d.UnlinkMemoryEntity(ctx, id,
		store.EntityRef{Kind: "person", ID: "alice@x", Role: "subject"}); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	rows, _ = d.ListMemoryEntities(ctx, id)
	if len(rows) != 1 || rows[0].Role != "mentioned" {
		t.Fatalf("after role-specific unlink, want 1 'mentioned', got %+v", rows)
	}
}

func TestUnlinkMemoryEntityAnyRole(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	id := mustWrite(t, d, "n1", "ws-1")
	for _, role := range []string{"subject", "mentioned", "derived_from"} {
		_ = d.LinkMemoryEntity(ctx, id,
			store.EntityRef{Kind: "task", ID: "T1", Role: role}, "")
	}
	// Empty role on unlink removes every flavour for the triple.
	if err := d.UnlinkMemoryEntity(ctx, id,
		store.EntityRef{Kind: "task", ID: "T1"}); err != nil {
		t.Fatalf("unlink any: %v", err)
	}
	rows, _ := d.ListMemoryEntities(ctx, id)
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows after broad unlink, got %d", len(rows))
	}
}

func TestRecallFilterByEntityAND(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	idAB := mustWrite(t, d, "n-AB", ws) // about task:T + person:alice
	idA := mustWrite(t, d, "n-A", ws)   // about task:T only
	_ = mustWrite(t, d, "n-none", ws)   // no links

	_ = d.LinkMemoryEntity(ctx, idAB, store.EntityRef{Kind: "task", ID: "T"}, "")
	_ = d.LinkMemoryEntity(ctx, idAB,
		store.EntityRef{Kind: "person", ID: "alice@x"}, "")
	_ = d.LinkMemoryEntity(ctx, idA, store.EntityRef{Kind: "task", ID: "T"}, "")

	filter := store.MemoryFilter{
		Scope: store.SkillScope{WorkspaceIDs: []string{ws}},
		Entities: []store.EntityRef{
			{Kind: "task", ID: "T"},
			{Kind: "person", ID: "alice@x"},
		},
	}
	hits, err := d.ListMemories(ctx, filter)
	if err != nil {
		t.Fatalf("list AND: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != idAB {
		t.Fatalf("AND filter: want only n-AB (%s), got %d rows: %+v",
			idAB, len(hits), hits)
	}
}

func TestRecallFilterByEntityOR(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	idA := mustWrite(t, d, "n-A", ws)
	idB := mustWrite(t, d, "n-B", ws)
	_ = mustWrite(t, d, "n-none", ws)

	_ = d.LinkMemoryEntity(ctx, idA, store.EntityRef{Kind: "task", ID: "T1"}, "")
	_ = d.LinkMemoryEntity(ctx, idB, store.EntityRef{Kind: "task", ID: "T2"}, "")

	filter := store.MemoryFilter{
		Scope: store.SkillScope{WorkspaceIDs: []string{ws}},
		EntitiesAny: []store.EntityRef{
			{Kind: "task", ID: "T1"},
			{Kind: "task", ID: "T2"},
		},
	}
	hits, err := d.ListMemories(ctx, filter)
	if err != nil {
		t.Fatalf("list OR: %v", err)
	}
	if len(hits) != 2 {
		t.Fatalf("OR filter: want 2 (n-A, n-B), got %d", len(hits))
	}
}

func TestRecallFilterByEntityRoleScoped(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	idSubj := mustWrite(t, d, "n-subject", ws)
	idMent := mustWrite(t, d, "n-mentioned", ws)
	_ = d.LinkMemoryEntity(ctx, idSubj,
		store.EntityRef{Kind: "task", ID: "T", Role: "subject"}, "")
	_ = d.LinkMemoryEntity(ctx, idMent,
		store.EntityRef{Kind: "task", ID: "T", Role: "mentioned"}, "")

	// Empty role on filter = any role.
	hits, _ := d.ListMemories(ctx, store.MemoryFilter{
		Scope:    store.SkillScope{WorkspaceIDs: []string{ws}},
		Entities: []store.EntityRef{{Kind: "task", ID: "T"}},
	})
	if len(hits) != 2 {
		t.Fatalf("role-blind filter: want 2, got %d", len(hits))
	}
	// Role=subject narrows to just the one.
	hits, _ = d.ListMemories(ctx, store.MemoryFilter{
		Scope: store.SkillScope{WorkspaceIDs: []string{ws}},
		Entities: []store.EntityRef{
			{Kind: "task", ID: "T", Role: "subject"},
		},
	})
	if len(hits) != 1 || hits[0].ID != idSubj {
		t.Fatalf("role=subject filter: want only n-subject, got %+v", hits)
	}
}

func TestListEntitiesAggregation(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	// 3 memories about task:HOT, 1 about task:COLD, 2 about person:alice.
	for i, name := range []string{"a1", "a2", "a3"} {
		id := mustWrite(t, d, name, ws)
		_ = d.LinkMemoryEntity(ctx, id, store.EntityRef{Kind: "task", ID: "HOT"}, "")
		if i == 0 {
			_ = d.LinkMemoryEntity(ctx, id,
				store.EntityRef{Kind: "person", ID: "alice"}, "")
		}
		if i == 1 {
			_ = d.LinkMemoryEntity(ctx, id,
				store.EntityRef{Kind: "person", ID: "alice"}, "")
		}
	}
	cold := mustWrite(t, d, "cold-mem", ws)
	_ = d.LinkMemoryEntity(ctx, cold, store.EntityRef{Kind: "task", ID: "COLD"}, "")

	entities, err := d.ListEntities(ctx, store.EntityFilter{
		Scope: store.SkillScope{WorkspaceIDs: []string{ws}},
	})
	if err != nil {
		t.Fatalf("list entities: %v", err)
	}
	if len(entities) < 3 {
		t.Fatalf("expected 3+ entities, got %d", len(entities))
	}
	// First entry must be task:hot with count 3 (sorted DESC).
	top := entities[0]
	if top.Kind != "task" || top.ID != "hot" || top.MemoryCount != 3 {
		t.Fatalf("top entity = %+v, want task:hot count=3", top)
	}
}

func TestListEntitiesExcludesInvalidated(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	wsPtr := ws
	// fact-style memory; second write invalidates the first.
	first := &store.MemoryEntry{
		Name: "pref", Kind: store.MemoryKindFact, Content: "v1",
		WorkspaceID: &wsPtr,
	}
	if err := d.WriteMemory(ctx, first); err != nil {
		t.Fatalf("first: %v", err)
	}
	_ = d.LinkMemoryEntity(ctx, first.ID,
		store.EntityRef{Kind: "task", ID: "GONE"}, "")
	second := &store.MemoryEntry{
		Name: "pref", Kind: store.MemoryKindFact, Content: "v2",
		WorkspaceID: &wsPtr,
	}
	if err := d.WriteMemory(ctx, second); err != nil {
		t.Fatalf("second: %v", err)
	}
	// The "GONE" entity is only linked to the invalidated row, so it
	// must NOT surface in ListEntities (we exclude t_valid_end IS NOT NULL).
	entities, err := d.ListEntities(ctx, store.EntityFilter{
		Scope: store.SkillScope{WorkspaceIDs: []string{ws}},
	})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, e := range entities {
		if e.Kind == "task" && e.ID == "gone" {
			t.Fatalf("invalidated memory's entity leaked: %+v", entities)
		}
	}
}

func TestListEntitiesKindFilter(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	id := mustWrite(t, d, "m1", ws)
	_ = d.LinkMemoryEntity(ctx, id, store.EntityRef{Kind: "task", ID: "T1"}, "")
	_ = d.LinkMemoryEntity(ctx, id,
		store.EntityRef{Kind: "person", ID: "alice"}, "")

	tasks, _ := d.ListEntities(ctx, store.EntityFilter{
		Scope: store.SkillScope{WorkspaceIDs: []string{ws}},
		Kind:  "task",
	})
	for _, e := range tasks {
		if e.Kind != "task" {
			t.Fatalf("kind filter leaked %s", e.Kind)
		}
	}
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task entity, got %d", len(tasks))
	}
}

func TestLinkCascadeOnMemoryDelete(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	id := mustWrite(t, d, "doomed", ws)
	_ = d.LinkMemoryEntity(ctx, id, store.EntityRef{Kind: "task", ID: "T"}, "")
	// Hard-delete the row (the FK CASCADE only fires on real DELETE).
	if _, err := d.q.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	rows, _ := d.ListMemoryEntities(ctx, id)
	if len(rows) != 0 {
		t.Fatalf("FK CASCADE failed: %d link rows remain", len(rows))
	}
}

// --- cross-workspace entity recall (EntityDrivenIgnoresScope) ---
//
// These tests cover the semantic-vs-episodic split: facts about a globally-
// identifiable entity (person, task, skill, …) should follow the entity,
// not the workspace where they were first encoded. The flag is the policy
// hook; eligibility is enforced by store.EntityRecallCanEscapeScope on the
// handler side, then the SQL layer just honours the flag.

// Memory saved in workspace-A about a person — queried from workspace-B
// with the flag set — MUST surface. This is the "Elliot has blue eyes"
// worked example from the design doc.
func TestRecallEntityDrivenIgnoresScopeFindsCrossWorkspace(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsA, wsB := "ws-A", "ws-B"
	idAboutElliot := mustWrite(t, d, "elliot-eyes", wsA)
	_ = d.LinkMemoryEntity(ctx, idAboutElliot,
		store.EntityRef{Kind: "person", ID: "bob@x"}, "")

	// A red-herring memory in wsB about a different person — to confirm
	// the entity filter still narrows.
	idAboutAlice := mustWrite(t, d, "alice-note", wsB)
	_ = d.LinkMemoryEntity(ctx, idAboutAlice,
		store.EntityRef{Kind: "person", ID: "alice@x"}, "")

	// Session is in wsB. Without the flag → wsA memory is invisible.
	filterScoped := store.MemoryFilter{
		Scope:    store.SkillScope{WorkspaceIDs: []string{wsB}},
		Entities: []store.EntityRef{{Kind: "person", ID: "bob@x"}},
	}
	hitsScoped, err := d.ListMemories(ctx, filterScoped)
	if err != nil {
		t.Fatalf("scoped list: %v", err)
	}
	if len(hitsScoped) != 0 {
		t.Fatalf("baseline: expected 0 hits (wsA hidden from wsB), got %d", len(hitsScoped))
	}

	// Flag set → wsA memory surfaces from wsB.
	filterEscape := store.MemoryFilter{
		Scope:                    store.SkillScope{WorkspaceIDs: []string{wsB}},
		Entities:                 []store.EntityRef{{Kind: "person", ID: "bob@x"}},
		EntityDrivenIgnoresScope: true,
	}
	hitsEscape, err := d.ListMemories(ctx, filterEscape)
	if err != nil {
		t.Fatalf("escaped list: %v", err)
	}
	if len(hitsEscape) != 1 || hitsEscape[0].ID != idAboutElliot {
		t.Fatalf("escape: want only %s, got %d rows: %+v",
			idAboutElliot, len(hitsEscape), hitsEscape)
	}

	// The flag should NOT widen results when the entity filter doesn't
	// match — Alice's memory must NOT leak into an Elliot query.
	for _, h := range hitsEscape {
		if h.ID == idAboutAlice {
			t.Fatalf("entity narrowing broke: alice memory leaked into elliot query")
		}
	}
}

// Defence-in-depth: the flag is a no-op when no entity filter is set.
// Without entity narrowing, dropping the scope clause would expose the
// entire global pool — the SQL layer must refuse to honour the flag in
// that case.
func TestRecallEntityDrivenIgnoresScopeNoOpWithoutEntityFilter(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsA, wsB := "ws-A", "ws-B"
	_ = mustWrite(t, d, "in-A", wsA)
	idB := mustWrite(t, d, "in-B", wsB)

	// Flag set, but no entity refs — must behave as a plain workspace-B
	// query and NOT leak the workspace-A memory.
	filter := store.MemoryFilter{
		Scope:                    store.SkillScope{WorkspaceIDs: []string{wsB}},
		EntityDrivenIgnoresScope: true,
	}
	hits, err := d.ListMemories(ctx, filter)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != idB {
		t.Fatalf("flag-without-entities: expected 1 hit (%s), got %d: %+v",
			idB, len(hits), hits)
	}
}

// Multi-link memory: a memory linked to BOTH a global and a local entity
// should still surface via the global side when the handler decides it
// can escape. The local link is a passenger, not a veto — handler-side
// policy controls whether the flag gets set in the first place.
func TestRecallEntityDrivenIgnoresScopeMultiLinkSurfaces(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsA, wsB := "ws-A", "ws-B"
	id := mustWrite(t, d, "deploy-fix-elliot", wsA)
	_ = d.LinkMemoryEntity(ctx, id,
		store.EntityRef{Kind: "person", ID: "bob@x"}, "")
	_ = d.LinkMemoryEntity(ctx, id,
		store.EntityRef{Kind: "place", ID: "/Users/example/proj"}, "")

	filter := store.MemoryFilter{
		Scope:                    store.SkillScope{WorkspaceIDs: []string{wsB}},
		Entities:                 []store.EntityRef{{Kind: "person", ID: "bob@x"}},
		EntityDrivenIgnoresScope: true,
	}
	hits, err := d.ListMemories(ctx, filter)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != id {
		t.Fatalf("multi-link: want %s, got %+v", id, hits)
	}
}
