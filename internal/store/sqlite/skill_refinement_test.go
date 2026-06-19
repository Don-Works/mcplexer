package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestRefinementProposalRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	p := &store.SkillRefinementProposal{
		SkillName:           "pdf-extract",
		SkillVersion:        2,
		Friction:            "fails on multi-column layouts",
		SuggestedChange:     "switch to pdfplumber's --layout flag",
		Rationale:           "preserves column order; current pdftotext output is interleaved garbage",
		ProposedBySessionID: "sess-abc",
		WorkspaceID:         "ws-1",
		MetadataJSON:        json.RawMessage(`{"hint":"hand-test"}`),
	}
	if err := db.RecordRefinementProposal(ctx, p); err != nil {
		t.Fatalf("record: %v", err)
	}
	if p.ID == "" {
		t.Fatal("expected ID to be assigned")
	}
	if p.CreatedAt.IsZero() {
		t.Fatal("expected CreatedAt to be assigned")
	}
	if p.Status != store.RefinementStatusPending {
		t.Fatalf("status = %q, want %q", p.Status, store.RefinementStatusPending)
	}

	got, err := db.GetRefinementProposal(ctx, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SkillName != p.SkillName {
		t.Fatalf("skill_name = %q, want %q", got.SkillName, p.SkillName)
	}
	if got.SkillVersion != 2 {
		t.Fatalf("skill_version = %d, want 2", got.SkillVersion)
	}
	if got.Friction != p.Friction {
		t.Fatalf("friction = %q, want %q", got.Friction, p.Friction)
	}
	if string(got.MetadataJSON) != `{"hint":"hand-test"}` {
		t.Fatalf("metadata = %s", got.MetadataJSON)
	}
}

