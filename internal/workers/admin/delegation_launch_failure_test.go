package admin_test

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// Launch-failure ranking tests: a worker run that died at the
// adapter/launch stage (subprocess crashed before the model produced
// any output) must NOT poison per-model quality ranking. The model
// never ran, so the parent's review score is a judgement about the
// adapter, not about model quality — folding it into the per-model
// avg would corrupt capacity ranking for every model that ever had a
// launch crash. The operational counter preserves the data for the
// operator; the review simply doesn't move the quality number.

// TestIsOperationalFailureSignature is the table-driven unit test for
// the predicate that decides whether a run qualifies as an
// operational failure (adapter/launch died before the model produced
// any output). The signature is intentionally narrow so a real
// quality failure mid-loop is never misclassified.
func TestIsOperationalFailureSignature(t *testing.T) {
	cases := []struct {
		name string
		run  *store.WorkerRun
		want bool
	}{
		{
			name: "nil run is not operational (dispatch-failed is a separate case)",
			run:  nil,
			want: false,
		},
		{
			name: "status=success with zero tokens is accounting-missing, NOT operational",
			run:  &store.WorkerRun{Status: "success", InputTokens: 0, OutputTokens: 0, Error: ""},
			want: false,
		},
		{
			name: "status=failure with zero tokens and adapter-send prefix IS operational",
			run: &store.WorkerRun{
				Status:       "failure",
				InputTokens:  0,
				OutputTokens: 0,
				Error:        "adapter send: opencode_cli: run: signal: killed (stderr: )",
			},
			want: true,
		},
		{
			name: "status=failure with tokens burned is a real quality failure, NOT operational",
			run: &store.WorkerRun{
				Status:       "failure",
				InputTokens:  1200,
				OutputTokens: 80,
				Error:        "adapter send: opencode_cli: run: exit status 1",
			},
			want: false,
		},
		{
			name: "status=failure with zero tokens but wrong error prefix is NOT operational",
			run: &store.WorkerRun{
				Status:       "failure",
				InputTokens:  0,
				OutputTokens: 0,
				Error:        "wall-clock (1m0s) exceeded",
			},
			want: false,
		},
		{
			name: "status=cap_exceeded is treated as a real run, NOT operational",
			run: &store.WorkerRun{
				Status:       "cap_exceeded",
				InputTokens:  0,
				OutputTokens: 0,
				Error:        "wall-clock (1m0s) exceeded",
			},
			want: false,
		},
		{
			name: "status=cancelled is operator hard-stop, NOT operational",
			run: &store.WorkerRun{
				Status:       "cancelled",
				InputTokens:  0,
				OutputTokens: 0,
				Error:        "cancelled by operator",
			},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := admin.IsOperationalFailureForTest(c.run)
			if got != c.want {
				t.Fatalf("isOperationalFailure(%+v) = %v, want %v", c.run, got, c.want)
			}
		})
	}
}

