package admin_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestCapacityListMarksOperationallyQuarantinedModelUnavailable(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	for _, p := range []*store.ModelProfile{
		{Name: "unstable", Provider: "anthropic", SecretScopeID: scopeID, KnownModels: []string{"claude-unstable-runtime"}},
		{Name: "proven", Provider: "anthropic", SecretScopeID: scopeID, KnownModels: []string{"claude-proven-runtime"}},
	} {
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create profile %s: %v", p.Name, err)
		}
	}

	seedDelegationModelReview(t, svc, db, wsID, scopeID, "anthropic", "claude-proven-runtime", 82)
	seedDelegationModelReview(t, svc, db, wsID, scopeID, "anthropic", "claude-unstable-runtime", 88)
	for i := 0; i < 6; i++ {
		seedDelegationOperationalFailure(t, svc, db, wsID, scopeID, "anthropic", "claude-unstable-runtime")
	}

	rows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "coding",
		Limit:       20,
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}
	var unstable *admin.DelegationModelCapacity
	for i := range rows {
		if rows[i].ModelKey == "anthropic/claude-unstable-runtime" {
			unstable = &rows[i]
			break
		}
	}
	if unstable == nil {
		t.Fatalf("missing unstable row in %+v", rows)
	}
	if unstable.Available {
		t.Fatalf("unstable row should be unavailable after quarantine: %+v", unstable)
	}
	if !unstable.Quarantined {
		t.Fatalf("unstable row should be marked quarantined: %+v", unstable)
	}
	if !strings.Contains(unstable.UnavailableReason, "quarantined") {
		t.Fatalf("unavailable reason = %q, want quarantine explanation", unstable.UnavailableReason)
	}

	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Pick a healthy model.",
		TaskKind:            "coding",
		ModelSelectionMode:  "capacity",
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("Delegate capacity: %v", err)
	}
	got, err := svc.Get(ctx, admin.GetInput{ID: out.Dispatches[0].WorkerID})
	if err != nil {
		t.Fatalf("Get selected worker: %v", err)
	}
	if got.Worker.ModelID == "claude-unstable-runtime" {
		t.Fatalf("capacity selected quarantined model: %+v", got.Worker)
	}
}

func TestDelegationRejectsRawOpenCodeFanout(t *testing.T) {
	t.Setenv("MCPLEXER_ALLOW_OPENCODE_CLI", "1")
	svc, _, wsID, _ := newTestService(t)

	_, err := svc.Delegate(context.Background(), admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Fan out on OpenCode.",
		ModelProvider:       "opencode_cli",
		ModelID:             "minimax/MiniMax-M3",
		ModelEndpointURL:    "/usr/local/bin/opencode",
		Parallelism:         2,
		MaxWallClockSeconds: 30,
	})
	if err == nil {
		t.Fatal("Delegate raw opencode fan-out succeeded, want error")
	}
	if !strings.Contains(err.Error(), "fan-out requires an HTTP attach endpoint") {
		t.Fatalf("error = %q, want raw CLI fan-out guidance", err.Error())
	}
}

func seedDelegationOperationalFailure(
	t *testing.T,
	svc *admin.Service,
	db interface {
		UpdateWorkerRunStatus(context.Context, string, store.WorkerRunFinalize) error
		GetWorkerRun(context.Context, string) (*store.WorkerRun, error)
		ListWorkerRuns(context.Context, string, int) ([]*store.WorkerRun, error)
	},
	wsID string,
	scopeID string,
	provider string,
	modelID string,
) {
	t.Helper()
	ctx := context.Background()
	out, err := svc.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:         wsID,
		Objective:           "Seed operational failure.",
		TaskKind:            "coding",
		ModelProvider:       provider,
		ModelID:             modelID,
		SecretScopeID:       scopeID,
		MaxWallClockSeconds: 30,
	})
	if err != nil {
		t.Fatalf("seed operational Delegate %s/%s: %v", provider, modelID, err)
	}
	run := waitForDelegationRun(t, db, out.Dispatches[0].WorkerID)
	if err := db.UpdateWorkerRunStatus(ctx, run.ID, store.WorkerRunFinalize{
		Status:             "failure",
		FinishedAt:         time.Now().UTC(),
		InputTokens:        0,
		OutputTokens:       0,
		CostUSD:            0,
		Error:              "adapter send: test_cli: run: signal: killed (stderr: )",
		MeshMessageIDsJSON: "[]",
		AuditRecordIDsJSON: "[]",
	}); err != nil {
		t.Fatalf("finish operational seed run: %v", err)
	}
	if _, err := svc.ReviewDelegation(ctx, admin.DelegationReviewInput{
		WorkspaceID:  wsID,
		DelegationID: out.DelegationID,
		TaskKind:     "coding",
		Score:        15,
		Notes:        "Adapter died before producing output.",
	}); err != nil {
		t.Fatalf("review operational seed delegation: %v", err)
	}
}
