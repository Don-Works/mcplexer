package gateway

import (
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

func TestTaskActivityClustersGroupsRepeatedStatusNoise(t *testing.T) {
	got := taskActivityClusters([]tasks.ActivityEntry{
		{TaskID: "task-a", TaskTitle: "A", Evt: "status_changed", To: "doing", Status: "doing"},
		{TaskID: "task-b", TaskTitle: "B", Evt: "status_changed", To: "doing", Status: "doing"},
		{TaskID: "task-c", TaskTitle: "C", Evt: "status_changed", To: "blocked", Status: "blocked"},
	})

	if got.Total != 3 {
		t.Fatalf("Total = %d, want 3", got.Total)
	}
	if got.Clustered != 2 {
		t.Fatalf("Clustered = %d, want 2: %+v", got.Clustered, got.Groups)
	}
	if len(got.Groups) != 2 {
		t.Fatalf("len(Groups) = %d, want 2: %+v", len(got.Groups), got.Groups)
	}
	if got.Groups[0].Count != 2 {
		t.Fatalf("first group count = %d, want 2: %+v", got.Groups[0].Count, got.Groups[0])
	}
}

func TestSemanticRankTasksRanksAuthorizedCandidates(t *testing.T) {
	billingTags, _ := json.Marshal([]string{"billing", "refund"})
	infraTags, _ := json.Marshal([]string{"infra"})
	rows := []store.Task{
		{
			ID:          "task-infra",
			Title:       "Rotate build runner",
			Description: "Update CI credentials",
			Status:      "open",
			TagsJSON:    infraTags,
		},
		{
			ID:          "task-billing",
			Title:       "Refund invoice overcharge",
			Description: "Customer needs billing adjustment",
			Status:      "open",
			TagsJSON:    billingTags,
		},
	}

	got := semanticRankTasks(rows, "billing refund customer", 1, 0)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1: %+v", len(got), got)
	}
	if got[0].ID != "task-billing" {
		t.Fatalf("top hit = %q, want task-billing", got[0].ID)
	}
}
