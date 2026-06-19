// cross_peer_meta_test.go — verifies that an inbound task envelope
// carrying legacy frontmatter-shaped meta (from a pre-072 peer) is
// normalised to JSON on receipt. Service.Create always funnels meta
// through MetaToJSON, so materializeOfferedTask + the offer-accept
// path both inherit the rewrite-on-receive behaviour.
package tasks_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// TestServiceCreateNormalisesLegacyMeta — direct Create with legacy
// frontmatter meta lands as canonical JSON in the store. This is the
// same path materializeOfferedTask exercises on the peer-import side.
func TestServiceCreateNormalisesLegacyMeta(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: wsID,
		Title:       "Imported from older peer",
		Meta:        "composed_by: 01EPIC\ncomposes: 01A, 01B",
		SourceKind:  store.TaskSourcePeerImport,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if t1.Meta == "" {
		t.Fatal("meta should not be empty after legacy import")
	}
	if !json.Valid([]byte(t1.Meta)) {
		t.Fatalf("meta should be JSON after legacy import, got %q", t1.Meta)
	}
	if got := tasks.MetaListGet(t1.Meta, "composed_by"); len(got) != 1 || got[0] != "01EPIC" {
		t.Errorf("composed_by lost: %v\n meta=%q", got, t1.Meta)
	}
	if got := tasks.MetaListGet(t1.Meta, "composes"); len(got) != 2 || got[0] != "01A" || got[1] != "01B" {
		t.Errorf("composes lost: %v\n meta=%q", got, t1.Meta)
	}
	// Source kind is recorded as peer-import so the dashboard can
	// distinguish.
	if t1.SourceKind != store.TaskSourcePeerImport {
		t.Errorf("source_kind = %q, want peer-import", t1.SourceKind)
	}
}

// TestServiceUpdateNormalisesLegacyMeta — patching a row with legacy
// meta in the patch body also normalises to JSON on write.
func TestServiceUpdateNormalisesLegacyMeta(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	t1, err := svc.Create(ctx, tasks.CreateOptions{WorkspaceID: wsID, Title: "seed"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	legacy := "worktree: /tmp/x\nbranch: feat/y"
	patch := tasks.UpdatePatch{Meta: &legacy}
	updated, err := svc.Update(ctx, wsID, t1.ID, patch)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if !json.Valid([]byte(updated.Meta)) {
		t.Fatalf("post-update meta should be JSON, got %q", updated.Meta)
	}
	wc, err := tasks.ParseWorkContext(updated.Meta)
	if err != nil {
		t.Fatalf("ParseWorkContext: %v", err)
	}
	if wc.Worktree != "/tmp/x" || wc.Branch != "feat/y" {
		t.Errorf("legacy patch lost during normalisation: %+v\n meta=%q", wc, updated.Meta)
	}
}
