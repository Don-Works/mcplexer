// Package skillregistry — stats.go (W6) is the pure-functional aggregator
// over W2's skill_runs telemetry table. Take a slice of store.SkillRun
// rows + a rolling-window cutoff and emit a SkillStats roll-up the API
// + the dashboard tile render.
//
// All maths is in-memory — the store layer already filters by skill name
// + workspace + Since; this file owns percentile calculation, success
// rate, and top-tool aggregation. Pure so it is trivially testable and
// trivially callable per-skill from the composition-graph handler.
//
// Design choices (worth knowing before you tweak):
//
//   - **0-run input is a successful zero, not an error.** SkillStats with
//     Invocations=0 and rates=0 surfaces cleanly on the dashboard as
//     "no runs yet" without a special nil-check. The downstream JSON
//     shape stays stable across "skill has data" and "skill is brand
//     new". The lone exception is LastRunAt, which is nil when there
//     are no terminal runs to date its `last_run_at` from.
//   - **Rates are over terminal runs.** Running rows count toward
//     Invocations but NOT toward the denominator of SuccessRate /
//     FailureRate / CancelledRate. A skill that's been triggered 10
//     times but only finished once will not get a 10% success rate.
//   - **p50/p95 are over completed runs only.** Mid-flight runs have no
//     duration. Single-completed-run input gives p50=p95=that run's
//     duration (degenerate but well-defined).
//   - **Top tools is sum-of-counts.** Each SkillRun.ToolsUsedJSON is a
//     `[{name,count}]` list (W2's choice). We sum across runs, then
//     return the top N by total count, ties broken by name asc for
//     determinism. Default N=5.
package skillregistry

