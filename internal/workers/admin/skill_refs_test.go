// skill_refs_test.go (M0.7) — multi-skill round-trip + validation
// coverage for the admin Service.
package admin_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

func TestServiceCreate_SkillRefsRoundTrip(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	in := baseCreate(wsID, scopeID)
	in.SkillRefs = []store.SkillRef{
		{Name: "lead-responder", Version: "1"},
		{Name: "ops-guard", Version: ""},
	}
	w, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(w.SkillRefs) != 2 {
		t.Fatalf("skill_refs len = %d, want 2 (%+v)", len(w.SkillRefs), w.SkillRefs)
	}
	if w.SkillRefs[0].Name != "lead-responder" || w.SkillRefs[1].Name != "ops-guard" {
		t.Fatalf("unexpected order: %+v", w.SkillRefs)
	}
	// Legacy mirror — first entry leaks into SkillName/SkillVersion so
	// pre-multi-skill consumers see something sensible.
	if w.SkillName != "lead-responder" || w.SkillVersion != "1" {
		t.Fatalf("legacy fields not mirrored: name=%q ver=%q", w.SkillName, w.SkillVersion)
	}
	// EffectiveSkillRefs prefers the canonical list.
	got := w.EffectiveSkillRefs()
	if len(got) != 2 || got[0].Name != "lead-responder" {
		t.Fatalf("EffectiveSkillRefs = %+v", got)
	}
}

func TestServiceCreate_LegacySkillNameOnly(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	in := baseCreate(wsID, scopeID)
	in.SkillName = "lead-responder"
	in.SkillVersion = "2"
	// SkillRefs intentionally left nil — exercises legacy fallback.
	w, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(w.SkillRefs) != 1 {
		t.Fatalf("expected single ref synthesised, got %+v", w.SkillRefs)
	}
	if w.SkillRefs[0].Name != "lead-responder" || w.SkillRefs[0].Version != "2" {
		t.Fatalf("ref mismatch: %+v", w.SkillRefs[0])
	}
}

func TestServiceCreate_SkillRefsRejectDuplicates(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	in := baseCreate(wsID, scopeID)
	in.SkillRefs = []store.SkillRef{
		{Name: "a", Version: ""},
		{Name: "a", Version: ""},
	}
	_, err := svc.Create(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate error, got %v", err)
	}
}

func TestServiceCreate_SkillRefsRejectEmptyName(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	in := baseCreate(wsID, scopeID)
	in.SkillRefs = []store.SkillRef{{Name: " ", Version: "1"}}
	_, err := svc.Create(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "name required") {
		t.Fatalf("expected name-required error, got %v", err)
	}
}

func TestServiceCreate_SkillRefsRejectTooMany(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	in := baseCreate(wsID, scopeID)
	for i := 0; i < 9; i++ {
		in.SkillRefs = append(in.SkillRefs, store.SkillRef{Name: fmt.Sprintf("skill-%d", i)})
	}
	_, err := svc.Create(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "skill_refs max 8") {
		t.Fatalf("expected skill-ref cap error, got %v", err)
	}
}

func TestServiceUpdate_ReplaceSkillRefs(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	in := baseCreate(wsID, scopeID)
	in.SkillName = "first"
	w, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	newRefs := []store.SkillRef{{Name: "second", Version: "1"}, {Name: "third", Version: ""}}
	updated, err := svc.Update(ctx, admin.UpdateInput{
		ID:        w.ID,
		SkillRefs: &newRefs,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(updated.SkillRefs) != 2 {
		t.Fatalf("skill_refs len = %d, want 2", len(updated.SkillRefs))
	}
	if updated.SkillName != "second" {
		t.Fatalf("legacy mirror = %q, want second", updated.SkillName)
	}
}

func TestServiceUpdate_ClearSkillRefs(t *testing.T) {
	svc, _, wsID, scopeID := newTestService(t)
	ctx := context.Background()

	in := baseCreate(wsID, scopeID)
	in.SkillRefs = []store.SkillRef{{Name: "x"}}
	w, err := svc.Create(ctx, in)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	empty := []store.SkillRef{}
	updated, err := svc.Update(ctx, admin.UpdateInput{ID: w.ID, SkillRefs: &empty})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(updated.SkillRefs) != 0 {
		t.Fatalf("expected empty SkillRefs, got %+v", updated.SkillRefs)
	}
	if updated.SkillName != "" || updated.SkillVersion != "" {
		t.Fatalf("legacy fields not cleared: %q/%q", updated.SkillName, updated.SkillVersion)
	}
}
