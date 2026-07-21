package skillregistry

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// fixedNow is the deterministic clock anchor for all stats tests. Picked
// to fall inside a default 30-day window so test runs can pre-date it by
// hours / days without falling off the cliff.
var fixedNow = time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC)

// run is a compact test-helper for building a SkillRun row with sensible
// defaults. Pass startedAgo / durationMs in ms (0 duration → CompletedAt
// nil → counts as "running"). outcome="" → "running".
func run(name string, startedAgo time.Duration, durationMs int64, outcome string, tools ...store.SkillRunToolUse) store.SkillRun {
	startedAt := fixedNow.Add(-startedAgo)
	out := store.SkillRun{
		SkillName: name,
		StartedAt: startedAt,
		Outcome:   outcome,
	}
	if out.Outcome == "" {
		out.Outcome = store.SkillRunOutcomeRunning
	}
	if durationMs > 0 {
		completed := startedAt.Add(time.Duration(durationMs) * time.Millisecond)
		out.CompletedAt = &completed
	}
	if len(tools) > 0 {
		b, _ := json.Marshal(tools)
		out.ToolsUsedJSON = b
	}
	return out
}

func TestAggregateSkillRuns_EmptyInput(t *testing.T) {
	got := AggregateSkillRuns(nil, StatsOptions{Now: fixedNow})
	if got.Invocations != 0 {
		t.Errorf("Invocations = %d, want 0", got.Invocations)
	}
	if got.SuccessRate != 0 || got.FailureRate != 0 || got.CancelledRate != 0 {
		t.Errorf("Rates = (%v,%v,%v), want all zero", got.SuccessRate, got.FailureRate, got.CancelledRate)
	}
	if got.P50DurationMs != 0 || got.P95DurationMs != 0 {
		t.Errorf("Percentiles = (%d,%d), want both 0", got.P50DurationMs, got.P95DurationMs)
	}
	if got.LastRunAt != nil {
		t.Errorf("LastRunAt = %v, want nil", got.LastRunAt)
	}
	if got.TopToolsUsed == nil {
		t.Error("TopToolsUsed must be non-nil empty slice for JSON [] serialisation")
	}
	if got.WindowDays != 30 {
		t.Errorf("WindowDays = %d, want 30 (default)", got.WindowDays)
	}
}

func TestAggregateSkillRuns_SingleSuccessfulRun(t *testing.T) {
	runs := []store.SkillRun{
		run("foo", 1*time.Hour, 500, store.SkillRunOutcomeSuccess),
	}
	got := AggregateSkillRuns(runs, StatsOptions{Now: fixedNow})
	if got.Invocations != 1 {
		t.Errorf("Invocations = %d, want 1", got.Invocations)
	}
	if got.SuccessRate != 1.0 {
		t.Errorf("SuccessRate = %v, want 1.0", got.SuccessRate)
	}
	if got.FailureRate != 0 || got.CancelledRate != 0 {
		t.Errorf("FailureRate=%v CancelledRate=%v, want 0", got.FailureRate, got.CancelledRate)
	}
	if got.P50DurationMs != 500 || got.P95DurationMs != 500 {
		t.Errorf("Percentiles = (%d,%d), want (500,500)", got.P50DurationMs, got.P95DurationMs)
	}
	if got.LastRunAt == nil || !got.LastRunAt.Equal(fixedNow.Add(-1*time.Hour)) {
		t.Errorf("LastRunAt = %v, want %v", got.LastRunAt, fixedNow.Add(-1*time.Hour))
	}
}

func TestAggregateSkillRuns_MixedOutcomes(t *testing.T) {
	runs := []store.SkillRun{
		run("foo", 1*time.Hour, 100, store.SkillRunOutcomeSuccess),
		run("foo", 2*time.Hour, 200, store.SkillRunOutcomeSuccess),
		run("foo", 3*time.Hour, 300, store.SkillRunOutcomeFailure),
		run("foo", 4*time.Hour, 400, store.SkillRunOutcomeCancelled),
		run("foo", 5*time.Hour, 0, store.SkillRunOutcomeRunning), // in-flight
	}
	got := AggregateSkillRuns(runs, StatsOptions{Now: fixedNow})
	if got.Invocations != 5 {
		t.Errorf("Invocations = %d, want 5", got.Invocations)
	}
	// 4 terminal runs: 2 success, 1 failure, 1 cancelled.
	if got.SuccessRate != 0.5 {
		t.Errorf("SuccessRate = %v, want 0.5", got.SuccessRate)
	}
	if got.FailureRate != 0.25 {
		t.Errorf("FailureRate = %v, want 0.25", got.FailureRate)
	}
	if got.CancelledRate != 0.25 {
		t.Errorf("CancelledRate = %v, want 0.25", got.CancelledRate)
	}
	// Percentiles over 4 completed: [100, 200, 300, 400]. p50 → 250
	// (interp between 200+300); p95 → 385 (interp 95% of len-1=3 → 2.85).
	if got.P50DurationMs != 250 {
		t.Errorf("P50DurationMs = %d, want 250", got.P50DurationMs)
	}
	if got.P95DurationMs != 385 {
		t.Errorf("P95DurationMs = %d, want 385", got.P95DurationMs)
	}
}

