package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestDeployGapIsSubtractedFromTheBaseline is the acceptance test for the
// evidence-subtraction rule.
//
// A deploy stops the service for several minutes. That pause is a KNOWN CAUSE,
// and the rule is that a known cause is subtracted from evidence rather than
// used to filter output. So the restart gap must never enter the gap sample at
// all — not be recorded and then forgiven, and not suppress an alert while it
// sits there. The observable consequence is that the learned period and the
// absence window are identical whether or not the deploy happened.
func TestDeployGapIsSubtractedFromTheBaseline(t *testing.T) {
	withOutDeploy := mineDeployScenario(t, false)
	withDeploy := mineDeployScenario(t, true)

	if withDeploy.DeployGapsExcised == 0 {
		t.Fatal("the restart gap was not excised; it entered the baseline as evidence")
	}
	if withOutDeploy.DeployGapsExcised != 0 {
		t.Errorf("excised %d gaps with no deploy present", withOutDeploy.DeployGapsExcised)
	}

	clean := store.EvaluateBaselineCandidate(withOutDeploy)
	deployed := store.EvaluateBaselineCandidate(withDeploy)

	if deployed.Decision != store.BaselinePromoted {
		t.Fatalf("decision = %q (%s); a deploy must not stop a job being learned",
			deployed.Decision, deployed.Reason)
	}
	// The point of the whole exercise: the restart is invisible in the result.
	if deployed.Stats.Median != clean.Stats.Median {
		t.Errorf("period %.1fs with a deploy vs %.1fs without; the restart leaked "+
			"into what the system believes normal is",
			deployed.Stats.Median, clean.Stats.Median)
	}
	if deployed.Window != clean.Window {
		t.Errorf("absence window %s with a deploy vs %s without; a restart must not "+
			"widen the alarm threshold", deployed.Window, clean.Window)
	}
	t.Logf("excised %d restart gap(s); period %.0fs and window %s unchanged",
		withDeploy.DeployGapsExcised, deployed.Stats.Median, deployed.Window)
}

// TestSignalStoppedByADeployStillPromotesAndGoesAbsent is the case the operator
// actually cares about: a deploy that KILLS the job. Excising the restart gap
// must not excise the silence that follows it.
func TestSignalStoppedByADeployStillPromotesAndGoesAbsent(t *testing.T) {
	c := mineDeployScenario(t, true)
	v := store.EvaluateBaselineCandidate(c)
	if v.Decision != store.BaselinePromoted {
		t.Fatalf("decision = %q; %s", v.Decision, v.Reason)
	}
	rule := store.ProposeExpectedSignal(c, v)
	rule.CreatedAt = baselineFixtureNow.Add(-14 * 24 * time.Hour)
	last := baselineFixtureNow
	rule.LastSignalAt = &last

	// Long after the deploy, with the job never having come back. Nothing
	// anywhere waits out a window: there is no window.
	at := baselineFixtureNow.Add(3 * time.Hour)
	d := store.EvaluateExpectedSignal(store.ExpectedSignalInput{
		Rule: *rule, Now: at, Location: time.UTC,
		Observed: store.ExpectedSignalObservation{TotalLines: 60, MatchCount: 0, LastMatchAt: &last},
		Health:   store.SourceCollectionHealth{Enabled: true},
	})
	if !d.Raise || d.Outcome != store.OutcomeSignalAbsent {
		t.Fatalf("outcome = %q raise=%v; a job killed by a deploy must alert — %s",
			d.Outcome, d.Raise, d.Detail)
	}
	t.Logf("RAISED: %s", d.Detail)
}

// mineDeployScenario seeds the real bursty job over 7 days, optionally with a
// service restart 3 days in (a startup banner plus a genuine hole in output),
// and returns the mined candidate.
func mineDeployScenario(t *testing.T, withDeploy bool) store.BaselineCandidate {
	t.Helper()
	db := newTestDB(t)
	ctx := context.Background()
	src := seedBaselineSource(t, db, ctx)

	const masked = "order sync completed batch=<n>"
	const raw = "order sync completed batch=418"
	start := baselineFixtureNow.Add(-acceptRetention)
	restart := baselineFixtureNow.Add(-acceptRedeployAgo)
	// The restart is long relative to the 5-minute tick, so if it were left
	// in the sample it would be a clear outlier in the gap distribution.
	const restartFor = 12 * time.Minute
	seedTemplateRow(t, db, ctx, src, "tpl-sync", masked, start, baselineFixtureNow)

	lines := []store.LogLine{}
	for i := 0; ; i++ {
		tick := start.Add(time.Duration(i) * acceptTick)
		if tick.After(baselineFixtureNow) {
			break
		}
		if withDeploy && !tick.Before(restart) && tick.Before(restart.Add(restartFor)) {
			continue // the service is down; it emits nothing at all
		}
		n := 3
		if i%3 == 0 {
			n = 4
		}
		for j := 0; j < n; j++ {
			lines = append(lines, store.LogLine{
				SourceID: src.ID, TemplateID: "tpl-sync",
				TS: tick.Add(time.Duration(j) * acceptIntra), Line: raw,
			})
		}
	}
	if err := db.InsertLogLines(ctx, lines); err != nil {
		t.Fatalf("insert lines: %v", err)
	}
	if withDeploy {
		seedDeployLine(t, db, ctx, src, "tpl-banner",
			"info api/main.go:<n> running version: v<n>.<n>.<n>",
			"info api/main.go:159 running version: v5.7.7",
			store.SeverityInfo, 1, restart.Add(restartFor))
	}
	return findCandidate(t, db, ctx, src, store.CadenceKey(src.ID, masked))
}
