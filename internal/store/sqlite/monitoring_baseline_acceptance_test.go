// monitoring_baseline_acceptance_test.go — the end-to-end bar for the baseline
// learner, written against the MEASURED shape of the production order-sync job
// rather than a convenient one.
//
// A backtest against real data proved the detector would NOT have fired for the
// incident it was built for. Three gates rejected the real signal: the
// regularity gate scored bunched arrivals as noise, the day-history floor could
// not be met from 7-day line retention, and a redeploy reset the learning clock
// by minting a new template id for unchanged message text. This file pins all
// three, in one scenario, from real rows to a raised absence incident.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// The measured production shape: a 5-minute tick carrying ~3.3 completions
// bunched a second apart (~40/hour), flat 24/7, against 7 days of retained
// lines, with a redeploy 3 days before now.
const (
	acceptTick        = 5 * time.Minute
	acceptIntra       = time.Second
	acceptRetention   = 7 * 24 * time.Hour
	acceptRedeployAgo = 3 * 24 * time.Hour
)

// seedBurstyRedeployedJob writes the real arrival shape across TWO template
// ids, split at the redeploy. Both carry identical message text; they differ
// only in the code-location line number, which is exactly what a release does
// and exactly what the masker protects from masking.
func seedBurstyRedeployedJob(
	t *testing.T, db *sqlite.DB, ctx context.Context, src *store.LogSource,
) {
	t.Helper()
	const (
		maskedR1 = "order sync completed batch=<n> ordersync.go:142"
		maskedR2 = "order sync completed batch=<n> ordersync.go:151"
		rawR1    = "order sync completed batch=418 ordersync.go:142"
		rawR2    = "order sync completed batch=418 ordersync.go:151"
	)
	start := baselineFixtureNow.Add(-acceptRetention)
	redeploy := baselineFixtureNow.Add(-acceptRedeployAgo)
	seedTemplateRow(t, db, ctx, src, "tpl-sync-r1", maskedR1, start, redeploy)
	seedTemplateRow(t, db, ctx, src, "tpl-sync-r2", maskedR2, redeploy, baselineFixtureNow)

	lines := []store.LogLine{}
	for i := 0; ; i++ {
		tick := start.Add(time.Duration(i) * acceptTick)
		if tick.After(baselineFixtureNow) {
			break
		}
		// 3 completions per tick, 4 on every third — ~3.33/tick, ~40/hour.
		n := 3
		if i%3 == 0 {
			n = 4
		}
		id, raw := "tpl-sync-r1", rawR1
		if !tick.Before(redeploy) {
			id, raw = "tpl-sync-r2", rawR2
		}
		for j := 0; j < n; j++ {
			lines = append(lines, store.LogLine{
				SourceID: src.ID, TemplateID: id,
				TS: tick.Add(time.Duration(j) * acceptIntra), Line: raw,
			})
		}
	}
	if err := db.InsertLogLines(ctx, lines); err != nil {
		t.Fatalf("insert order-sync lines: %v", err)
	}
}

// seedLivenessTemplate writes unrelated chatter that keeps producing lines
// after the order-sync job stops. Without it the evaluator would correctly
// report COLLECTION ("we cannot see") rather than absence, so this is what
// makes the silence attributable to the job instead of the collector.
func seedLivenessTemplate(
	t *testing.T, db *sqlite.DB, ctx context.Context, src *store.LogSource, until time.Time,
) {
	t.Helper()
	start := baselineFixtureNow.Add(-acceptRetention)
	seedTemplateRow(t, db, ctx, src, "tpl-health", "health probe ok <dur>", start, until)
	lines := []store.LogLine{}
	for ts := start; !ts.After(until); ts = ts.Add(time.Minute) {
		lines = append(lines, store.LogLine{
			SourceID: src.ID, TemplateID: "tpl-health", TS: ts, Line: "health probe ok 2ms",
		})
	}
	if err := db.InsertLogLines(ctx, lines); err != nil {
		t.Fatalf("insert liveness lines: %v", err)
	}
}

func seedTemplateRow(
	t *testing.T, db *sqlite.DB, ctx context.Context,
	src *store.LogSource, id, masked string, first, last time.Time,
) {
	t.Helper()
	tpl := &store.LogTemplate{
		ID: id, SourceID: src.ID, Masked: masked,
		Severity: store.SeverityInfo, FirstSeen: first, LastSeen: last,
	}
	if _, err := db.UpsertLogTemplate(ctx, tpl, 1); err != nil {
		t.Fatalf("upsert template %s: %v", id, err)
	}
}

