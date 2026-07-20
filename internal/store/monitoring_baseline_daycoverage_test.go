package store

import "testing"

// TestDayHistoryFloorIsAWholeWeek pins the boundary the floor is justified on.
//
// The floor exists to defeat one specific trap: a weekday-only job observed
// Monday to Friday shows a gap-free range, because the range ENDS on the Friday
// and the missing Saturday falls outside it. Seven consecutive days are one
// whole week cycle and therefore contain exactly one Saturday and one Sunday,
// so a gap-free run of seven has observed both and cannot have come from a
// weekday-only job. Six can be Monday to Saturday and proves nothing about
// Sundays.
//
// The previous floor of fourteen was set for this same hazard but bought
// nothing more against it, and could not be met at all: raw lines retain 7 days
// and migration 140 backfills day history FROM those lines, so a fresh install
// could never satisfy it and NOTHING promoted. Measured at the real incident
// the template had 3-4 days against a floor of 14.
func TestDayHistoryFloorIsAWholeWeek(t *testing.T) {
	if BaselineMinDayHistoryDays != 7 {
		t.Fatalf("floor = %d; the whole-week argument only holds at 7",
			BaselineMinDayHistoryDays)
	}
	tests := []struct {
		name string
		days int
		want BaselineDecision
	}{
		{"a full week of gap-free history promotes", 7, BaselinePromoted},
		{"a fortnight still promotes", 14, BaselinePromoted},
		{"six days cannot rule out a weekday-only job", 6, BaselineRejectDayGaps},
		{"a working week alone certainly cannot", 5, BaselineRejectDayGaps},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := promotableCandidate()
			c.DayHistoryDays = tt.days
			if got := EvaluateBaselineCandidate(c).Decision; got != tt.want {
				t.Errorf("decision with %d days = %q; want %q", tt.days, got, tt.want)
			}
		})
	}
}

// TestDayGapsStillRejectInteriorHoles proves the relaxed floor did not weaken
// the check that actually catches a weekly pattern once it is visible. A single
// missing day inside the observed range is still a refusal, at any width.
func TestDayGapsStillRejectInteriorHoles(t *testing.T) {
	for _, days := range []int{7, 14, 28} {
		c := promotableCandidate()
		c.DayHistoryDays, c.DayGaps = days, 1
		v := EvaluateBaselineCandidate(c)
		if v.Decision != BaselineRejectDayGaps {
			t.Errorf("%d days with one interior gap: decision = %q; want day_gaps",
				days, v.Decision)
		}
	}
}
