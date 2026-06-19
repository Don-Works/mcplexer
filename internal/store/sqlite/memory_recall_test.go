// memory_recall_test.go — coverage for the recall-event log + co-recall
// aggregator (AR4).
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestLogMemoryRecallEvents_Roundtrip(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	m1 := mustWrite(t, d, "m1", ws)
	m2 := mustWrite(t, d, "m2", ws)

	rsid := "01KSJTEST"
	events := []store.MemoryRecallEvent{
		{
			MemoryID: m1, WorkspaceID: ws, Query: "test",
			RankPosition: 1, ResultSetID: rsid, Source: "rrf",
		},
		{
			MemoryID: m2, WorkspaceID: ws, Query: "test",
			RankPosition: 2, ResultSetID: rsid, Source: "rrf",
		},
	}
	if err := d.LogMemoryRecallEvents(ctx, events); err != nil {
		t.Fatalf("LogMemoryRecallEvents: %v", err)
	}
	// Sanity: both events stamped IDs + timestamps.
	for _, e := range events {
		if e.ID == "" {
			t.Errorf("ID not stamped on %+v", e)
		}
		if e.CreatedAt.IsZero() {
			t.Errorf("CreatedAt not stamped on %+v", e)
		}
	}
}

func TestLogMemoryRecallEvents_Idempotent(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	m := mustWrite(t, d, "m", ws)

	ev := store.MemoryRecallEvent{
		ID: "01KSJDUP", MemoryID: m, WorkspaceID: ws,
		ResultSetID: "rs", RankPosition: 1, Source: "rrf",
	}
	for i := 0; i < 3; i++ {
		if err := d.LogMemoryRecallEvents(ctx, []store.MemoryRecallEvent{ev}); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// One row only (idempotent on id).
	var count int
	if err := d.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_recall_events WHERE id = ?`,
		ev.ID).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 row, got %d", count)
	}
}

func TestCoRecalledMemories_AggregatesAcrossResultSets(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	a := mustWrite(t, d, "a", ws)
	b := mustWrite(t, d, "b", ws)
	c := mustWrite(t, d, "c", ws)

	// rs1: A@1, B@2 → strong co-recall (adjacent)
	// rs2: A@1, B@3 → weaker (further apart)
	// rs3: A@1, C@5 → even weaker
	// rs4: A@2, B@1 → still strong (adjacent, just inverted)
	events := []store.MemoryRecallEvent{
		{MemoryID: a, ResultSetID: "rs1", RankPosition: 1},
		{MemoryID: b, ResultSetID: "rs1", RankPosition: 2},
		{MemoryID: a, ResultSetID: "rs2", RankPosition: 1},
		{MemoryID: b, ResultSetID: "rs2", RankPosition: 3},
		{MemoryID: a, ResultSetID: "rs3", RankPosition: 1},
		{MemoryID: c, ResultSetID: "rs3", RankPosition: 5},
		{MemoryID: a, ResultSetID: "rs4", RankPosition: 2},
		{MemoryID: b, ResultSetID: "rs4", RankPosition: 1},
	}
	if err := d.LogMemoryRecallEvents(ctx, events); err != nil {
		t.Fatalf("log: %v", err)
	}
	got, err := d.CoRecalledMemories(ctx, a,
		store.SkillScope{WorkspaceIDs: []string{ws}}, 10)
	if err != nil {
		t.Fatalf("CoRecalledMemories: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 co-recalled (b + c), got %d: %+v", len(got), got)
	}
	// B should rank above C: 3 co-occurrences vs 1; also B is rank-adjacent twice.
	if got[0].MemoryID != b {
		t.Fatalf("top = %+v, want b (%s)", got[0], b)
	}
	if got[0].CoOccurrences != 3 {
		t.Fatalf("b co_occurrences = %d, want 3", got[0].CoOccurrences)
	}
}

func TestCoRecalledMemories_ExcludesSelf(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	a := mustWrite(t, d, "a", ws)
	// Log the same memory at two positions in one result set.
	_ = d.LogMemoryRecallEvents(ctx, []store.MemoryRecallEvent{
		{MemoryID: a, ResultSetID: "rs", RankPosition: 1},
		{MemoryID: a, ResultSetID: "rs", RankPosition: 2},
	})
	got, _ := d.CoRecalledMemories(ctx, a,
		store.SkillScope{WorkspaceIDs: []string{ws}}, 10)
	if len(got) != 0 {
		t.Fatalf("self should be excluded, got %+v", got)
	}
}

func TestCoRecalledMemories_ExcludesDeleted(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	a := mustWrite(t, d, "a", ws)
	b := mustWrite(t, d, "b", ws)
	_ = d.LogMemoryRecallEvents(ctx, []store.MemoryRecallEvent{
		{MemoryID: a, ResultSetID: "rs", RankPosition: 1},
		{MemoryID: b, ResultSetID: "rs", RankPosition: 2},
	})
	// Soft-delete b — should drop out of co-recall.
	if err := d.SoftDeleteMemory(ctx, b); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	got, _ := d.CoRecalledMemories(ctx, a,
		store.SkillScope{WorkspaceIDs: []string{ws}}, 10)
	if len(got) != 0 {
		t.Fatalf("deleted should be excluded, got %+v", got)
	}
}

func TestForgetRecallEventsBySource(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	m := mustWrite(t, d, "m", "ws-1")
	_ = d.LogMemoryRecallEvents(ctx, []store.MemoryRecallEvent{
		{MemoryID: m, SessionID: "poisoned", ResultSetID: "rs", RankPosition: 1},
		{MemoryID: m, SessionID: "poisoned", ResultSetID: "rs2", RankPosition: 1},
		{MemoryID: m, SessionID: "clean", ResultSetID: "rs3", RankPosition: 1},
	})
	n, err := d.ForgetRecallEventsBySource(ctx, "poisoned", store.SkillScope{IncludeAll: true})
	if err != nil {
		t.Fatalf("forget: %v", err)
	}
	if n != 2 {
		t.Fatalf("want 2 purged, got %d", n)
	}
	var remaining int
	if err := d.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_recall_events`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("want 1 row remaining (the clean one), got %d", remaining)
	}
}