// TestDelegationIsOperationalOnlyForModel covers the suppression
// predicate. Every worker in the set must be operational (launch
// failure or dispatch-failed) for the parent review to be
// suppressed; any worker with a real run makes the model attributable.
func TestDelegationIsOperationalOnlyForModel(t *testing.T) {
	opRun := &store.WorkerRun{
		Status:       "failure",
		InputTokens:  0,
		OutputTokens: 0,
		Error:        "adapter send: opencode_cli: run: signal: killed",
	}
	successRun := &store.WorkerRun{
		Status:       "success",
		InputTokens:  100,
		OutputTokens: 50,
	}
	tokensBurnedFailure := &store.WorkerRun{
		Status:       "failure",
		InputTokens:  200,
		OutputTokens: 10,
		Error:        "adapter send: opencode_cli: parse output: bad JSON",
	}
	cases := []struct {
		name    string
		workers []admin.DelegationWorkerContext
		want    bool
	}{
		{
			name:    "empty worker set is not operational-only (no signal either way)",
			workers: nil,
			want:    false,
		},
		{
			name: "single operational run",
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: opRun},
			},
			want: true,
		},
		{
			name: "single dispatch-failed worker (no run row)",
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: nil, DispatchFailed: true},
			},
			want: true,
		},
		{
			name: "mixed: one op + one success → attributable (model DID run)",
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: opRun},
				{Worker: &store.Worker{ID: "w2"}, LatestRun: successRun},
			},
			want: false,
		},
		{
			name: "mixed: one op + one token-burned failure → attributable (model ran tokens)",
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: opRun},
				{Worker: &store.Worker{ID: "w2"}, LatestRun: tokensBurnedFailure},
			},
			want: false,
		},
		{
			name: "no-run worker that is NOT dispatch-failed is unattributable in this direction",
			// A worker with no run row and no dispatch-failed flag
			// (e.g. dispatch never completed and wasn't recorded as
			// failed) — the run is unattributed. We choose NOT to
			// treat it as operational-only so the review still
			// applies; the review-record path is more honest about
			// "we don't know what happened" than silently dropping
			// the parent score.
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: nil, DispatchFailed: false},
			},
			want: false,
		},
		{
			name: "operational + dispatch-failed → operational-only",
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: opRun},
				{Worker: &store.Worker{ID: "w2"}, LatestRun: nil, DispatchFailed: true},
			},
			want: true,
		},
		{
			name: "pre-execute block means model never ran",
			workers: []admin.DelegationWorkerContext{{
				Worker: &store.Worker{ID: "w1"},
				LatestRun: &store.WorkerRun{
					Status: "blocked",
					Error:  "pre-execute hook blocked the run: policy gate",
				},
			}},
			want: true,
		},
		{
			name: "post-execute block remains attributable",
			workers: []admin.DelegationWorkerContext{{
				Worker: &store.Worker{ID: "w1"},
				LatestRun: &store.WorkerRun{
					Status: "blocked",
					Error:  "post-execute deliverability gate blocked the run: empty report",
				},
			}},
			want: false,
		},
		{
			name: "dispatch-failed overrides a non-operational run row (operator-stamped flag wins)",
			// Production sets DispatchFailed + no run row; a test that
			// synthesises the flag on a worker that already has a run
			// row must still see suppression kick in. The flag is the
			// operator's authoritative "this worker never ran" signal.
			workers: []admin.DelegationWorkerContext{
				{Worker: &store.Worker{ID: "w1"}, LatestRun: successRun, DispatchFailed: true},
			},
			want: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := admin.DelegationIsOperationalOnlyForModelForTest(c.workers)
			if got != c.want {
				t.Fatalf("delegationIsOperationalOnly = %v, want %v", got, c.want)
			}
		})
	}
}

