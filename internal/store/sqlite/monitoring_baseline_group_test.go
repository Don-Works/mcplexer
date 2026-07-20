package sqlite

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestBaselineGroupByCadenceReunitesReleases covers the grouping policy in
// isolation: releases of one job merge, distinct jobs do not, and the
// minimum-sample floor is applied to the MERGED total.
//
// That last point is the whole bootstrap fix. Applied per template — as the SQL
// HAVING clause used to — it discards exactly the two halves of a
// freshly-redeployed job that need adding together, which is how a rule could
// be permanently prevented from bootstrapping by an ordinary release cadence.
func TestBaselineGroupByCadenceReunitesReleases(t *testing.T) {
	const src = "src-1"
	half := int64(store.BaselineMinDeltas/2 + 1)

	groups := baselineGroupByCadence(src, []baselineTemplate{
		{id: "tpl-r1", masked: "order sync done <n> sync.go:142", lines: half},
		{id: "tpl-r2", masked: "order sync done <n> sync.go:151", lines: half},
		{id: "tpl-other", masked: "invoice run done <n> billing.go:9", lines: 500},
	})
	if len(groups) != 2 {
		t.Fatalf("groups = %d; want 2 (one per job)", len(groups))
	}
	byKey := map[string]baselineGroup{}
	for _, g := range groups {
		byKey[g.key] = g
	}
	sync := byKey[store.CadenceKey(src, "order sync done <n> sync.go:142")]
	if len(sync.ids) != 2 {
		t.Fatalf("order-sync group holds %d template ids; want both releases", len(sync.ids))
	}
	if sync.lines != 2*half {
		t.Errorf("group lines = %d; want the summed %d", sync.lines, 2*half)
	}
	if _, ok := byKey[store.CadenceKey(src, "invoice run done <n> billing.go:9")]; !ok {
		t.Error("the unrelated job was merged away; distinct jobs must stay distinct")
	}
}

// TestBaselineGroupByCadenceDropsThinAndFirehoseGroups pins the two bounds the
// grouped total is judged against.
func TestBaselineGroupByCadenceDropsThinAndFirehoseGroups(t *testing.T) {
	const src = "src-1"
	tests := []struct {
		name  string
		lines int64
		keep  bool
	}{
		{"below the robustness budget", store.BaselineMinDeltas, false},
		{"just above it", store.BaselineMinDeltas + 1, true},
		{"a firehose, not a schedule", baselineMaxTemplateLines + 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := baselineGroupByCadence(src, []baselineTemplate{
				{id: "tpl", masked: "a recurring line <n>", lines: tt.lines},
			})
			if (len(got) == 1) != tt.keep {
				t.Errorf("kept = %v with %d lines; want %v", len(got) == 1, tt.lines, tt.keep)
			}
		})
	}
}

// TestBaselineGroupTemplateIDsAndCadenceMap checks the two projections the
// arrival scan binds, since a template missing from either would silently drop
// half a job's history.
func TestBaselineGroupTemplateIDsAndCadenceMap(t *testing.T) {
	const src = "src-1"
	groups := baselineGroupByCadence(src, []baselineTemplate{
		{id: "tpl-r1", masked: "order sync done <n> sync.go:142", lines: 200},
		{id: "tpl-r2", masked: "order sync done <n> sync.go:151", lines: 200},
	})
	ids := baselineGroupTemplateIDs(groups)
	if len(ids) != 2 {
		t.Fatalf("template ids = %v; want both releases bound into the scan", ids)
	}
	byTemplate := baselineCadenceByTemplate(groups)
	if byTemplate["tpl-r1"] == "" || byTemplate["tpl-r1"] != byTemplate["tpl-r2"] {
		t.Errorf("cadence map = %v; both releases must fold into one series", byTemplate)
	}
}
