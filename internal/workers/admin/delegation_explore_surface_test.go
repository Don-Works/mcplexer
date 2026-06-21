package admin_test

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// TestCapacityListSurfacesExplorationForNewModel proves the explore/exploit
// signal reaches ListDelegationModelCapacity: a freshly-registered model the
// ledger has never run is marked Exploring with a positive ExplorationBonus
// and floats to the top of the ranking (so it gets scheduled), while a
// proven, well-run reviewed model is no longer marked exploring.
func TestCapacityListSurfacesExplorationForNewModel(t *testing.T) {
	svc, db, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	for _, p := range []*store.ModelProfile{
		{Name: "proven", Provider: "openai", SecretScopeID: scopeID, KnownModels: []string{"gpt-proven"}},
		{Name: "fresh-frontier", Provider: "anthropic", SecretScopeID: scopeID, KnownModels: []string{"claude-fresh-frontier"}},
	} {
		if err := db.CreateModelProfile(ctx, p); err != nil {
			t.Fatalf("create profile %s: %v", p.Name, err)
		}
	}

	// Run the "proven" model enough times that it settles out of the explore
	// phase (clear of the boundary), leaving "fresh-frontier" never touched.
	for i := 0; i < admin.ExplorationSettledRunsForTest()+2; i++ {
		seedDelegationModelReview(t, svc, db, wsID, scopeID, "openai", "gpt-proven", 80)
	}

	rows, err := svc.ListDelegationModelCapacity(ctx, admin.DelegationCapacityListInput{
		WorkspaceID: wsID,
		TaskKind:    "coding",
	})
	if err != nil {
		t.Fatalf("ListDelegationModelCapacity: %v", err)
	}

	var fresh, proven *admin.DelegationModelCapacity
	for i := range rows {
		switch rows[i].ModelKey {
		case "anthropic/claude-fresh-frontier":
			fresh = &rows[i]
		case "openai/gpt-proven":
			proven = &rows[i]
		}
	}
	if fresh == nil || proven == nil {
		t.Fatalf("missing rows: fresh=%v proven=%v", fresh, proven)
	}

	if !fresh.Exploring {
		t.Fatalf("fresh frontier model must be marked Exploring, got %+v", fresh)
	}
	if fresh.ExplorationBonus <= 0 {
		t.Fatalf("fresh frontier model must carry a positive exploration bonus, got %.2f", fresh.ExplorationBonus)
	}
	if proven.Exploring {
		t.Fatalf("proven settled model must NOT be marked Exploring, got %+v", proven)
	}
	// The proven model's exploration bonus must have decayed below the fresh
	// model's full optimism — exploration is winding down for it.
	if proven.ExplorationBonus >= fresh.ExplorationBonus {
		t.Fatalf("proven bonus %.2f must be below fresh bonus %.2f (decayed by its own runs)",
			proven.ExplorationBonus, fresh.ExplorationBonus)
	}
	// The fresh, never-tried model must be lifted into contention — its
	// capacity score lands at the top of the board (rank 1 or 2) so it
	// actually gets scheduled, the whole point of the cold-start fix.
	if rows[0].ModelKey != "anthropic/claude-fresh-frontier" &&
		rows[1].ModelKey != "anthropic/claude-fresh-frontier" {
		t.Fatalf("fresh frontier model must rank in the top 2 so it gets scheduled; got order %s, %s",
			rows[0].ModelKey, rows[1].ModelKey)
	}
	if fresh.CapacityScore < proven.CapacityScore-1 {
		t.Fatalf("fresh score %.2f must be at least on par with proven %.2f for the cold-start lift",
			fresh.CapacityScore, proven.CapacityScore)
	}
}
