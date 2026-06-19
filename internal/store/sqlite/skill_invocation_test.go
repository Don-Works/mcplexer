package sqlite_test

import (
	"context"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestSkillInvocations_RoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	rows := []store.SkillInvocation{
		{SkillName: "browser-only", ToolName: "browser__open",
			Namespace: "browser", Allowed: true},
		{SkillName: "browser-only", ToolName: "postgres__query",
			Namespace: "postgres", Allowed: false},
		{SkillName: "issue-bot", ToolName: "linear__create",
			Namespace: "linear", Allowed: true},
	}
	for i := range rows {
		if err := db.InsertSkillInvocation(ctx, &rows[i]); err != nil {
			t.Fatalf("insert[%d]: %v", i, err)
		}
		if rows[i].ID == 0 {
			t.Fatalf("insert[%d] did not assign ID", i)
		}
	}

	all, err := db.ListSkillInvocations(ctx, store.SkillInvocationFilter{})
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("len(all) = %d, want 3", len(all))
	}

	skill := "browser-only"
	bo, err := db.ListSkillInvocations(ctx, store.SkillInvocationFilter{
		SkillName: &skill,
	})
	if err != nil {
		t.Fatalf("list by skill: %v", err)
	}
	if len(bo) != 2 {
		t.Errorf("len(browser-only) = %d, want 2", len(bo))
	}

	denied := false
	rejected, err := db.ListSkillInvocations(ctx, store.SkillInvocationFilter{
		Allowed: &denied,
	})
	if err != nil {
		t.Fatalf("list denied: %v", err)
	}
	if len(rejected) != 1 {
		t.Fatalf("len(denied) = %d, want 1", len(rejected))
	}
	if rejected[0].ToolName != "postgres__query" {
		t.Errorf("denied[0].ToolName = %q, want postgres__query", rejected[0].ToolName)
	}
}

func TestSkillInvocations_NilInsert(t *testing.T) {
	db := newTestDB(t)
	if err := db.InsertSkillInvocation(context.Background(), nil); err == nil {
		t.Fatal("expected error for nil invocation, got nil")
	}
}

// TestAuditRecord_SkillIDRoundTrip ensures the new nullable skill_id column
// round-trips cleanly: nil for legacy rows, a *string for skill-tagged rows.
func TestAuditRecord_SkillIDRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	skillName := "browser-bot"
	rows := []store.AuditRecord{
		{ToolName: "legacy_call", Status: "success"},
		{ToolName: "skill_call", Status: "success", SkillID: &skillName},
	}
	for i := range rows {
		if err := db.InsertAuditRecord(ctx, &rows[i]); err != nil {
			t.Fatalf("insert[%d]: %v", i, err)
		}
	}

	got, _, err := db.QueryAuditRecords(ctx, store.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}

	var legacy, skilled *store.AuditRecord
	for i := range got {
		switch got[i].ToolName {
		case "legacy_call":
			legacy = &got[i]
		case "skill_call":
			skilled = &got[i]
		}
	}
	if legacy == nil || skilled == nil {
		t.Fatalf("missing rows: legacy=%v skilled=%v", legacy, skilled)
	}
	if legacy.SkillID != nil {
		t.Errorf("legacy.SkillID = %v, want nil", *legacy.SkillID)
	}
	if skilled.SkillID == nil || *skilled.SkillID != "browser-bot" {
		got := "<nil>"
		if skilled.SkillID != nil {
			got = *skilled.SkillID
		}
		t.Errorf("skilled.SkillID = %s, want browser-bot", got)
	}
}
