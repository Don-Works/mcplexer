package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// baselineFixtureNow is a fixed instant so the mined spans are reproducible.
var baselineFixtureNow = time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)

// baselineSeedSpan is how much history the fixtures write. It exceeds the
// 14-day mining horizon on purpose: the long-horizon day table is populated by
// the trigger on log_lines, so writing three weeks of arrivals is what a real
// three weeks of daemon operation would have produced, and the weekly-shape
// gate is then exercised against real rows rather than a hand-seeded table.
const baselineSeedSpan = 21 * 24 * time.Hour

func seedBaselineSource(t *testing.T, db *sqlite.DB, ctx context.Context) *store.LogSource {
	t.Helper()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	h := seedRemoteHost(t, db, ctx, wsID, scopeID)
	src := &store.LogSource{
		WorkspaceID: wsID, RemoteHostID: h.ID,
		Name: "orders-api", Selector: "orders-api", Enabled: true,
	}
	if err := db.CreateLogSource(ctx, src); err != nil {
		t.Fatalf("create source: %v", err)
	}
	return src
}

// seedPeriodicTemplate writes a template plus lines arriving every `period`
// across baselineSeedSpan, ending at baselineFixtureNow. This is the real
// 2026-07-20 shape: a recurring job whose completion line lands on a cadence.
func seedPeriodicTemplate(
	t *testing.T, db *sqlite.DB, ctx context.Context,
	src *store.LogSource, templateID, masked, raw string, period time.Duration,
) {
	t.Helper()
	start := baselineFixtureNow.Add(-baselineSeedSpan)
	tpl := &store.LogTemplate{
		ID: templateID, SourceID: src.ID, Masked: masked,
		Severity: store.SeverityInfo, FirstSeen: start, LastSeen: baselineFixtureNow,
	}
	if _, err := db.UpsertLogTemplate(ctx, tpl, 1); err != nil {
		t.Fatalf("upsert template: %v", err)
	}
	lines := []store.LogLine{}
	for ts := start; !ts.After(baselineFixtureNow); ts = ts.Add(period) {
		lines = append(lines, store.LogLine{
			SourceID: src.ID, TemplateID: templateID, TS: ts, Line: raw,
		})
	}
	if err := db.InsertLogLines(ctx, lines); err != nil {
		t.Fatalf("insert lines: %v", err)
	}
}

func mineBaseline(
	t *testing.T, db *sqlite.DB, ctx context.Context, src *store.LogSource,
) []store.BaselineCandidate {
	t.Helper()
	candidates, err := db.MineBaselineCandidates(ctx, src,
		baselineFixtureNow.Add(-store.BaselineLearnHorizon), baselineFixtureNow)
	if err != nil {
		t.Fatalf("mine: %v", err)
	}
	return candidates
}

// TestMineBaselineCandidatesFindsRecurringJob is the store-level acceptance:
// real log rows go in, and the mined evidence is enough for the pure learner to
// promote a rule — with nobody having configured anything.
func TestMineBaselineCandidatesFindsRecurringJob(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	src := seedBaselineSource(t, db, ctx)
	seedPeriodicTemplate(t, db, ctx, src, "tpl-sync",
		"order sync completed batch=<n>", "order sync completed batch=418",
		10*time.Minute)

	candidates := mineBaseline(t, db, ctx, src)
	if len(candidates) != 1 {
		t.Fatalf("mined %d candidates; want 1", len(candidates))
	}
	c := candidates[0]
	// Candidates are identified by CADENCE KEY, not template id, so a job's
	// history survives the redeploys that mint a new template id for it.
	wantKey := store.CadenceKey(src.ID, "order sync completed batch=<n>")
	if c.WorkspaceID != src.WorkspaceID || c.TemplateID != wantKey {
		t.Errorf("candidate identity = %s/%s; want %s/%s",
			c.WorkspaceID, c.TemplateID, src.WorkspaceID, wantKey)
	}
	if c.MatchSubstring != "order sync completed batch=" {
		t.Errorf("derived matcher = %q; want the literal run before the mask",
			c.MatchSubstring)
	}
	if c.SubstringMatches != c.SubstringTemplateLines {
		t.Errorf("matcher verification: %d matches against %d template lines; want equal",
			c.SubstringMatches, c.SubstringTemplateLines)
	}
	// The day table is written by the trigger on log_lines, so three weeks of
	// seeded arrivals must show three weeks of gap-free day history.
	if c.DayHistoryDays < store.BaselineMinDayHistoryDays || c.DayGaps != 0 {
		t.Errorf("day history = %d days with %d gaps; want >= %d and 0",
			c.DayHistoryDays, c.DayGaps, store.BaselineMinDayHistoryDays)
	}
	if c.HourBucketsSeen != c.HourBucketsTotal {
		t.Errorf("hour occupancy = %d/%d; a ten-minute job should fill every hour",
			c.HourBucketsSeen, c.HourBucketsTotal)
	}

	verdict := store.EvaluateBaselineCandidate(c)
	if verdict.Decision != store.BaselinePromoted {
		t.Fatalf("decision = %q (%s); want promoted", verdict.Decision, verdict.Reason)
	}
	if verdict.Window != time.Hour {
		t.Errorf("window = %s; want 1h for a ten-minute cadence", verdict.Window)
	}
	rule := store.ProposeExpectedSignal(c, verdict)
	if err := store.ValidateMonitoringExpectedSignal(rule); err != nil {
		t.Errorf("the rule mined from real rows is invalid: %v", err)
	}
}

