package admin

import (
	"fmt"
	"testing"
)

func TestCandidateBetterThanUsesQualityBeforeCost(t *testing.T) {
	good := delegationCandidateRank{
		reviewCount:    10,
		reviews:        10,
		scoreTotal:     800,
		recencyScore:   80,
		runs:           10,
		success:        10,
		qualitySuccess: 10,
		costUSD:        10,
	}
	bad := delegationCandidateRank{
		reviewCount:    10,
		reviews:        10,
		scoreTotal:     800,
		recencyScore:   80,
		runs:           10,
		failure:        10,
		qualityFailure: 10,
		costUSD:        1,
	}
	if !good.betterThan(&bad, "coding") {
		t.Fatal("quality-success candidate should beat equally reviewed cheaper quality-failure candidate")
	}
	if bad.betterThan(&good, "coding") {
		t.Fatal("quality-failure candidate should not beat equally reviewed quality-success candidate")
	}
}

func TestCandidateBetterThanDoesNotRewardMissingSpeedAccounting(t *testing.T) {
	known := delegationCandidateRank{
		reviewCount:    1,
		reviews:        1,
		scoreTotal:     80,
		recencyScore:   80,
		runs:           1,
		success:        1,
		qualitySuccess: 1,
		costUSD:        0.1,
		durationMS:     100,
	}
	missing := known
	missing.unknownCostRuns = 1
	missing.unknownSuccessRuns = 1
	missing.costUSD = 0
	missing.durationMS = 0
	if !known.betterThan(&missing, "coding") {
		t.Fatal("known-accounting candidate should beat equally capable all-missing candidate")
	}
	if missing.betterThan(&known, "coding") {
		t.Fatal("all-missing candidate must not win through apparent zero cost or duration")
	}
}

func TestConfidenceAdjustedReviewScoreIsBoundedAndMonotonic(t *testing.T) {
	for _, raw := range []int{0, 58, 100} {
		t.Run(fmt.Sprintf("raw_%d", raw), func(t *testing.T) {
			previous := 58.0
			for reviews := 1; reviews <= 100; reviews++ {
				rank := delegationCandidateRank{
					reviewCount:  reviews,
					reviews:      reviews,
					scoreTotal:   raw * reviews,
					recencyScore: float64(raw),
				}
				got := rank.confidenceAdjustedReviewScore("")
				if got < 0 || got > 100 {
					t.Fatalf("reviews=%d score=%f, want within [0,100]", reviews, got)
				}
				switch {
				case raw < 58 && got > previous:
					t.Fatalf("reviews=%d score=%f increased from %f below the prior", reviews, got, previous)
				case raw > 58 && got < previous:
					t.Fatalf("reviews=%d score=%f decreased from %f above the prior", reviews, got, previous)
				case raw == 58 && got != 58:
					t.Fatalf("reviews=%d score=%f, want prior 58", reviews, got)
				}
				previous = got
			}
		})
	}
}
