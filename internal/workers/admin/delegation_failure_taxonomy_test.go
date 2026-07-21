package admin_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestModelStatsSeparatesOperationalBudgetAndQualityFailures(t *testing.T) {
	worker := func(status, errText string, input, output int) admin.DelegationWorkerContext {
		return admin.DelegationWorkerContext{
			Worker: &store.Worker{ModelProvider: "mimo_cli", ModelID: "xiaomi/mimo-v2.5-pro"},
			LatestRun: &store.WorkerRun{
				ModelProvider: "mimo_cli",
				ModelID:       "xiaomi/mimo-v2.5-pro",
				Status:        status,
				Error:         errText,
				InputTokens:   input,
				OutputTokens:  output,
			},
		}
	}
	d := admin.DelegationContext{Workers: []admin.DelegationWorkerContext{
		worker("success", "", 100, 20),
		worker("failure", "adapter send: process exited before first turn", 0, 0),
		worker("cap_exceeded", "max tool calls exceeded", 400, 60),
		worker("failure", "model returned an invalid patch", 200, 30),
	}}

	stats := admin.ModelStatsForDelegationForTest(d)
	if len(stats) != 1 {
		t.Fatalf("stats len = %d, want 1", len(stats))
	}
	s := stats[0]
	if s.Runs != 4 || s.Success != 1 || s.Failure != 3 {
		t.Fatalf("legacy totals = runs:%d success:%d failure:%d, want 4/1/3", s.Runs, s.Success, s.Failure)
	}
	if s.OperationalFailures != 1 {
		t.Fatalf("operational failures = %d, want 1", s.OperationalFailures)
	}
	if s.BudgetFailures != 1 {
		t.Fatalf("budget failures = %d, want 1", s.BudgetFailures)
	}
	if s.QualitySuccess != 1 || s.QualityFailure != 1 || s.QualityRate != 0.5 {
		t.Fatalf("quality = success:%d failure:%d rate:%v, want 1/1/0.5", s.QualitySuccess, s.QualityFailure, s.QualityRate)
	}
	if s.ReliabilityRate != 0.75 {
		t.Fatalf("reliability rate = %v, want 0.75", s.ReliabilityRate)
	}
}

func TestModelStatsSeparatesPreAndPostExecuteBlocks(t *testing.T) {
	worker := func(errText string) admin.DelegationWorkerContext {
		return admin.DelegationWorkerContext{
			Worker: &store.Worker{ModelProvider: "grok_cli", ModelID: "grok-4.5"},
			LatestRun: &store.WorkerRun{
				ModelProvider: "grok_cli",
				ModelID:       "grok-4.5",
				Status:        "blocked",
				Error:         errText,
				DurationMS:    200,
			},
		}
	}

	pre := admin.ModelStatsForDelegationForTest(admin.DelegationContext{
		Review: admin.DelegationReview{Reviewed: true, Score: 10},
		Workers: []admin.DelegationWorkerContext{
			worker("pre-execute hook blocked the run: policy gate"),
		},
	})[0]
	if pre.Runs != 0 || pre.Failure != 0 || pre.ReviewCount != 0 {
		t.Fatalf("pre-execute block = %+v, want no model run or review attribution", pre)
	}

	legacy := admin.ModelStatsForDelegationForTest(admin.DelegationContext{
		Review: admin.DelegationReview{Reviewed: true, Score: 15},
		Workers: []admin.DelegationWorkerContext{
			worker("blocked by legacy policy without phase provenance"),
		},
	})[0]
	if legacy.Runs != 0 || legacy.Failure != 0 || legacy.ReviewCount != 0 {
		t.Fatalf("legacy block = %+v, want no model run or review attribution", legacy)
	}

	post := admin.ModelStatsForDelegationForTest(admin.DelegationContext{
		Review: admin.DelegationReview{Reviewed: true, Score: 20},
		Workers: []admin.DelegationWorkerContext{
			worker("post-execute deliverability gate blocked the run: empty final report"),
		},
	})[0]
	if post.Runs != 1 || post.Failure != 1 || post.DeliverabilityFailures != 1 {
		t.Fatalf("post-execute block = %+v, want one deliverability failure", post)
	}
	if post.ReviewCount != 1 || post.ReviewScore != 20 {
		t.Fatalf("post-execute review = count:%d score:%d, want attributable 1/20", post.ReviewCount, post.ReviewScore)
	}
	if post.UnknownCostRuns != 1 || post.AvgDurationMS != 0 {
		t.Fatalf("post-execute CLI accounting = unknown:%d avg:%d, want missing telemetry", post.UnknownCostRuns, post.AvgDurationMS)
	}
}

func TestCLICapWithoutUsageRemainsAccountingMissing(t *testing.T) {
	stats := admin.ModelStatsForDelegationForTest(admin.DelegationContext{
		Workers: []admin.DelegationWorkerContext{{
			Worker: &store.Worker{ModelProvider: "grok_cli", ModelID: "grok-4.5"},
			LatestRun: &store.WorkerRun{
				ModelProvider: "grok_cli",
				ModelID:       "grok-4.5",
				Status:        "cap_exceeded",
				Error:         "max tool calls exceeded",
				DurationMS:    500,
			},
		}},
	})
	if len(stats) != 1 || stats[0].UnknownCostRuns != 1 || stats[0].AvgDurationMS != 0 {
		t.Fatalf("capped CLI accounting = %+v, want one missing-telemetry run", stats)
	}
}

