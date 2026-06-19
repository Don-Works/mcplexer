package sqlite_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSkillRuns_RoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	r := &store.SkillRun{
		SkillName:      "mcplexer-features",
		SkillVersion:   3,
		WorkspaceID:    "ws-1",
		AgentSessionID: "sess-abc",
		MetadataJSON:   json.RawMessage(`{"agent_name":"w2-skill-telemetry"}`),
	}
	if err := db.RecordSkillRun(ctx, r); err != nil {
		t.Fatalf("RecordSkillRun: %v", err)
	}
	if r.ID == "" {
		t.Fatalf("expected ID to be assigned, got empty string")
	}
	if r.Outcome != store.SkillRunOutcomeRunning {
		t.Fatalf("expected default outcome=running, got %q", r.Outcome)
	}
	if string(r.PhasesJSON) != "[]" {
		t.Fatalf("expected default phases_json=[], got %q", r.PhasesJSON)
	}

	// Round-trip via Get.
	got, err := db.GetSkillRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetSkillRun: %v", err)
	}
	if got.SkillName != "mcplexer-features" || got.SkillVersion != 3 || got.WorkspaceID != "ws-1" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
	if got.AgentSessionID != "sess-abc" {
		t.Fatalf("agent_session_id not preserved: %q", got.AgentSessionID)
	}

	// Patch: append a phase event.
	events := []store.SkillRunPhaseEvent{
		{Phase: "discover", Event: "started", At: time.Now().UTC()},
	}
	phases, _ := json.Marshal(events)
	if err := db.UpdateSkillRun(ctx, r.ID, store.SkillRunPatch{
		PhasesJSON: phases,
	}); err != nil {
		t.Fatalf("UpdateSkillRun phases: %v", err)
	}

	got2, err := db.GetSkillRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetSkillRun after phase patch: %v", err)
	}
	if string(got2.PhasesJSON) == "[]" {
		t.Fatalf("phases_json not updated: %q", got2.PhasesJSON)
	}
	if got2.Outcome != store.SkillRunOutcomeRunning {
		t.Fatalf("outcome should still be running, got %q", got2.Outcome)
	}
	if got2.CompletedAt != nil {
		t.Fatalf("completed_at should be nil mid-run, got %v", got2.CompletedAt)
	}

	// Complete: terminal outcome auto-stamps CompletedAt.
	success := store.SkillRunOutcomeSuccess
	tools, _ := json.Marshal([]store.SkillRunToolUse{
		{Name: "mcpx__skill_get", Count: 2},
		{Name: "task__create", Count: 5},
	})
	if err := db.UpdateSkillRun(ctx, r.ID, store.SkillRunPatch{
		Outcome:       &success,
		ToolsUsedJSON: tools,
	}); err != nil {
		t.Fatalf("UpdateSkillRun complete: %v", err)
	}

	got3, err := db.GetSkillRun(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetSkillRun after complete: %v", err)
	}
	if got3.Outcome != store.SkillRunOutcomeSuccess {
		t.Fatalf("outcome not updated: %q", got3.Outcome)
	}
	if got3.CompletedAt == nil {
		t.Fatalf("completed_at should be auto-stamped on terminal outcome")
	}
	if string(got3.ToolsUsedJSON) == "[]" {
		t.Fatalf("tools_used_json not updated")
	}
}

func TestSkillRuns_Filter(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	base := time.Now().UTC().Add(-1 * time.Hour)
	rows := []store.SkillRun{
		{SkillName: "skill-a", SkillVersion: 1, WorkspaceID: "ws-1", StartedAt: base.Add(0)},
		{SkillName: "skill-a", SkillVersion: 1, WorkspaceID: "ws-1", StartedAt: base.Add(1 * time.Minute)},
		{SkillName: "skill-b", SkillVersion: 1, WorkspaceID: "ws-1", StartedAt: base.Add(2 * time.Minute)},
		{SkillName: "skill-a", SkillVersion: 2, WorkspaceID: "ws-2", StartedAt: base.Add(3 * time.Minute)},
	}
	for i := range rows {
		if err := db.RecordSkillRun(ctx, &rows[i]); err != nil {
			t.Fatalf("insert[%d]: %v", i, err)
		}
	}
	// Mark one as failed.
	failed := store.SkillRunOutcomeFailure
	if err := db.UpdateSkillRun(ctx, rows[2].ID, store.SkillRunPatch{Outcome: &failed}); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	// All four.
	all, err := db.ListSkillRuns(ctx, store.SkillRunFilter{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("len(all) = %d, want 4", len(all))
	}
	// Ordered started_at DESC — last inserted should be first.
	if all[0].SkillName != "skill-a" || all[0].SkillVersion != 2 {
		t.Fatalf("ordering wrong: first = %+v", all[0])
	}

	// Filter by skill name.
	skillA, err := db.ListSkillRuns(ctx, store.SkillRunFilter{SkillName: "skill-a"})
	if err != nil {
		t.Fatalf("list skill-a: %v", err)
	}
	if len(skillA) != 3 {
		t.Fatalf("len(skill-a) = %d, want 3", len(skillA))
	}

	// Filter by workspace.
	ws2, err := db.ListSkillRuns(ctx, store.SkillRunFilter{WorkspaceID: "ws-2"})
	if err != nil {
		t.Fatalf("list ws-2: %v", err)
	}
	if len(ws2) != 1 {
		t.Fatalf("len(ws-2) = %d, want 1", len(ws2))
	}

	// Filter by outcome.
	failures, err := db.ListSkillRuns(ctx, store.SkillRunFilter{Outcome: store.SkillRunOutcomeFailure})
	if err != nil {
		t.Fatalf("list failures: %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("len(failures) = %d, want 1", len(failures))
	}
	if failures[0].SkillName != "skill-b" {
		t.Fatalf("failure should be skill-b, got %s", failures[0].SkillName)
	}

	// Filter by Since (everything strictly after the 2nd row).
	cutoff := base.Add(90 * time.Second)
	recent, err := db.ListSkillRuns(ctx, store.SkillRunFilter{Since: &cutoff})
	if err != nil {
		t.Fatalf("list since: %v", err)
	}
	if len(recent) != 2 {
		t.Fatalf("len(recent) = %d, want 2", len(recent))
	}

	// Limit clamp.
	limited, err := db.ListSkillRuns(ctx, store.SkillRunFilter{Limit: 2})
	if err != nil {
		t.Fatalf("list limit: %v", err)
	}
	if len(limited) != 2 {
		t.Fatalf("len(limited) = %d, want 2", len(limited))
	}
}

func TestSkillRuns_GetNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	_, err := db.GetSkillRun(ctx, "01KAAAAAAAAAAAAAAAAAAAAAAA")
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestSkillRuns_UpdateNoop(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	r := &store.SkillRun{SkillName: "x", SkillVersion: 1, WorkspaceID: "ws-1"}
	if err := db.RecordSkillRun(ctx, r); err != nil {
		t.Fatalf("RecordSkillRun: %v", err)
	}
	// Empty patch — should not error, should not change row.
	if err := db.UpdateSkillRun(ctx, r.ID, store.SkillRunPatch{}); err != nil {
		t.Fatalf("UpdateSkillRun empty: %v", err)
	}
	// Empty patch on missing id should yield ErrNotFound.
	err := db.UpdateSkillRun(ctx, "01KNOEXISTNOEXISTNOEXISTNZ", store.SkillRunPatch{})
	if !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for missing id, got %v", err)
	}
}
