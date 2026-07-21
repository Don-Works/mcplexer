package admin_test

import (
	"math"
	"testing"

	"github.com/don-works/mcplexer/internal/workers/admin"
)

// TestExplorationBonusDecaysWithRuns proves the UCB-style optimism is largest
// for a brand-new (0-run) candidate and decays as 1/sqrt(runs+1) so explore
// hands off to exploit as the model accrues its own runs.
func TestExplorationBonusDecaysWithRuns(t *testing.T) {
	zero := admin.ExplorationBonusForTest(0, 0, 0)
	four := admin.ExplorationBonusForTest(4, 4, 0)
	nine := admin.ExplorationBonusForTest(9, 9, 0)
	thirty := admin.ExplorationBonusForTest(30, 30, 0)

	if !(zero > four && four > nine && nine > thirty) {
		t.Fatalf("bonus must decay monotonically: 0=%.2f 4=%.2f 9=%.2f 30=%.2f", zero, four, nine, thirty)
	}
	// 1/sqrt(runs+1) shape against the configured weight.
	if math.Abs(zero-four*math.Sqrt(5)) > 0.01 {
		t.Fatalf("decay shape broken: zero=%.2f four=%.2f (four*sqrt5=%.2f)", zero, four, four*math.Sqrt(5))
	}
	if math.Abs(zero-nine*math.Sqrt(10)) > 0.01 {
		t.Fatalf("decay shape broken: zero=%.2f nine=%.2f (nine*sqrt10=%.2f)", zero, nine, nine*math.Sqrt(10))
	}
	// By the time a model has accrued plenty of its own runs, the bonus has
	// shrunk to a small fraction of its initial optimism so exploit dominates.
	if thirty > zero/4 {
		t.Fatalf("settled (30-run) bonus = %.2f, want < quarter of initial %.2f so exploit dominates", thirty, zero)
	}
}

// TestCapacityDefaultsToProvenIncumbent keeps exploration opt-in: a newly
// registered, unreviewed model is surfaced as under-sampled but does not
// displace reviewed production capacity before any evidence exists.
func TestCapacityDefaultsToProvenIncumbent(t *testing.T) {
	// Proven incumbent: 30 reliable runs and 12 strong reviews.
	incumbent := admin.CapacityScoreWithReliabilityForTest(
		"anthropic", "claude-sonnet-4-6", "", "",
		30, 30, 0, 0, 0, 0, 12, 85, "coding",
	)
	// Brand-new frontier model just added to a profile: 0 runs, 0 reviews.
	newModel := admin.CapacityScoreWithReliabilityForTest(
		"anthropic", "claude-fable-5", "", "",
		0, 0, 0, 0, 0, 0, 0, 0, "coding",
	)
	if newModel >= incumbent {
		t.Fatalf("new 0-run model %.2f must remain BELOW proven incumbent %.2f until explicitly evaluated", newModel, incumbent)
	}

	// After the new model has accrued plenty of its own runs but still has no
	// reviews, operational success alone still must not promote it above
	// reviewed quality evidence.
	settledNew := admin.CapacityScoreWithReliabilityForTest(
		"anthropic", "claude-fable-5", "", "",
		30, 30, 0, 0, 0, 0, 0, 0, "coding",
	)
	if settledNew >= incumbent {
		t.Fatalf("settled unreviewed model %.2f must NOT outrank proven reviewed incumbent %.2f", settledNew, incumbent)
	}
}

// TestReviewConfidenceStopsSingleLuckyReview proves the confidence prior:
// one perfect review is useful evidence, but it cannot immediately outrank a
// model with many independently strong reviews and equally reliable runs.
func TestReviewConfidenceStopsSingleLuckyReview(t *testing.T) {
	onePerfect := admin.CapacityScoreWithReliabilityForTest(
		"anthropic", "claude-new", "", "",
		30, 30, 0, 0, 0, 0, 1, 100, "coding",
	)
	provenStrong := admin.CapacityScoreWithReliabilityForTest(
		"grok_cli", "grok-proven", "", "",
		30, 30, 0, 0, 0, 0, 12, 85, "coding",
	)
	if onePerfect >= provenStrong {
		t.Fatalf("one perfect review score %.2f must remain below proven strong score %.2f", onePerfect, provenStrong)
	}
}

// TestAntiThrashStopsExploringBrokenModel proves the guard: a candidate that
// keeps dying at the adapter/launch stage (operational failures) with no
// successful run loses its exploration bonus, so a genuinely-broken model
// (e.g. mimo 400 "Not supported model") is not force-explored forever.
func TestAntiThrashStopsExploringBrokenModel(t *testing.T) {
	// One operational failure, no success: still under the cutoff, still
	// optimistically explored (give it a couple of chances).
	earlyBonus := admin.ExplorationBonusForTest(1, 0, 1)
	if earlyBonus <= 0 {
		t.Fatalf("after a single launch failure the model should still be explored, bonus=%.2f", earlyBonus)
	}
	earlyUnder, earlyThrash := admin.ExplorationStateForTest(1, 0, 1)
	if !earlyUnder || earlyThrash {
		t.Fatalf("1 op-failure/0-success: underSampled=%v thrashed=%v, want true/false", earlyUnder, earlyThrash)
	}

	// Three operational failures, still no success: anti-thrash trips. No
	// exploration bonus, no "exploring" marker.
	thrashedBonus := admin.ExplorationBonusForTest(3, 0, 3)
	if thrashedBonus != 0 {
		t.Fatalf("broken model (3 op-failures, 0 success) bonus = %.2f, want 0", thrashedBonus)
	}
	under, thrashed := admin.ExplorationStateForTest(3, 0, 3)
	if under || !thrashed {
		t.Fatalf("3 op-failures/0-success: underSampled=%v thrashed=%v, want false/true", under, thrashed)
	}

	// A model that launched broken a few times but then SUCCEEDED is not
	// thrashed — it gets to keep being explored.
	recoveredBonus := admin.ExplorationBonusForTest(4, 1, 3)
	if recoveredBonus <= 0 {
		t.Fatalf("model with 3 op-failures but 1 success must still be explored, bonus=%.2f", recoveredBonus)
	}
}