func TestAggregateMixedUnknownCapAndKnownFailureHasCostEvidence(t *testing.T) {
	agg, _ := admin.AggregateDelegationForTest(admin.DelegationContext{
		Workers: []admin.DelegationWorkerContext{
			{
				Worker: &store.Worker{ModelProvider: "grok_cli", ModelID: "grok-4.5"},
				LatestRun: &store.WorkerRun{
					ModelProvider: "grok_cli",
					ModelID:       "grok-4.5",
					Status:        "cap_exceeded",
					Error:         "max tool calls exceeded",
				},
			},
			{
				Worker: &store.Worker{ModelProvider: "anthropic", ModelID: "claude-sonnet-4-5"},
				LatestRun: &store.WorkerRun{
					ModelProvider: "anthropic",
					ModelID:       "claude-sonnet-4-5",
					Status:        "failure",
					Error:         "model returned an invalid patch",
					InputTokens:   500,
					OutputTokens:  50,
					CostUSD:       0.25,
				},
			},
		},
	})
	if agg.UnknownCostRuns != 1 {
		t.Fatalf("unknown cost runs = %d, want 1", agg.UnknownCostRuns)
	}
	if agg.CostAllMissing {
		t.Fatal("CostAllMissing = true, want false when a failed sibling has usage telemetry")
	}
}

func TestRankAccountingSeparatesUnknownSuccessesAndFailures(t *testing.T) {
	tests := []struct {
		name                string
		runs                int
		success             int
		failure             int
		running             int
		unknownCostRuns     int
		unknownSuccessRuns  int
		wantSuccessRate     float64
		wantAccountingKnown bool
	}{
		{
			name: "known success and unknown cap",
			runs: 2, success: 1, failure: 1,
			unknownCostRuns: 1, unknownSuccessRuns: 0,
			wantSuccessRate: 1, wantAccountingKnown: true,
		},
		{
			name: "unknown success and known failure",
			runs: 2, success: 1, failure: 1,
			unknownCostRuns: 1, unknownSuccessRuns: 1,
			wantSuccessRate: 0, wantAccountingKnown: true,
		},
		{
			name: "known success and in-flight siblings",
			runs: 4, success: 1, running: 3,
			wantSuccessRate: 1, wantAccountingKnown: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			successRate, _, _, accountingKnown := admin.DelegationRankRatesWithAccountingOutcomesForTest(
				tc.runs, tc.success, tc.failure, tc.running,
				tc.unknownCostRuns, tc.unknownSuccessRuns, 0,
			)
			if successRate != tc.wantSuccessRate || accountingKnown != tc.wantAccountingKnown {
				t.Fatalf("rate/accounting = %v/%v, want %v/%v", successRate, accountingKnown, tc.wantSuccessRate, tc.wantAccountingKnown)
			}
		})
	}
}

func TestModelStatsExcludeInFlightRowsFromAccountingAndSpeed(t *testing.T) {
	stats := admin.ModelStatsForDelegationForTest(admin.DelegationContext{
		Workers: []admin.DelegationWorkerContext{
			{
				Worker: &store.Worker{ModelProvider: "anthropic", ModelID: "claude-sonnet-4-5"},
				LatestRun: &store.WorkerRun{ModelProvider: "anthropic", ModelID: "claude-sonnet-4-5",
					Status: "success", InputTokens: 100, OutputTokens: 20, CostUSD: 0.1, DurationMS: 100},
			},
			{
				Worker: &store.Worker{ModelProvider: "anthropic", ModelID: "claude-sonnet-4-5"},
				LatestRun: &store.WorkerRun{ModelProvider: "anthropic", ModelID: "claude-sonnet-4-5",
					Status: "running", InputTokens: 900, OutputTokens: 90, CostUSD: 9, DurationMS: 900},
			},
		},
	})
	if len(stats) != 1 {
		t.Fatalf("stats len = %d, want 1", len(stats))
	}
	s := stats[0]
	if s.Runs != 2 || s.Running != 1 || s.AvgDurationMS != 100 {
		t.Fatalf("run/speed stats = %+v, want runs=2 running=1 avg=100", s)
	}
	if s.InputTokens != 100 || s.OutputTokens != 20 || s.CostUSD != 0.1 {
		t.Fatalf("accounting = in:%d out:%d cost:%v, want terminal row only", s.InputTokens, s.OutputTokens, s.CostUSD)
	}
}

func TestOperationalDurationDoesNotInflateModelSpeed(t *testing.T) {
	stats := admin.ModelStatsForDelegationForTest(admin.DelegationContext{
		Workers: []admin.DelegationWorkerContext{
			{
				Worker: &store.Worker{ModelProvider: "mimo_cli", ModelID: "xiaomi/mimo-v2.5-pro"},
				LatestRun: &store.WorkerRun{ModelProvider: "mimo_cli", ModelID: "xiaomi/mimo-v2.5-pro",
					Status: "success", InputTokens: 100, OutputTokens: 20, DurationMS: 100},
			},
			{
				Worker: &store.Worker{ModelProvider: "mimo_cli", ModelID: "xiaomi/mimo-v2.5-pro"},
				LatestRun: &store.WorkerRun{ModelProvider: "mimo_cli", ModelID: "xiaomi/mimo-v2.5-pro",
					Status: "failure", Error: "adapter send: process died", DurationMS: 900},
			},
		},
	})
	if len(stats) != 1 || stats[0].OperationalDurationMS != 900 || stats[0].AvgDurationMS != 100 {
		t.Fatalf("duration stats = %+v, want operational=900 and model avg=100", stats)
	}
}

func TestZeroQualityAndReliabilityRatesRemainVisibleInJSON(t *testing.T) {
	b, err := json.Marshal(admin.DelegationModelStat{})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, field := range []string{`"quality_rate":0`, `"reliability_rate":0`} {
		if !strings.Contains(string(b), field) {
			t.Fatalf("JSON %s is missing zero-valued field %s", b, field)
		}
	}
}