func TestForgetRecallEventsBySourceHonorsScope(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	wsA := "ws-a"
	wsB := "ws-b"
	global := mustWrite(t, d, "global", "")
	a := mustWrite(t, d, "a", wsA)
	b := mustWrite(t, d, "b", wsB)
	_ = d.LogMemoryRecallEvents(ctx, []store.MemoryRecallEvent{
		{MemoryID: global, SessionID: "poisoned", ResultSetID: "rs-g", RankPosition: 1},
		{MemoryID: a, SessionID: "poisoned", WorkspaceID: wsA, ResultSetID: "rs-a", RankPosition: 1},
		{MemoryID: b, SessionID: "poisoned", WorkspaceID: wsB, ResultSetID: "rs-b", RankPosition: 1},
	})

	n, err := d.ForgetRecallEventsBySource(ctx, "poisoned", store.SkillScope{WorkspaceIDs: []string{wsA}})
	if err != nil {
		t.Fatalf("forget scoped: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected global + ws-a purge count 2, got %d", n)
	}
	var remaining int
	if err := d.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memory_recall_events WHERE session_id = 'poisoned'`).Scan(&remaining); err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 1 {
		t.Fatalf("expected only ws-b event to survive, got %d row(s)", remaining)
	}
}

func TestGetMemoryRecallStats(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-1"
	hot := mustWrite(t, d, "hot", ws)   // recalled in 3 distinct result sets
	cold := mustWrite(t, d, "cold", ws) // never recalled
	stale := mustWrite(t, d, "stale", ws)

	// hot: three distinct result sets (recent_count must count DISTINCT sets,
	// not raw rows — rs1 has hot twice).
	if err := d.LogMemoryRecallEvents(ctx, []store.MemoryRecallEvent{
		{MemoryID: hot, ResultSetID: "rs1", RankPosition: 1},
		{MemoryID: hot, ResultSetID: "rs1", RankPosition: 2}, // dup set
		{MemoryID: hot, ResultSetID: "rs2", RankPosition: 1},
		{MemoryID: hot, ResultSetID: "rs3", RankPosition: 1},
	}); err != nil {
		t.Fatalf("log hot: %v", err)
	}
	// stale: one event but OUTSIDE the recency window — must be excluded.
	old := time.Now().Add(-recallStatsWindow - 24*time.Hour).UTC()
	if err := d.LogMemoryRecallEvents(ctx, []store.MemoryRecallEvent{
		{MemoryID: stale, ResultSetID: "rs-old", RankPosition: 1, CreatedAt: old},
	}); err != nil {
		t.Fatalf("log stale: %v", err)
	}

	got, err := d.GetMemoryRecallStats(ctx, []string{hot, cold, stale})
	if err != nil {
		t.Fatalf("GetMemoryRecallStats: %v", err)
	}
	// hot present with 3 distinct sets.
	hs, ok := got[hot]
	if !ok {
		t.Fatalf("hot missing from stats: %+v", got)
	}
	if hs.RecentCount != 3 {
		t.Fatalf("hot recent_count = %d, want 3 (distinct result sets)", hs.RecentCount)
	}
	if hs.LastRecalledAt.IsZero() {
		t.Fatalf("hot last_recalled not stamped")
	}
	// cold + stale absent: cold never recalled, stale only outside the window.
	if _, ok := got[cold]; ok {
		t.Fatalf("cold should be absent (never recalled), got %+v", got[cold])
	}
	if _, ok := got[stale]; ok {
		t.Fatalf("stale should be absent (outside recency window), got %+v", got[stale])
	}
}

func TestGetMemoryRecallStats_EmptyIDs(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	got, err := d.GetMemoryRecallStats(ctx, nil)
	if err != nil {
		t.Fatalf("GetMemoryRecallStats(nil): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty map for nil ids, got %+v", got)
	}
}

func TestCoRecalledMemories_FKCascade(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	m := mustWrite(t, d, "m", "ws-1")
	_ = d.LogMemoryRecallEvents(ctx, []store.MemoryRecallEvent{
		{MemoryID: m, ResultSetID: "rs", RankPosition: 1},
	})
	// Hard-delete the memory; FK CASCADE should drop the event.
	if _, err := d.q.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, m); err != nil {
		t.Fatalf("hard delete: %v", err)
	}
	var remaining int
	_ = d.q.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_recall_events`).Scan(&remaining)
	if remaining != 0 {
		t.Fatalf("CASCADE failed: %d event rows remain", remaining)
	}
}
