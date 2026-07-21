package admin_test

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestDelegationCandidateRegistryExcludesEphemeralWorkers(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	durable := baseCreate(wsID, scopeID)
	durable.Name = "delegate-static-reviewer"
	durable.ModelID = "durable-model"
	if _, err := svc.Create(ctx, durable); err != nil {
		t.Fatalf("create durable worker: %v", err)
	}

	metadataDelegate := baseCreate(wsID, scopeID)
	metadataDelegate.Name = "historical-one-shot"
	metadataDelegate.ModelID = "ephemeral-from-metadata"
	metadataDelegate.ParametersJSON = `{"_mcplexer_delegation":{"id":"del-old","parallel_index":1,"parallel_total":1}}`
	if _, err := svc.Create(ctx, metadataDelegate); err != nil {
		t.Fatalf("create metadata delegation worker: %v", err)
	}

	prefixOnlyDurable := baseCreate(wsID, scopeID)
	prefixOnlyDurable.Name = "delegate-static-0123456789ab"
	prefixOnlyDurable.ModelID = "prefix-only-durable"
	if _, err := svc.Create(ctx, prefixOnlyDurable); err != nil {
		t.Fatalf("create prefix-only durable worker: %v", err)
	}

	rows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		Limit:       50,
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}

	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.ModelID] = true
	}
	if !seen["durable-model"] {
		t.Fatal("durable configured worker was removed from the capacity registry")
	}
	if !seen["prefix-only-durable"] {
		t.Fatal("durable delegate-* worker was removed by a name heuristic")
	}
	if seen["ephemeral-from-metadata"] {
		t.Fatal("metadata-marked ephemeral delegation model leaked into capacity candidates")
	}
}
