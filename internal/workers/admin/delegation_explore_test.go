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

// TestNewModelFloatsAboveProvenIncumbentThenSettles is the core cold-start
// fix: a freshly-registered 0-run unreviewed model must out-score a proven
// reviewed incumbent (so it is SELECTED and accrues runs), then once it has
// accrued enough runs of its own the incumbent's proven quality wins again.
func TestNewModelFloatsAboveProvenIncumbentThenSettles(t *testing.T) {
	// Proven incumbent: 30 runs, reviewed, strong EWMA 85. Exploration bonus
	// is negligible (45/sqrt(31) ~ 8).
	incumbent := admin.CapacityScoreWithReliabilityForTest(
		"anthropic", "claude-sonnet-4-6", "", "",
		30, 30, 0, 0, 0, 0, 12, 85, "coding",
	)
	// Brand-new frontier model just added to a profile: 0 runs, 0 reviews.
	newModel := admin.CapacityScoreWithReliabilityForTest(
		"anthropic", "claude-fable-5", "", "",
		0, 0, 0, 0, 0, 0, 0, 0, "coding",
	)
	if newModel <= incumbent {
		t.Fatalf("new 0-run model %.2f must float ABOVE proven incumbent %.2f so it gets scheduled", newModel, incumbent)
	}

	// After the new model has accrued plenty of its own runs but still has no
	// reviews, the proven reviewed incumbent must reclaim the top spot —
	// exploration has handed off to exploit.
	settledNew := admin.CapacityScoreWithReliabilityForTest(
		"anthropic", "claude-fable-5", "", "",
		30, 30, 0, 0, 0, 0, 0, 0, "coding",
	)
	if settledNew >= incumbent {
		t.Fatalf("settled unreviewed model %.2f must NOT outrank proven reviewed incumbent %.2f once exploration decays", settledNew, incumbent)
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