// TestServiceDelegationLaunchFailureExcludedFromModelRank is the end-
// to-end assertion: a worker that died at launch + a parent review
// score of 20 must NOT move the model's avg review score in the
// capacity ranker. The operational counter increments so the operator
// can see the launch crash.
func TestServiceDelegationLaunchFailureExcludedFromModelRank(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_OPENCODE_CLI", "1")
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	// Seed a healthy review for the model so we have a baseline. This
	// delegation SUCCEEDS with real tokens — the model ran.
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "opencode_cli", "minimax/MiniMax-M3", 85)

	// Now create a second delegation for the same model whose run dies
	// at launch. The parent scores it 20 (a real reaction, but a
	// reaction to a subprocess crash, not to model quality). The
	// expected behaviour: the model's avg review score stays at 85;
	// the operational counter goes up.
	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Bounded worker, will die at launch.",
		TaskKind:            "coding",
		ModelProvider:       "opencode_cli",
		ModelID:             "minimax/MiniMax-M3",
		WorkerIsolation:     "none",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate launch-failure seed: %v", err)
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "failure",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        0,
		OutputTokens:       0,
		CostUSD:            0,
		Error:              "adapter send: opencode_cli: run: signal: killed (stderr: <truncated>)",
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize launch-failure run: %v", err)
	}
	if _, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		TaskKind:     "coding",
		Score:        20,
		Notes:        "Worker never produced output — adapter crashed at launch.",
	}); err != nil {
		t.Fatalf("review launch-failure delegation: %v", err)
	}

	// Capacity rank must show the model's avg review score UNCHANGED
	// (still 85 from the one healthy review) AND the operational
	// counter at 1.
	capRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "coding",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	var row *admin.DelegationModelCapacity
	for i := range capRows {
		if capRows[i].ModelKey == "opencode_cli/minimax/MiniMax-M3" {
			row = &capRows[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("model not in capacity rows: %+v", capRows)
	}
	if row.ReviewScore != 85 {
		t.Fatalf("avg review score = %.1f, want 85 (launch-failure review must be excluded)", row.ReviewScore)
	}
	if row.ReviewCount != 1 {
		t.Fatalf("review count = %d, want 1 (only the healthy review counts)", row.ReviewCount)
	}
	if row.OperationalFailures != 1 {
		t.Fatalf("operational failures = %d, want 1", row.OperationalFailures)
	}
	if row.Runs < 2 {
		t.Fatalf("runs = %d, want >= 2 (the launch-failure still counts as a run)", row.Runs)
	}
}

// TestServiceDelegationGenuineRejectionMovesModelRank is the
// counter-case: a quality rejection (status=failure but with
// non-zero tokens — the model did run before failing) MUST still
// move the model's avg review score. This is the regression guard
// that proves the operational-failure exclusion doesn't accidentally
// swallow real quality data.
func TestServiceDelegationGenuineRejectionMovesModelRank(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	seedDelegationModelReview(t, svc, db, wsID, scopeID, "anthropic", "claude-genuine", 90)

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Genuine quality rejection.",
		TaskKind:            "review",
		ModelProvider:       "anthropic",
		ModelID:             "claude-genuine",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate genuine seed: %v", err)
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	// Quality failure WITH tokens burned: the model produced at least
	// one turn (so the run wasn't an operational launch crash), then
	// failed on a later turn. This is real quality data.
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "failure",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        1500,
		OutputTokens:       200,
		CostUSD:            0.03,
		Error:              "adapter send: opencode_cli: parse output: truncated",
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize genuine-failure run: %v", err)
	}
	if _, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		TaskKind:     "review",
		Score:        30,
		Notes:        "Model ran but produced nonsense.",
	}); err != nil {
		t.Fatalf("review genuine delegation: %v", err)
	}

	capRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "review",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	var row *admin.DelegationModelCapacity
	for i := range capRows {
		if capRows[i].ModelKey == "anthropic/claude-genuine" {
			row = &capRows[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("model not in capacity rows: %+v", capRows)
	}
	// The capacity ranker reports the recency-weighted EWMA (alpha=0.70,
	// task-kind boost → effective weight 2), not the simple average. With
	// the genuine rejection reviewed MOST RECENTLY, the EWMA must pull
	// away from 90 toward 30 — i.e. the new review is included, the
	// operational-exclusion didn't swallow it. Expected:
	//   ewma_0 = 90
	//   effAlpha = 1 - (1-0.70)^2 = 0.91
	//   ewma_1 = 0.91*30 + 0.09*90 = 35.4
	if row.ReviewCount != 2 {
		t.Fatalf("review count = %d, want 2 (both reviews count)", row.ReviewCount)
	}
	wantEWMA := 0.91*30 + 0.09*90
	if math.Abs(row.ReviewScore-wantEWMA) > 0.5 {
		t.Fatalf("recency-weighted review score = %.2f, want ~%.2f (genuine rejection MUST move the avg)", row.ReviewScore, wantEWMA)
	}
	// Sanity: the EWMA must sit strictly between 30 and 60 — closer to
	// 30 than 60 because the genuine rejection is the most-recent
	// review. This proves the operational-failure exclusion didn't
	// accidentally include the launch-failure case AND didn't
	// accidentally exclude the genuine case.
	if row.ReviewScore >= 60 || row.ReviewScore <= 30 {
		t.Fatalf("recency score = %.2f, want strictly between 30 (recent) and 60 (simple avg)", row.ReviewScore)
	}
	if row.OperationalFailures != 0 {
		t.Fatalf("operational failures = %d, want 0 (genuine failure is not operational)", row.OperationalFailures)
	}
}

// TestServiceDelegationDispatchFailedExcludedFromModelRank covers the
// other operational-failure case: a worker whose detached RunNowWithOpts
// errored before any run row existed (no LatestRun, DispatchFailed=true).
// The parent's review score is preserved on the delegation record (the
// operator can still see it) but must not be attributed to the model
// because the model never ran.
func TestServiceDelegationDispatchFailedExcludedFromModelRank(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_GROK_CLI", "1")
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	seedDelegationModelReview(t, svc, db, wsID, scopeID, "grok_cli", "grok-dispatch-test", 80)

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Bounded worker, will fail at dispatch.",
		TaskKind:            "coding",
		ModelProvider:       "grok_cli",
		ModelID:             "grok-dispatch-test",
		WorkerIsolation:     "none",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate dispatch-failed seed: %v", err)
	}

	// Force a dispatch-failed state by stamping the worker metadata
	// directly. In production, dispatchDelegationRun stamps this when
	// RunNowWithOpts errored before a run row existed. Tests can't
	// trigger that code path deterministically (it requires a failing
	// run-bus), so we synthesize the metadata flag here.
	stampDispatchFailedForTest(t, ctx, svc, out.Dispatches[0].WorkerID, "synthetic dispatch failure for test")

	if _, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		TaskKind:     "coding",
		Score:        15,
		Notes:        "Worker never dispatched.",
	}); err != nil {
		t.Fatalf("review dispatch-failed delegation: %v", err)
	}

	capRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "coding",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	var row *admin.DelegationModelCapacity
	for i := range capRows {
		if capRows[i].ModelKey == "grok_cli/grok-dispatch-test" {
			row = &capRows[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("model not in capacity rows: %+v", capRows)
	}
	if row.ReviewCount != 1 {
		t.Fatalf("review count = %d, want 1 (only the healthy seed counts)", row.ReviewCount)
	}
	if row.ReviewScore != 80 {
		t.Fatalf("avg review score = %.1f, want 80 (dispatch-failed review must be excluded)", row.ReviewScore)
	}
	if row.OperationalFailures != 1 {
		t.Fatalf("operational failures = %d, want 1", row.OperationalFailures)
	}
	if row.DispatchFailures != 1 {
		t.Fatalf("dispatch failures = %d, want 1", row.DispatchFailures)
	}
	if row.ReliabilityRate != 0.5 {
		t.Fatalf("reliability rate = %v, want 0.5 (one healthy run, one failed dispatch)", row.ReliabilityRate)
	}
	if row.SuccessRate != 1 || !row.AccountingKnown {
		t.Fatalf("accounting = rate:%v known:%v, want healthy run preserved despite dispatch-only attempt", row.SuccessRate, row.AccountingKnown)
	}
}

// TestServiceDelegationLaunchFailureCounterIsModelScoped confirms the
// operational-failure counter is per-model, not per-delegation: a
// launch failure on opencode_cli does not bump the counter for
// anthropic or grok_cli.
func TestServiceDelegationLaunchFailureCounterIsModelScoped(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_OPENCODE_CLI", "1")
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	for _, p := range []admin.CreateInput{
		{
			Name: "registry-launch-scope-a", ModelProvider: "opencode_cli", ModelID: "minimax/M3",
			SecretScopeID: scopeID, PromptTemplate: "x", ScheduleSpec: "manual", WorkspaceID: wsID,
		},
		{
			Name: "registry-launch-scope-b", ModelProvider: "anthropic", ModelID: "claude-scope-b",
			SecretScopeID: scopeID, PromptTemplate: "x", ScheduleSpec: "manual", WorkspaceID: wsID,
		},
	} {
		if _, err := svc.Create(ctx, p); err != nil {
			t.Fatalf("seed create %s: %v", p.Name, err)
		}
	}

	// OpenCode model dies at launch.
	launchOut, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "opencode_cli launch-failure seed.",
		TaskKind:            "coding",
		ModelProvider:       "opencode_cli",
		ModelID:             "minimax/M3",
		WorkerIsolation:     "none",
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate opencode: %v", err)
	}
	run := waitForDelegationRun(t, db, launchOut.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "failure",
		FinishedAt:         time.Now().UTC(),
		Error:              "adapter send: opencode_cli: run: signal: killed",
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finalize opencode: %v", err)
	}
	if _, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: launchOut.DelegationID,
		TaskKind:     "coding",
		Score:        20,
	}); err != nil {
		t.Fatalf("review opencode: %v", err)
	}

	// Sanity-check: the sibling anthropic model has no operational failures.
	capRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "coding",
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	byKey := map[string]admin.DelegationModelCapacity{}
	for _, r := range capRows {
		byKey[r.ModelKey] = r
	}
	oc, ok := byKey["opencode_cli/minimax/M3"]
	if !ok {
		t.Fatalf("opencode row missing: %+v", capRows)
	}
	if oc.OperationalFailures != 1 {
		t.Fatalf("opencode operational_failures = %d, want 1", oc.OperationalFailures)
	}
	if an, ok := byKey["anthropic/claude-scope-b"]; ok && an.OperationalFailures != 0 {
		t.Fatalf("anthropic operational_failures = %d, want 0 (must not leak from opencode)", an.OperationalFailures)
	}
}