// TestMineBaselineCandidatesSkipsRareTemplates proves the shortlist keeps the
// pass bounded: a template below the minimum sample is never even measured.
func TestMineBaselineCandidatesSkipsRareTemplates(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	src := seedBaselineSource(t, db, ctx)
	// Six-hourly: only 56 arrivals inside the 14-day horizon, below the
	// minimum sample, so the shortlist never picks it up.
	seedPeriodicTemplate(t, db, ctx, src, "tpl-rare",
		"nightly reconcile finished <dur>", "nightly reconcile finished 4.2s",
		6*time.Hour)

	if candidates := mineBaseline(t, db, ctx, src); len(candidates) != 0 {
		t.Errorf("mined %d candidates from a rare template; want 0", len(candidates))
	}
}

// TestMineBaselineCandidatesDetectsOverBroadMatcher covers matcher verification
// against real rows: two templates sharing a literal prefix must be caught as
// an over-broad matcher rather than promoted blind.
func TestMineBaselineCandidatesDetectsOverBroadMatcher(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	src := seedBaselineSource(t, db, ctx)
	// The shared literal run must clear BaselineMinSubstringLen, or the
	// candidate is rejected as "no matcher" before verification ever runs.
	seedPeriodicTemplate(t, db, ctx, src, "tpl-a",
		"order sync batch <n> ok", "order sync batch 1 ok", 10*time.Minute)
	seedPeriodicTemplate(t, db, ctx, src, "tpl-b",
		"order sync batch <n> retried", "order sync batch 2 retried", 10*time.Minute)

	// Distinct masked text means distinct cadence keys: grouping collapses
	// releases of ONE job, never two genuinely different templates.
	keyA := store.CadenceKey(src.ID, "order sync batch <n> ok")
	found := false
	for _, c := range mineBaseline(t, db, ctx, src) {
		if c.TemplateID != keyA {
			continue
		}
		found = true
		// "order sync " is the longest literal run and it matches BOTH
		// templates, so the measured match count is roughly double.
		if c.SubstringMatches <= c.SubstringTemplateLines {
			t.Fatalf("matcher %q matched %d of %d lines; expected it to sweep in the sibling",
				c.MatchSubstring, c.SubstringMatches, c.SubstringTemplateLines)
		}
		if v := store.EvaluateBaselineCandidate(c); v.Decision != store.BaselineRejectMatcherUnverified {
			t.Errorf("decision = %q; an over-broad matcher must not be promoted", v.Decision)
		}
	}
	if !found {
		t.Fatal("tpl-a was not mined")
	}
}