func TestAggregateSkillRuns_TopToolsUsed_TopNAndTieBreaks(t *testing.T) {
	runs := []store.SkillRun{
		run("foo", 1*time.Hour, 100, store.SkillRunOutcomeSuccess,
			store.SkillRunToolUse{Name: "bash", Count: 5},
			store.SkillRunToolUse{Name: "read", Count: 3},
		),
		run("foo", 2*time.Hour, 100, store.SkillRunOutcomeSuccess,
			store.SkillRunToolUse{Name: "bash", Count: 2},
			store.SkillRunToolUse{Name: "edit", Count: 4},
		),
		// Tie breaker: "alpha" + "zeta" both end at count=1 → alpha first.
		run("foo", 3*time.Hour, 100, store.SkillRunOutcomeSuccess,
			store.SkillRunToolUse{Name: "alpha", Count: 1},
			store.SkillRunToolUse{Name: "zeta", Count: 1},
			store.SkillRunToolUse{Name: "grep", Count: 2},
		),
	}
	got := AggregateSkillRuns(runs, StatsOptions{Now: fixedNow, TopTools: 4})
	want := []SkillStatsToolUse{
		{Name: "bash", Count: 7},
		{Name: "edit", Count: 4},
		{Name: "read", Count: 3},
		{Name: "grep", Count: 2},
	}
	if !reflect.DeepEqual(got.TopToolsUsed, want) {
		t.Errorf("TopToolsUsed = %+v, want %+v", got.TopToolsUsed, want)
	}
}

func TestAggregateSkillRuns_WindowFilter_DropsOldRuns(t *testing.T) {
	runs := []store.SkillRun{
		run("foo", 1*time.Hour, 100, store.SkillRunOutcomeSuccess),      // in window
		run("foo", 31*24*time.Hour, 100, store.SkillRunOutcomeSuccess),  // out of window (default 30d)
		run("foo", 100*24*time.Hour, 100, store.SkillRunOutcomeFailure), // way out
	}
	got := AggregateSkillRuns(runs, StatsOptions{Now: fixedNow})
	if got.Invocations != 1 {
		t.Errorf("Invocations = %d, want 1 (only the recent run)", got.Invocations)
	}
	if got.SuccessRate != 1.0 {
		t.Errorf("SuccessRate = %v, want 1.0", got.SuccessRate)
	}
}

func TestAggregateSkillRuns_CustomWindow(t *testing.T) {
	runs := []store.SkillRun{
		run("foo", 6*time.Hour, 100, store.SkillRunOutcomeSuccess),
		run("foo", 5*24*time.Hour, 100, store.SkillRunOutcomeSuccess), // 5 days ago
	}
	// 1-day window → only the 6h-old run counts.
	got := AggregateSkillRuns(runs, StatsOptions{Now: fixedNow, Window: 24 * time.Hour})
	if got.Invocations != 1 {
		t.Errorf("Invocations = %d, want 1 (1-day window)", got.Invocations)
	}
	if got.WindowDays != 1 {
		t.Errorf("WindowDays = %d, want 1", got.WindowDays)
	}
}

func TestAggregateSkillRuns_OnlyRunning_NoRatesNoPercentiles(t *testing.T) {
	runs := []store.SkillRun{
		run("foo", 1*time.Hour, 0, store.SkillRunOutcomeRunning),
		run("foo", 2*time.Hour, 0, store.SkillRunOutcomeRunning),
	}
	got := AggregateSkillRuns(runs, StatsOptions{Now: fixedNow})
	if got.Invocations != 2 {
		t.Errorf("Invocations = %d, want 2", got.Invocations)
	}
	if got.SuccessRate != 0 || got.FailureRate != 0 || got.CancelledRate != 0 {
		t.Errorf("Rates non-zero with only-running input: (%v,%v,%v)",
			got.SuccessRate, got.FailureRate, got.CancelledRate)
	}
	if got.P50DurationMs != 0 || got.P95DurationMs != 0 {
		t.Errorf("Percentiles non-zero with no completed runs: (%d,%d)",
			got.P50DurationMs, got.P95DurationMs)
	}
	if got.LastRunAt == nil {
		t.Error("LastRunAt must be set even for all-running input")
	}
}

func TestAggregateSkillRuns_MalformedToolsJSON_SilentSkip(t *testing.T) {
	r := run("foo", 1*time.Hour, 100, store.SkillRunOutcomeSuccess)
	r.ToolsUsedJSON = []byte(`{not valid json`)
	got := AggregateSkillRuns([]store.SkillRun{r}, StatsOptions{Now: fixedNow})
	if got.Invocations != 1 {
		t.Errorf("Invocations = %d, want 1 (malformed tools should not block aggregation)", got.Invocations)
	}
	if len(got.TopToolsUsed) != 0 {
		t.Errorf("TopToolsUsed = %v, want empty (malformed JSON skipped)", got.TopToolsUsed)
	}
}

func TestPercentileMs_TableDriven(t *testing.T) {
	cases := []struct {
		name   string
		values []int64
		p      float64
		want   int64
	}{
		{"empty", nil, 0.5, 0},
		{"single", []int64{42}, 0.5, 42},
		{"single-p95", []int64{42}, 0.95, 42},
		{"two-p50", []int64{100, 200}, 0.5, 150},
		{"four-p50", []int64{100, 200, 300, 400}, 0.5, 250},
		{"four-p95", []int64{100, 200, 300, 400}, 0.95, 385},
		{"hundred-p95", makeRange(1, 100), 0.95, 95},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Copy to avoid mutating the case's slice on repeated runs.
			vs := append([]int64(nil), tc.values...)
			got := percentileMs(vs, tc.p)
			if got != tc.want {
				t.Errorf("percentileMs(%v, %v) = %d, want %d", tc.values, tc.p, got, tc.want)
			}
		})
	}
}

func makeRange(lo, hi int64) []int64 {
	out := make([]int64, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, i)
	}
	return out
}