// stampDispatchFailedForTest mutates a delegation worker's metadata
// to set DispatchFailed + DispatchError. Production code reaches this
// state via dispatchDelegationRun (when detached RunNowWithOpts errors
// before a run row exists); tests can't deterministically reproduce
// that race, so we synthesize the flag here.
func stampDispatchFailedForTest(t *testing.T, ctx context.Context, svc *admin.Service, workerID, dispatchError string) {
	t.Helper()
	got, err := svc.Get(ctx, admin.GetInput{ID: workerID})
	if err != nil {
		t.Fatalf("get worker for dispatch-failed stamp: %v", err)
	}
	var env map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got.Worker.ParametersJSON), &env); err != nil {
		t.Fatalf("parse parameters: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(env["_mcplexer_delegation"], &meta); err != nil {
		t.Fatalf("parse meta: %v", err)
	}
	meta["dispatch_failed"] = true
	meta["dispatch_error"] = dispatchError
	updated, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal updated meta: %v", err)
	}
	env["_mcplexer_delegation"] = updated
	out, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal env: %v", err)
	}
	params := string(out)
	if _, err := svc.Update(ctx, admin.UpdateInput{ID: workerID, ParametersJSON: &params}); err != nil {
		t.Fatalf("update parameters: %v", err)
	}
}