import (
	"encoding/json"
	"math"
	"sort"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// DefaultStatsWindow is the rolling-window default for the W6 stats
// aggregator (30d, per the milestone spec). Callers can override per-
// request via the HTTP `window_days` query parameter.
const DefaultStatsWindow = 30 * 24 * time.Hour

// defaultTopTools is how many top-N tools the aggregator surfaces in
// SkillStats.TopToolsUsed. Five is the sweet spot for the dashboard
// chip strip — enough to spot patterns, not so many the row wraps.
const defaultTopTools = 5

// SkillStats is the rolled-up telemetry view of a single skill. JSON
// shape matches the dashboard tile contract; if you change this, also
// update web/src/api/skill-stats.ts.
type SkillStats struct {
	// Invocations is every run (running + terminal) inside the window.
	Invocations int `json:"invocations"`

	// SuccessRate / FailureRate / CancelledRate are computed over terminal
	// runs only. Sum to 1.0 (within float drift) when at least one
	// terminal run exists; all three are 0 otherwise.
	SuccessRate   float64 `json:"success_rate"`
	FailureRate   float64 `json:"failure_rate"`
	CancelledRate float64 `json:"cancelled_rate"`

	// P50DurationMs / P95DurationMs are the median + 95th percentile of
	// completed-run durations in milliseconds. Zero when no completed
	// runs are in the window. Linear-interpolation percentiles (NIST
	// definition); we don't ceil to integer milliseconds since the
	// underlying durations are already int64 ms.
	P50DurationMs int64 `json:"p50_duration_ms"`
	P95DurationMs int64 `json:"p95_duration_ms"`

	// LastRunAt is the most-recent StartedAt across all runs in the
	// window (including in-flight). Nil when no runs exist — the
	// dashboard renders "Never" in that case.
	LastRunAt *time.Time `json:"last_run_at,omitempty"`

	// TopToolsUsed is the top-N tools by summed count across all runs
	// in the window. Empty when no runs carried a ToolsUsedJSON blob.
	TopToolsUsed []SkillStatsToolUse `json:"top_tools_used"`

	// WindowDays records the rolling window the rollup was computed
	// over. Surfaced in the JSON so the dashboard tile can label the
	// "(30d)" suffix without re-reading the query string.
	WindowDays int `json:"window_days"`
}

// SkillStatsToolUse is one entry in SkillStats.TopToolsUsed. Mirrors
// store.SkillRunToolUse shape — kept distinct so the API layer can evolve
// independently of the storage wire format.
type SkillStatsToolUse struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// StatsOptions tunes the aggregator. Zero values give sensible defaults
// (DefaultStatsWindow, defaultTopTools).
type StatsOptions struct {
	// Window is the rolling cutoff. Runs with StartedAt < Now-Window are
	// dropped on the assumption the caller already trimmed at the
	// store-layer SQL — this is belt-and-braces in case raw rows are
	// passed in. Zero defaults to DefaultStatsWindow.
	Window time.Duration

	// TopTools caps SkillStats.TopToolsUsed length. Zero or negative
	// defaults to defaultTopTools.
	TopTools int

	// Now is the reference timestamp for the window cutoff. Zero falls
	// back to time.Now(). Tests pass an explicit anchor to keep fixtures
	// deterministic.
	Now time.Time
}

// AggregateSkillRuns computes the W6 SkillStats over runs. A nil/empty
// slice yields a zero-valued SkillStats with Invocations=0 and
// LastRunAt=nil — the canonical "no data yet" shape.
//
// runs may include in-flight (running) rows: they increment Invocations
// but do not contribute to rates or percentiles. Caller's responsibility
// to filter by skill_name if mixed-skill input is unwanted.
func AggregateSkillRuns(runs []store.SkillRun, opts StatsOptions) SkillStats {
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}
	window := opts.Window
	if window <= 0 {
		window = DefaultStatsWindow
	}
	topN := opts.TopTools
	if topN <= 0 {
		topN = defaultTopTools
	}
	cutoff := now.Add(-window)

	stats := SkillStats{
		WindowDays:   int(math.Round(window.Hours() / 24)),
		TopToolsUsed: []SkillStatsToolUse{},
	}

	var (
		durations  []int64 // ms, completed only
		succeeded  int
		failed     int
		cancelled  int
		terminal   int
		toolCounts = map[string]int{}
		lastRun    time.Time
	)

	for i := range runs {
		r := runs[i]
		if r.StartedAt.Before(cutoff) {
			continue
		}
		stats.Invocations++
		if r.StartedAt.After(lastRun) {
			lastRun = r.StartedAt
		}
		switch r.Outcome {
		case store.SkillRunOutcomeSuccess:
			succeeded++
			terminal++
		case store.SkillRunOutcomeFailure:
			failed++
			terminal++
		case store.SkillRunOutcomeCancelled:
			cancelled++
			terminal++
		}
		if r.CompletedAt != nil && !r.CompletedAt.Before(r.StartedAt) {
			durations = append(durations, r.CompletedAt.Sub(r.StartedAt).Milliseconds())
		}
		mergeToolCounts(toolCounts, r.ToolsUsedJSON)
	}

	if terminal > 0 {
		denom := float64(terminal)
		stats.SuccessRate = float64(succeeded) / denom
		stats.FailureRate = float64(failed) / denom
		stats.CancelledRate = float64(cancelled) / denom
	}
	stats.P50DurationMs = percentileMs(durations, 0.50)
	stats.P95DurationMs = percentileMs(durations, 0.95)
	if !lastRun.IsZero() {
		lr := lastRun
		stats.LastRunAt = &lr
	}
	stats.TopToolsUsed = topNTools(toolCounts, topN)
	return stats
}

// mergeToolCounts decodes one run's ToolsUsedJSON (a `[{name,count}]`
// array per W2 contract) and folds the counts into the running total.
// Malformed / empty payloads are silently skipped — telemetry should
// never break the aggregator.
func mergeToolCounts(into map[string]int, raw json.RawMessage) {
	if len(raw) == 0 {
		return
	}
	var list []store.SkillRunToolUse
	if err := json.Unmarshal(raw, &list); err != nil {
		return
	}
	for _, t := range list {
		if t.Name == "" {
			continue
		}
		into[t.Name] += t.Count
	}
}

// topNTools returns the top-N tool entries by count, ties broken by
// name ascending (so the output is deterministic across runs). Empty
// input returns an empty slice — never nil — so JSON serialises as `[]`.
func topNTools(counts map[string]int, n int) []SkillStatsToolUse {
	out := make([]SkillStatsToolUse, 0, len(counts))
	for name, c := range counts {
		out = append(out, SkillStatsToolUse{Name: name, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// percentileMs computes p (0..1) of values using linear interpolation
// between adjacent sample values (NIST R-7 / the default of numpy +
// pandas + Excel). Empty input returns 0. values is mutated (sorted in
// place) — the caller's slice is intentionally a working buffer.
func percentileMs(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}
	if len(values) == 1 {
		return values[0]
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	rank := p * float64(len(values)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return values[lo]
	}
	frac := rank - float64(lo)
	return int64(math.Round(float64(values[lo]) + frac*float64(values[hi]-values[lo])))
}
