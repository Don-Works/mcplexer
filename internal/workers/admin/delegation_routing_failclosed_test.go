package admin_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

type failDelegationHistoryStore struct {
	store.WorkerStore
}

func (s *failDelegationHistoryStore) ListWorkers(
	ctx context.Context, workspaceID string, enabledOnly bool,
) ([]*store.Worker, error) {
	if !enabledOnly {
		return nil, errors.New("injected delegation history read failure")
	}
	return s.WorkerStore.ListWorkers(ctx, workspaceID, enabledOnly)
}

func TestDelegationCapacityFailsClosedWhenHistoryCannotBeRead(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()
	durable := baseCreate(wsID, scopeID)
	durable.Name = "capacity-profile"
	if _, err := svc.Create(ctx, durable); err != nil {
		t.Fatalf("create candidate worker: %v", err)
	}

	broken := admin.New(&failDelegationHistoryStore{WorkerStore: db}, admin.Options{Workspaces: db})
	if _, err := broken.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
	}); err == nil || !strings.Contains(err.Error(), "ranking history") {
		t.Fatalf("capacity list error = %v, want fail-closed ranking-history error", err)
	}
	if _, err := broken.Delegate(ctx, admin.DelegationInput{
		WorkspaceID:        wsID,
		Objective:          "Use trusted production capacity.",
		ModelSelectionMode: "capacity",
	}); err == nil || !strings.Contains(err.Error(), "ranking history") {
		t.Fatalf("capacity dispatch error = %v, want fail-closed ranking-history error", err)
	}
}

func TestRankedSelectionRequiresReviewedCandidate(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	_, err := svc.Delegate(context.Background(), admin.DelegationInput{
		WorkspaceID:        wsID,
		Objective:          "Use the best reviewed candidate.",
		ModelSelectionMode: "ranked",
		ModelCandidates: []admin.DelegationModelCandidate{
			{ModelProvider: "anthropic", ModelID: "candidate-a", SecretScopeID: scopeID},
			{ModelProvider: "anthropic", ModelID: "candidate-b", SecretScopeID: scopeID},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "reviewed, non-quarantined") {
		t.Fatalf("ranked dispatch error = %v, want no-reviewed-candidate error", err)
	}
}