// TestOperationalQuarantineTripsForMostlyBadRecoveredTransport covers the
// post-incident case: a provider that occasionally succeeds but mostly dies at
// adapter/launch time must not stay selectable forever just because success > 0.
func TestOperationalQuarantineTripsForMostlyBadRecoveredTransport(t *testing.T) {
	if admin.OperationalQuarantinedForTest(4, 1, 3, 3) {
		t.Fatal("below the hard cutoff should not quarantine yet")
	}
	if !admin.OperationalQuarantinedForTest(10, 2, 8, 6) {
		t.Fatal("mostly-bad transport with repeated operational failures must quarantine")
	}
	if admin.OperationalQuarantinedForTest(20, 16, 4, 5) {
		t.Fatal("five operational failures on an otherwise healthy model should not quarantine")
	}

	quarantined := admin.CapacityScoreWithReliabilityForTest(
		"mimo_cli", "mimo-auto", "", "",
		10, 2, 8, 0, 6, 0, 2, 88, "coding",
	)
	fresh := admin.CapacityScoreWithReliabilityForTest(
		"anthropic", "claude-fresh", "", "",
		0, 0, 0, 0, 0, 0, 0, 0, "coding",
	)
	if quarantined >= fresh {
		t.Fatalf("quarantined transport %.2f must rank below fresh viable candidate %.2f", quarantined, fresh)
	}
}

// TestBrokenModelRanksBelowEverythingViable proves the guard's end effect on
// the capacity score: a repeatedly-launch-failing model with no successes
// sinks below a fresh untried candidate AND below a proven incumbent, instead
// of being kept near the top by perpetual optimism.
func TestBrokenModelRanksBelowEverythingViable(t *testing.T) {
	broken := admin.CapacityScoreWithReliabilityForTest(
		"mimo_cli", "mimo-broken", "", "",
		4, 0, 0, 0, 4, 0, 0, 0, "coding", // 4 runs, all operational failures
	)
	fresh := admin.CapacityScoreWithReliabilityForTest(
		"anthropic", "claude-fable-5", "", "",
		0, 0, 0, 0, 0, 0, 0, 0, "coding",
	)
	proven := admin.CapacityScoreWithReliabilityForTest(
		"anthropic", "claude-sonnet-4-6", "", "",
		30, 30, 0, 0, 0, 0, 12, 85, "coding",
	)
	if broken >= fresh {
		t.Fatalf("broken model %.2f must rank below a fresh untried candidate %.2f", broken, fresh)
	}
	if broken >= proven {
		t.Fatalf("broken model %.2f must rank below the proven incumbent %.2f", broken, proven)
	}
}

func TestDispatchFailureDemotesCapacityWithoutRunRow(t *testing.T) {
	dispatchFailed := admin.CapacityScoreWithDispatchFailuresForTest(1)
	fresh := admin.CapacityScoreWithReliabilityForTest(
		"grok_cli", "fresh", "", "",
		0, 0, 0, 0, 0, 0, 0, 0, "coding",
	)
	if dispatchFailed >= fresh {
		t.Fatalf("dispatch-failed candidate %.2f must rank below fresh candidate %.2f even with runs=0", dispatchFailed, fresh)
	}
}

func TestDeliverabilityFailureDemotesCapacity(t *testing.T) {
	emptyReporter := admin.CapacityScoreWithDeliverabilityFailuresForTest(1)
	fresh := admin.CapacityScoreWithReliabilityForTest(
		"grok_cli", "fresh", "", "",
		0, 0, 0, 0, 0, 0, 0, 0, "coding",
	)
	if emptyReporter >= fresh {
		t.Fatalf("empty-report candidate %.2f must rank below fresh candidate %.2f", emptyReporter, fresh)
	}
}

// TestProvenGoodModelStillRanksTopAmongSettled confirms exploration does not
// destabilise the steady state: among models that have all settled (plenty of
// runs, exploration negligible) the highest-reviewed one still wins.
func TestProvenGoodModelStillRanksTopAmongSettled(t *testing.T) {
	excellent := admin.CapacityScoreWithReliabilityForTest(
		"anthropic", "claude-opus-4-8", "", "",
		40, 40, 0, 0, 0, 0, 20, 92, "coding",
	)
	mediocre := admin.CapacityScoreWithReliabilityForTest(
		"openai", "gpt-mid", "", "",
		40, 36, 4, 0, 0, 0, 20, 60, "coding",
	)
	if excellent <= mediocre {
		t.Fatalf("proven excellent model %.2f must still top proven mediocre %.2f at steady state", excellent, mediocre)
	}
}
