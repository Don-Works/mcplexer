package admin

import "testing"

func TestCrossItemOverlapWarnings(t *testing.T) {
	dels := []DelegationInput{
		{TouchesFiles: []string{"a.go", "b.go"}},
		{TouchesFiles: []string{"b.go"}}, // collides with item 0
		{TouchesFiles: []string{"c.go"}},
		{TouchesFiles: []string{"b.go"}}, // collides again — only one warning per file
	}
	warnings := crossItemOverlapWarnings(dels)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly 1 (dedup per file)", warnings)
	}
	if !contains(warnings[0], "b.go") {
		t.Fatalf("warning missing file: %q", warnings[0])
	}
}

func TestCrossItemOverlapWarningsNoneWhenDisjoint(t *testing.T) {
	dels := []DelegationInput{
		{TouchesFiles: []string{"a.go"}},
		{TouchesFiles: []string{"b.go"}},
	}
	if w := crossItemOverlapWarnings(dels); len(w) != 0 {
		t.Fatalf("disjoint files must not warn, got %v", w)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestDelegationPlanSizeMatchesPlan(t *testing.T) {
	cands := []delegationResolvedModelCandidate{{}, {}, {}} // 3 candidates
	cases := []struct {
		mode string
		par  int
		want int
	}{
		{"", 1, 3},                                 // default: candidates * 1
		{"", 2, 6},                                 // default: candidates * 2
		{delegationModelSelectionRandom, 5, 5},     // random: parallelism only
		{delegationModelSelectionCapacity, 4, 4},   // capacity: parallelism only
		{delegationModelSelectionSideBySide, 2, 6}, // side_by_side: candidates * 2
		{"", 0, 3},                                 // parallelism defaults to 1
	}
	for _, tc := range cases {
		in := DelegationInput{ModelSelectionMode: tc.mode, Parallelism: tc.par, resolvedModelCandidates: cands}
		if got := delegationPlanSize(in); got != tc.want {
			t.Errorf("mode=%q par=%d: size=%d, want %d", tc.mode, tc.par, got, tc.want)
		}
		// Cross-check against the actual plan builder.
		if built := len(buildDelegationModelPlan(in)); built != tc.want {
			t.Errorf("mode=%q par=%d: buildDelegationModelPlan len=%d, want %d", tc.mode, tc.par, built, tc.want)
		}
	}
}