// TestServiceDelegationMixedRunOpsInSameDelegationAttributable: when a
// single delegation has BOTH an operational-failure worker AND a
// success worker (both for the same model key), the parent review
// remains attributable to the model. The model DID run on the
// success worker, so the parent's review is about model quality, not
// the adapter. This is the asymmetric "ops-only when ALL ops" rule
// documented on the suppression predicate.
func TestServiceDelegationMixedRunOpsInSameDelegationAttributable(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	ensureDelegationModelProfileForTest(t, db, scopeID, "anthropic", "claude-mixed-test")

	// Two-parallel delegation: one side dies at launch, one succeeds.
	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Mixed: one op, one success.",
		TaskKind:            "review",
		ModelProvider:       "anthropic",
		ModelID:             "claude-mixed-test",
		SecretScopeID:       scopeID,
		Parallelism:         2,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate mixed: %v", err)
	}
	if len(out.Dispatches) != 2 {
		t.Fatalf("dispatches = %d, want 2", len(out.Dispatches))
	}

	// Stamp one side as a launch failure, one as a healthy success.
	for i, dispatch := range out.Dispatches {
		run := waitForDelegationRun(t, db, dispatch.WorkerID)
		if i == 0 {
			if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
				Status: "failure", FinishedAt: time.Now().UTC(),
				Error:              "adapter send: opencode_cli: run: signal: killed",
				MeshMessageIDsJSON: "[]", AuditRecordIDsJSON: "[]",
			}); err != nil {
				t.Fatalf("finalize op side: %v", err)
			}
		} else {
			if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
				Status: "success", FinishedAt: time.Now().UTC(),
				InputTokens: 800, OutputTokens: 200, CostUSD: 0.01,
				MeshMessageIDsJSON: "[]", AuditRecordIDsJSON: "[]",
			}); err != nil {
				t.Fatalf("finalize success side: %v", err)
			}
		}
	}

	if _, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		TaskKind:     "review",
		Score:        70,
		Notes:        "One good worker, one died at launch.",
	}); err != nil {
		t.Fatalf("review mixed: %v", err)
	}

	capRows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "review",
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	var row *admin.DelegationModelCapacity
	for i := range capRows {
		if capRows[i].ModelKey == "anthropic/claude-mixed-test" {
			row = &capRows[i]
			break
		}
	}
	if row == nil {
		t.Fatalf("model not in capacity rows: %+v", capRows)
	}
	if row.ReviewCount != 1 {
		t.Fatalf("review count = %d, want 1 (mixed delegation still attributable)", row.ReviewCount)
	}
	if row.ReviewScore != 70 {
		t.Fatalf("avg review score = %.1f, want 70 (mixed: parent review applies because the model did run on the success worker)", row.ReviewScore)
	}
	if row.OperationalFailures != 1 {
		t.Fatalf("operational_failures = %d, want 1 (one side died at launch)", row.OperationalFailures)
	}
}

// _ ensures the strings import is used (kept for future string-key
// assertions in this file). The package is otherwise used by
// other tests in delegation_test.go.
var _ = strings.TrimSpace