// TestAcceptanceBurstyRedeployedJobPromotesAndDetectsAbsence is the bar the
// whole feature is measured against: the real job must PROMOTE, and the same
// job going silent must RAISE.
func TestAcceptanceBurstyRedeployedJobPromotesAndDetectsAbsence(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	src := seedBaselineSource(t, db, ctx)
	silentUntil := baselineFixtureNow.Add(2 * time.Hour)
	seedBurstyRedeployedJob(t, db, ctx, src)
	seedLivenessTemplate(t, db, ctx, src, silentUntil)

	c := findCandidate(t, db, ctx, src,
		store.CadenceKey(src.ID, "order sync completed batch=<n> ordersync.go:142"))

	// The redeploy fix: one candidate spanning BOTH release eras. Neither era
	// alone clears the gates — the pre-redeploy half holds 5 days of day
	// history and the post-redeploy half 4, against a floor of 7, and the
	// post-redeploy half spans only 72h against a 72h minimum. Only the merged
	// history promotes, which is precisely the 70.33h-against-72h miss
	// measured at the real incident.
	if got, want := c.FirstSeen.UTC(), baselineFixtureNow.Add(-acceptRetention); !got.Equal(want) {
		t.Errorf("first seen = %s; want %s — history did not carry across the redeploy",
			got, want)
	}
	if c.DayHistoryDays < store.BaselineMinDayHistoryDays {
		t.Errorf("day history = %d days; want at least %d — the union across template ids "+
			"is what makes the floor reachable", c.DayHistoryDays, store.BaselineMinDayHistoryDays)
	}
	if c.DayGaps != 0 {
		t.Errorf("day gaps = %d; want 0 for a flat 24/7 job", c.DayGaps)
	}

	v := assertPromotedAsATick(t, c)
	assertAbsenceRaised(t, db, ctx, c, v, silentUntil)
}

// assertPromotedAsATick checks the burst half of the fix: the shape promotes,
// and the period it learned is the TICK rather than the intra-burst spacing.
func assertPromotedAsATick(t *testing.T, c store.BaselineCandidate) store.BaselineVerdict {
	t.Helper()
	v := store.EvaluateBaselineCandidate(c)
	if v.Decision != store.BaselinePromoted {
		t.Fatalf("decision = %q (%s); the measured production shape MUST promote",
			v.Decision, v.Reason)
	}
	if !v.Stats.Bursty {
		t.Errorf("stats.Bursty = false; the arrival sample is bunched and must be read as ticks")
	}
	if got := v.Stats.Median; got < 290 || got > 310 {
		t.Errorf("learned period = %.1fs; want the ~300s tick, not the 1s intra-burst gap", got)
	}
	// Sized off the raw 1s median instead, the window would collapse to its
	// 5-minute floor and alert on every ordinary quiet stretch between ticks.
	if v.Window < 20*time.Minute {
		t.Errorf("absence window = %s; too tight for a 5-minute tick", v.Window)
	}
	t.Logf("PROMOTED: %s", v.Reason)
	return v
}

// assertAbsenceRaised drives the promoted rule through the real observation
// query and the real evaluator after the job has gone silent.
func assertAbsenceRaised(
	t *testing.T, db *sqlite.DB, ctx context.Context,
	c store.BaselineCandidate, v store.BaselineVerdict, evalAt time.Time,
) {
	t.Helper()
	rule := store.ProposeExpectedSignal(c, v)
	if err := db.CreateMonitoringExpectedSignal(ctx, rule); err != nil {
		t.Fatalf("create learned rule: %v", err)
	}
	obs, health, err := db.ObserveExpectedSignal(ctx, rule, evalAt)
	if err != nil {
		t.Fatalf("observe: %v", err)
	}
	if obs.MatchCount != 0 {
		t.Fatalf("match count = %d; the job stopped at %s so its window must be empty",
			obs.MatchCount, baselineFixtureNow)
	}
	if obs.TotalLines == 0 {
		t.Fatal("source produced no lines at all; the scenario cannot distinguish " +
			"a stopped job from a stopped collector")
	}
	// The rule really was created a week ago and really has seen its signal,
	// so neither the warm-up nor the bootstrap guard applies.
	rule.CreatedAt = baselineFixtureNow.Add(-acceptRetention)
	lastSignal := baselineFixtureNow
	rule.LastSignalAt = &lastSignal
	d := store.EvaluateExpectedSignal(store.ExpectedSignalInput{
		Rule: *rule, Observed: obs, Health: health, Now: evalAt, Location: time.UTC,
	})
	if !d.Raise || d.Outcome != store.OutcomeSignalAbsent {
		t.Fatalf("outcome = %q raise=%v; want a raised absence — %s",
			d.Outcome, d.Raise, d.Detail)
	}
	t.Logf("ABSENCE RAISED: %s — %s", d.Title, d.Detail)
}

func findCandidate(
	t *testing.T, db *sqlite.DB, ctx context.Context, src *store.LogSource, key string,
) store.BaselineCandidate {
	t.Helper()
	for _, c := range mineBaseline(t, db, ctx, src) {
		if c.TemplateID == key {
			return c
		}
	}
	t.Fatalf("cadence %s was not mined at all", key)
	return store.BaselineCandidate{}
}