// TestMineBaselineCandidatesRefusesUnhealthySource proves the learner is never
// taught by a broken collector.
func TestMineBaselineCandidatesRefusesUnhealthySource(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	src := seedBaselineSource(t, db, ctx)
	seedPeriodicTemplate(t, db, ctx, src, "tpl-sync",
		"order sync completed batch=<n>", "order sync completed batch=418",
		10*time.Minute)
	if err := db.SetLogSourceFailures(ctx, src.ID, 2); err != nil {
		t.Fatalf("set failures: %v", err)
	}

	candidates := mineBaseline(t, db, ctx, src)
	if len(candidates) == 0 {
		t.Fatal("expected the candidate to be mined and then rejected on health")
	}
	if v := store.EvaluateBaselineCandidate(candidates[0]); v.Decision != store.BaselineRejectCollectionUnhealthy {
		t.Errorf("decision = %q; want collection_unhealthy", v.Decision)
	}
}

func TestSignalBaselineUpsertAndList(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	src := seedBaselineSource(t, db, ctx)

	b := &store.SignalBaseline{
		WorkspaceID: src.WorkspaceID, SourceID: src.ID, TemplateID: "tpl-sync",
		Masked: "order sync completed batch=<n>", MatchSubstring: "order sync completed batch=",
		Decision: store.BaselinePromoted, Reason: "recurring every 10m0s",
		PeriodSeconds: 600, P95Seconds: 620, MADSeconds: 10, RelativeMAD: 0.017,
		P95Ratio: 1.03, SampleCount: 432, CyclesObserved: 480, HourOccupancy: 1,
		SpanSeconds: 288000, Confidence: 0.92, WindowSeconds: 3600,
		FirstSeen: baselineFixtureNow.Add(-80 * time.Hour), LastSeen: baselineFixtureNow,
		ObservedAt: baselineFixtureNow,
	}
	if err := db.UpsertSignalBaseline(ctx, b); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := db.GetSignalBaselineByTemplate(ctx, "tpl-sync")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Decision != store.BaselinePromoted || got.PeriodSeconds != 600 {
		t.Errorf("round trip lost evidence: %+v", got)
	}
	if got.LearnedRuns != 0 {
		t.Errorf("learned_runs = %d on first write; want 0", got.LearnedRuns)
	}
	if !got.FirstSeen.Equal(b.FirstSeen) {
		t.Errorf("first_seen = %s; want %s", got.FirstSeen, b.FirstSeen)
	}

	// A second pass on the same template must converge on one row and count
	// the run, not accumulate near-duplicate judgements.
	b.ID, b.Confidence = "", 0.95
	if err := db.UpsertSignalBaseline(ctx, b); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	list, err := db.ListSignalBaselines(ctx, src.WorkspaceID, 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("listed %d baselines after two passes; want 1", len(list))
	}
	if list[0].LearnedRuns != 1 {
		t.Errorf("learned_runs = %d after a second pass; want 1", list[0].LearnedRuns)
	}
	if list[0].Confidence != 0.95 {
		t.Errorf("confidence = %.2f; want the refreshed 0.95", list[0].Confidence)
	}

	scoped, err := db.ListSignalBaselinesForSource(ctx, src.ID, 0)
	if err != nil {
		t.Fatalf("list for source: %v", err)
	}
	if len(scoped) != 1 {
		t.Errorf("source-scoped list returned %d; want 1", len(scoped))
	}
}

// TestSignalBaselineRecordsRejections is the inspectability guarantee: "why is
// there no alert for this job" must have a stored answer.
func TestSignalBaselineRecordsRejections(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	src := seedBaselineSource(t, db, ctx)

	rejected := &store.SignalBaseline{
		WorkspaceID: src.WorkspaceID, SourceID: src.ID, TemplateID: "tpl-noise",
		Decision:   store.BaselineRejectIrregular,
		Reason:     "arrivals are not periodic: regularity 0.702 (max 0.35)",
		ObservedAt: baselineFixtureNow,
	}
	if err := db.UpsertSignalBaseline(ctx, rejected); err != nil {
		t.Fatalf("upsert rejection: %v", err)
	}
	got, err := db.GetSignalBaselineByTemplate(ctx, "tpl-noise")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.RuleID != "" {
		t.Errorf("rejected baseline points at rule %q", got.RuleID)
	}
	if got.Reason == "" {
		t.Error("a rejection with no reason is exactly the shrug this table exists to avoid")
	}
	if !got.FirstSeen.IsZero() {
		t.Errorf("first_seen = %s; an unobserved boundary must round-trip as zero, "+
			"not as year 1", got.FirstSeen)
	}
}
