package admin_test

import (
	"math"
	"testing"

	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestDelegationRankRatesCLIMissingAccounting(t *testing.T) {
	successRate, operationalRate, rankingRate, accountingKnown := admin.DelegationRankRatesForTest(5, 5, 0, 5)
	if successRate != 0 {
		t.Fatalf("successRate = %v, want 0 when all runs lack accounting", successRate)
	}
	if operationalRate != 1 {
		t.Fatalf("operationalRate = %v, want 1 for all-success terminal runs", operationalRate)
	}
	if rankingRate != 1 {
		t.Fatalf("rankingRate = %v, want 1 for five operationally successful runs", rankingRate)
	}
	if accountingKnown {
		t.Fatal("accountingKnown = true, want false")
	}

	mixedSuccess, mixedOperational, mixedRanking, mixedKnown := admin.DelegationRankRatesForTest(4, 3, 1, 1)
	if math.Abs(mixedSuccess-2.0/3.0) > 0.0001 {
		t.Fatalf("mixed successRate = %v, want 2/3 over known terminal runs", mixedSuccess)
	}
	if mixedOperational != 0.75 {
		t.Fatalf("mixed operationalRate = %v, want 0.75", mixedOperational)
	}
	if mixedRanking != 1 {
		t.Fatalf("mixed rankingRate = %v, want 1 because the quality failure is not an adapter failure", mixedRanking)
	}
	if !mixedKnown {
		t.Fatal("mixed accountingKnown = false, want true")
	}

	grokSuccess, grokOperational, grokRanking, grokKnown := admin.DelegationRankRatesWithOperationalFailuresForTest(7, 5, 2, 5, 2)
	if grokSuccess != 0 {
		t.Fatalf("grok-like successRate = %v, want 0 when successful runs lack accounting", grokSuccess)
	}
	if math.Abs(grokOperational-5.0/7.0) > 0.0001 {
		t.Fatalf("grok-like operationalRate = %v, want 5/7", grokOperational)
	}
	if math.Abs(grokRanking-5.0/7.0) > 0.0001 {
		t.Fatalf("grok-like rankingRate = %v, want 5/7 independent of cost accounting", grokRanking)
	}
	if grokKnown {
		t.Fatal("grok-like accountingKnown = true, want false")
	}
}

func TestCapacityScoreUsesOperationalReliabilityWhenAccountingMissing(t *testing.T) {
	// One 80-point review is confidence-shrunk toward 58: 62.4. Three
	// successful CLI runs then add +10 reliability and +5 quality, while
	// missing cost telemetry gets the flat -4 accounting penalty.
	cliScore := admin.CapacityScoreForCandidateForTest(3, 3, 0, 3, 1, 80, "coding")
	const want = 73.4
	if math.Abs(cliScore-want) > 0.01 {
		t.Fatalf("cli score %.2f, want %.2f from independent reliability/quality/accounting axes", cliScore, want)
	}
}

func TestCapacityScoreDemotesUnreviewedPiAndLocalCandidates(t *testing.T) {
	reviewedWorkhorse := admin.CapacityScoreForModelCandidateForTest(
		"anthropic", "claude-sonnet-4-5", "", "",
		1, 1, 0, 0, 1, 55, "coding",
	)
	unreviewedRemote := admin.CapacityScoreForModelCandidateForTest(
		"openai_compat", "qwen/qwen3.7-plus", "https://openrouter.ai/api/v1", "",
		2, 2, 0, 0, 0, 0, "coding",
	)
	unreviewedPi := admin.CapacityScoreForModelCandidateForTest(
		"pi_cli", "qwen-local", "", "Pi harness qwen-local",
		2, 2, 0, 0, 0, 0, "coding",
	)
	unreviewedLocal := admin.CapacityScoreForModelCandidateForTest(
		"openai_compat", "qwen3-coder", "http://127.0.0.1:1234/v1", "",
		2, 2, 0, 0, 0, 0, "coding",
	)

	if unreviewedRemote >= reviewedWorkhorse {
		t.Fatalf("unreviewed remote score %.2f must not outrank reviewed workhorse %.2f", unreviewedRemote, reviewedWorkhorse)
	}
	if unreviewedPi >= unreviewedRemote {
		t.Fatalf("unreviewed pi score %.2f must be below unreviewed remote %.2f", unreviewedPi, unreviewedRemote)
	}
	if unreviewedLocal >= unreviewedRemote {
		t.Fatalf("unreviewed local score %.2f must be below unreviewed remote %.2f", unreviewedLocal, unreviewedRemote)
	}
}
