// memory_stats_test.go — coverage for GetMemoryStats, focused on the
// recall-log-driven decay pressure + 7-day recall rate (and their
// graceful fallback when recall tracking is disabled / the log is empty).
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// backdateUpdatedAt forces a memory's updated_at to a fixed unix instant so
// the 180-day decay window is deterministic in tests (WriteMemory always
// stamps now). Test-only helper — uses the package-internal handle.
func backdateUpdatedAt(t *testing.T, d *DB, id string, when time.Time) {
	t.Helper()
	if _, err := d.q.ExecContext(context.Background(),
		`UPDATE memories SET updated_at = ? WHERE id = ?`, when.Unix(), id); err != nil {
		t.Fatalf("backdate updated_at: %v", err)
	}
}

func TestGetMemoryStatsDecayFallbackWhenLogEmpty(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-stats"
	old := time.Now().UTC().AddDate(0, 0, -200) // older than the 180d window
	fresh := time.Now().UTC().AddDate(0, 0, -1)

	stale := mustWrite(t, d, "stale", ws)
	backdateUpdatedAt(t, d, stale, old)
	recent := mustWrite(t, d, "recent", ws)
	backdateUpdatedAt(t, d, recent, fresh)
	// A pinned-but-old memory must NOT count toward decay.
	pinned := mustWrite(t, d, "pinned-old", ws)
	backdateUpdatedAt(t, d, pinned, old)
	if err := d.SetMemoryPinned(ctx, pinned, true); err != nil {
		t.Fatalf("pin: %v", err)
	}
	// SetMemoryPinned bumps updated_at, so re-backdate.
	backdateUpdatedAt(t, d, pinned, old)

	stats, err := d.GetMemoryStats(ctx, store.SkillScope{WorkspaceIDs: []string{ws}})
	if err != nil {
		t.Fatalf("GetMemoryStats: %v", err)
	}
	// With an empty recall log, decay = old AND not-pinned AND valid → only
	// "stale" (recent is fresh, pinned-old is pinned).
	if stats.DecayPressure != 1 {
		t.Fatalf("DecayPressure = %d, want 1 (only the stale unpinned row)", stats.DecayPressure)
	}
	if stats.RecallRate7d != 0 {
		t.Fatalf("RecallRate7d = %v, want 0 when recall log is empty", stats.RecallRate7d)
	}
}

func TestGetMemoryStatsDecayUsesRecallLog(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-stats2"
	old := time.Now().UTC().AddDate(0, 0, -200)

	// Two old memories. One was recalled recently → not decaying; the other
	// has never been recalled → decaying.
	recalledOld := mustWrite(t, d, "recalled-old", ws)
	backdateUpdatedAt(t, d, recalledOld, old)
	neglectedOld := mustWrite(t, d, "neglected-old", ws)
	backdateUpdatedAt(t, d, neglectedOld, old)

	// Recall event for recalledOld within the last day — populates the log
	// so GetMemoryStats takes the recall-aware decay path.
	if err := d.LogMemoryRecallEvents(ctx, []store.MemoryRecallEvent{
		{
			MemoryID: recalledOld, WorkspaceID: ws, Query: "q",
			RankPosition: 1, ResultSetID: "rs-stats", Source: "rrf",
			CreatedAt: time.Now().UTC().Add(-1 * time.Hour),
		},
	}); err != nil {
		t.Fatalf("log recall: %v", err)
	}

	stats, err := d.GetMemoryStats(ctx, store.SkillScope{WorkspaceIDs: []string{ws}})
	if err != nil {
		t.Fatalf("GetMemoryStats: %v", err)
	}
	// recalledOld is old but recalled within 30d → excluded. neglectedOld is
	// old and never recalled → counted. Decay pressure should be 1.
	if stats.DecayPressure != 1 {
		t.Fatalf("DecayPressure = %d, want 1 (only the never-recalled old row)", stats.DecayPressure)
	}
	// 1 of 2 valid memories recalled in the last 7 days → 0.5.
	if stats.RecallRate7d != 0.5 {
		t.Fatalf("RecallRate7d = %v, want 0.5 (1 of 2 recalled in 7d)", stats.RecallRate7d)
	}
}

func TestGetMemoryStatsRecallRateStaleEventNotCounted(t *testing.T) {
	ctx := context.Background()
	d := newMemDB(t)
	ws := "ws-stats3"
	m := mustWrite(t, d, "m", ws)
	// A recall event OLDER than 7 days must not count toward the 7-day rate,
	// but its presence still flips the log out of the empty-fallback path.
	if err := d.LogMemoryRecallEvents(ctx, []store.MemoryRecallEvent{
		{
			MemoryID: m, WorkspaceID: ws, ResultSetID: "rs-old",
			RankPosition: 1, Source: "rrf",
			CreatedAt: time.Now().UTC().AddDate(0, 0, -10),
		},
	}); err != nil {
		t.Fatalf("log recall: %v", err)
	}
	stats, err := d.GetMemoryStats(ctx, store.SkillScope{WorkspaceIDs: []string{ws}})
	if err != nil {
		t.Fatalf("GetMemoryStats: %v", err)
	}
	if stats.RecallRate7d != 0 {
		t.Fatalf("RecallRate7d = %v, want 0 (only a >7d-old recall event)", stats.RecallRate7d)
	}
}