func TestRefinementProposalUpdateAutoStampsResolvedAt(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	p := &store.SkillRefinementProposal{
		SkillName:           "pdf-extract",
		SkillVersion:        2,
		Friction:            "needs --layout flag",
		SuggestedChange:     "add it",
		Rationale:           "preserves order",
		ProposedBySessionID: "sess-abc",
		WorkspaceID:         "ws-1",
	}
	if err := db.RecordRefinementProposal(ctx, p); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Patch to promoted without a ResolvedAt — store should stamp one.
	promoted := store.RefinementStatusPromoted
	note := "approved by reviewer A"
	sess := "rev-1"
	if err := db.UpdateRefinementProposal(ctx, p.ID, store.RefinementProposalPatch{
		Status:              &promoted,
		ResolvedBySessionID: &sess,
		ResolutionNote:      &note,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	got, err := db.GetRefinementProposal(ctx, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != store.RefinementStatusPromoted {
		t.Fatalf("status = %q, want promoted", got.Status)
	}
	if got.ResolvedAt == nil {
		t.Fatal("expected ResolvedAt auto-stamp on terminal status")
	}
	if got.ResolutionNote != note {
		t.Fatalf("note = %q", got.ResolutionNote)
	}
	if got.ResolvedBySessionID != sess {
		t.Fatalf("resolver = %q", got.ResolvedBySessionID)
	}
}

func TestRefinementProposalFilterAndSort(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// Two skills, mixed statuses. Created at increasing offsets so
	// sort-by-DESC is deterministic.
	now := time.Now().UTC()
	rows := []*store.SkillRefinementProposal{
		{
			SkillName: "pdf-extract", SkillVersion: 1,
			Friction: "a", SuggestedChange: "b", Rationale: "c",
			ProposedBySessionID: "s1", WorkspaceID: "ws-1",
			CreatedAt: now.Add(-3 * time.Minute),
		},
		{
			SkillName: "pdf-extract", SkillVersion: 1,
			Friction: "a", SuggestedChange: "b", Rationale: "c",
			ProposedBySessionID: "s2", WorkspaceID: "ws-1",
			CreatedAt: now.Add(-2 * time.Minute),
			Status:    store.RefinementStatusCandidate,
		},
		{
			SkillName: "ocr-cleanup", SkillVersion: 4,
			Friction: "x", SuggestedChange: "y", Rationale: "z",
			ProposedBySessionID: "s3", WorkspaceID: "ws-2",
			CreatedAt: now.Add(-1 * time.Minute),
		},
	}
	for i, r := range rows {
		if err := db.RecordRefinementProposal(ctx, r); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}

	// Filter by skill name returns only the matching rows, newest first.
	got, err := db.ListRefinementProposals(ctx, store.RefinementFilter{
		SkillName: "pdf-extract",
	})
	if err != nil {
		t.Fatalf("list by skill: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("by skill len = %d, want 2", len(got))
	}
	if got[0].ProposedBySessionID != "s2" {
		t.Fatalf("expected s2 first (newest), got %q", got[0].ProposedBySessionID)
	}

	// Filter by status.
	got, err = db.ListRefinementProposals(ctx, store.RefinementFilter{
		Status: store.RefinementStatusCandidate,
	})
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if len(got) != 1 || got[0].Status != store.RefinementStatusCandidate {
		t.Fatalf("by status returned %+v", got)
	}

	// Filter by workspace.
	got, err = db.ListRefinementProposals(ctx, store.RefinementFilter{
		WorkspaceID: "ws-2",
	})
	if err != nil {
		t.Fatalf("list by ws: %v", err)
	}
	if len(got) != 1 || got[0].WorkspaceID != "ws-2" {
		t.Fatalf("by ws returned %+v", got)
	}
}

func TestCountSimilarProposals(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	mkProposal := func(skill, friction, sess string) {
		t.Helper()
		p := &store.SkillRefinementProposal{
			SkillName: skill, SkillVersion: 1,
			Friction:            friction,
			SuggestedChange:     "do the thing",
			Rationale:           "because",
			ProposedBySessionID: sess,
			WorkspaceID:         "ws-1",
		}
		if err := db.RecordRefinementProposal(ctx, p); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}

	mkProposal("pdf-extract", "ffmpeg fails on h.265 input", "s1")
	mkProposal("pdf-extract", "FFMPEG fails on H.265 with -c:v copy", "s2")
	mkProposal("pdf-extract", "ffmpeg fails on h.265 + audio dropped", "s3")
	mkProposal("pdf-extract", "totally unrelated layout issue", "s4")
	mkProposal("ocr-cleanup", "ffmpeg fails on h.265", "s5") // different skill — must not be counted

	tests := []struct {
		name, skill, substring string
		want                   int
	}{
		{"exact substring matches all three", "pdf-extract", "ffmpeg fails on h.265", 3},
		{"case-insensitive matches all three", "pdf-extract", "FFmpeg fails on H.265", 3},
		{"unique phrase matches one", "pdf-extract", "totally unrelated", 1},
		{"skill scoping isolates ocr-cleanup", "pdf-extract", "ffmpeg fails", 3},
		{"different skill scoped count", "ocr-cleanup", "ffmpeg fails", 1},
		{"no match returns zero", "pdf-extract", "this does not appear anywhere", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, err := db.CountSimilarProposals(ctx, tt.skill, tt.substring)
			if err != nil {
				t.Fatalf("count: %v", err)
			}
			if n != tt.want {
				t.Fatalf("count = %d, want %d", n, tt.want)
			}
		})
	}
}

func TestCountSimilarProposalsInputValidation(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if _, err := db.CountSimilarProposals(ctx, "", "x"); err == nil {
		t.Fatal("expected error for empty skill_name")
	}
	if _, err := db.CountSimilarProposals(ctx, "skill", ""); err == nil {
		t.Fatal("expected error for empty friction_substring")
	}
}

func TestUpdateRefinementProposalNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	status := store.RefinementStatusRejected
	err := db.UpdateRefinementProposal(ctx, "no-such-id", store.RefinementProposalPatch{
		Status: &status,
	})
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestGetRefinementProposalNotFound(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	_, err := db.GetRefinementProposal(ctx, "no-such-id")
	if err != store.ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestRefinementProposalAppliedIsTerminal(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	p := &store.SkillRefinementProposal{
		SkillName:           "test-skill",
		SkillVersion:        1,
		Friction:            "test friction",
		SuggestedChange:     "new body here",
		Rationale:           "rationale",
		ProposedBySessionID: "s1",
		WorkspaceID:         "ws-t",
	}
	if err := db.RecordRefinementProposal(ctx, p); err != nil {
		t.Fatalf("record: %v", err)
	}

	applied := store.RefinementStatusApplied
	note := "adopted via test"
	if err := db.UpdateRefinementProposal(ctx, p.ID, store.RefinementProposalPatch{
		Status:         &applied,
		ResolutionNote: &note,
	}); err != nil {
		t.Fatalf("update to applied: %v", err)
	}

	got, err := db.GetRefinementProposal(ctx, p.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != store.RefinementStatusApplied {
		t.Fatalf("status=%q want applied", got.Status)
	}
	if got.ResolvedAt == nil {
		t.Fatal("expected ResolvedAt set for applied (terminal)")
	}
	if got.ResolutionNote != note {
		t.Fatalf("note=%q", got.ResolutionNote)
	}
}
