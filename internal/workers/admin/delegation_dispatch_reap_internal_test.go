package admin

import (
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestDispatchOrphanedPredicate table-drives the pure orphan predicate so
// every branch (already-resolved, disabled, has-run, grace boundary,
// missing timestamps) is pinned without spinning up a Service.
func TestDispatchOrphanedPredicate(t *testing.T) {
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	grace := 10 * time.Minute
	cutoff := now.Add(-grace)

	old := now.Add(-30 * time.Minute)  // comfortably past cutoff
	fresh := now.Add(-1 * time.Minute) // within grace
	archived := now.Add(-time.Hour)

	row := func(m delegationMetadata, enabled bool, createdAt time.Time) delegationWorkerRow {
		return delegationWorkerRow{
			worker: &store.Worker{ID: "w1", Enabled: enabled, CreatedAt: createdAt},
			meta:   m,
		}
	}
	runningRun := []*store.WorkerRun{{ID: "r1", Status: "running"}}
	terminalRun := []*store.WorkerRun{{ID: "r1", Status: "success"}}

	cases := []struct {
		name string
		row  delegationWorkerRow
		runs []*store.WorkerRun
		want bool
	}{
		{
			name: "no run row, past grace → orphan",
			row:  row(delegationMetadata{CreatedAt: old}, true, old),
			runs: nil,
			want: true,
		},
		{
			name: "no run row, within grace → not yet",
			row:  row(delegationMetadata{CreatedAt: fresh}, true, fresh),
			runs: nil,
			want: false,
		},
		{
			name: "running run row present → not reaped (existing path owns it)",
			row:  row(delegationMetadata{CreatedAt: old}, true, old),
			runs: runningRun,
			want: false,
		},
		{
			name: "terminal run row present → not reaped",
			row:  row(delegationMetadata{CreatedAt: old}, true, old),
			runs: terminalRun,
			want: false,
		},
		{
			name: "already dispatch-failed → idempotent no-op",
			row:  row(delegationMetadata{CreatedAt: old, DispatchFailed: true}, true, old),
			runs: nil,
			want: false,
		},
		{
			name: "archived → skipped",
			row:  row(delegationMetadata{CreatedAt: old, ArchivedAt: &archived}, true, old),
			runs: nil,
			want: false,
		},
		{
			name: "disabled worker → skipped",
			row:  row(delegationMetadata{CreatedAt: old}, false, old),
			runs: nil,
			want: false,
		},
		{
			name: "no metadata createdAt falls back to worker.CreatedAt (past grace)",
			row:  row(delegationMetadata{}, true, old),
			runs: nil,
			want: true,
		},
		{
			name: "no timestamps at all → not reaped (cannot age)",
			row:  row(delegationMetadata{}, true, time.Time{}),
			runs: nil,
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatchOrphaned(tc.row, tc.runs, cutoff); got != tc.want {
				t.Fatalf("dispatchOrphaned = %v, want %v", got, tc.want)
			}
		})
	}
}
