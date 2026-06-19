// convergence_test.go — receive-side linked-workspace convergence:
// re-pushing a task (one offer per sender mutation) must UPDATE the
// existing local row, not pile up duplicates. Internal-package test so
// it can drive convergeOrMaterialize directly without standing up a
// libp2p TaskShareService (the auto-accept wire path is covered in
// internal/p2p).
package tasks

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newConvergeSvc(t *testing.T) (*Service, *sqlite.DB, string) {
	t.Helper()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	w := &store.Workspace{Name: "gateway", RootPath: "/tmp/gw", Tags: json.RawMessage("[]")}
	if err := d.CreateWorkspace(context.Background(), w); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return New(d), d, w.ID
}

// seedAcceptedOffer records the (peer, remote_task_id) -> localTaskID
// mapping a prior accepted offer would have left, so the next push can
// converge onto localTaskID.
func seedAcceptedOffer(t *testing.T, d *sqlite.DB, peer, remoteTaskID, localTaskID, wsID string) {
	t.Helper()
	o := &store.TaskOffer{
		RemoteTaskID:  remoteTaskID,
		FromPeerID:    peer,
		ToPeerID:      "self",
		TaskID:        localTaskID,
		WorkspaceID:   wsID,
		Title:         "seed",
		EnvelopeNonce: "nonce-seed",
		Direction:     "incoming",
		State:         store.TaskOfferAccepted,
	}
	if err := d.CreateTaskOffer(context.Background(), o); err != nil {
		t.Fatalf("seed offer: %v", err)
	}
}

func incomingOffer(peer, remoteTaskID string) *store.TaskOffer {
	return &store.TaskOffer{
		RemoteTaskID:      remoteTaskID,
		FromPeerID:        peer,
		ToPeerID:          "self",
		RemoteWorkspaceID: "ws-A",
		EnvelopeNonce:     "nonce-push",
		Direction:         "incoming",
		State:             store.TaskOfferAutoAccepted,
	}
}

func TestConvergeUpdatesExistingTaskNoDuplicate(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newConvergeSvc(t)

	// A task that arrived earlier from peer-A's remote task r1.
	orig, err := svc.Create(ctx, CreateOptions{
		WorkspaceID: wsID, Title: "Old title", Status: "open",
		SourceKind: store.TaskSourcePeerImport, ActorKind: store.TaskSourcePeerImport,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	seedAcceptedOffer(t, db, "peer-A", "r1", orig.ID, wsID)

	// A re-push carrying updated status + title for the same remote task.
	payload := &p2p.TaskPayloadEnvelope{
		RemoteTaskID: "r1", Title: "New title", Status: "doing", Priority: "high",
	}
	got, err := svc.convergeOrMaterialize(ctx, incomingOffer("peer-A", "r1"), payload, wsID)
	if err != nil {
		t.Fatalf("convergeOrMaterialize: %v", err)
	}
	if got.ID != orig.ID {
		t.Fatalf("converge created a NEW task %s (want update of %s)", got.ID, orig.ID)
	}
	if got.Status != "doing" || got.Title != "New title" {
		t.Fatalf("convergence did not apply payload: status=%q title=%q", got.Status, got.Title)
	}
	// Exactly one task in the workspace — no duplicate.
	all, err := svc.List(ctx, store.TaskFilter{WorkspaceID: wsID})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("got %d tasks, want 1 (no duplicate)", len(all))
	}
}

func TestConvergeMaterializesWhenNoMapping(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newConvergeSvc(t)

	payload := &p2p.TaskPayloadEnvelope{RemoteTaskID: "r2", Title: "Fresh", Status: "open"}
	got, err := svc.convergeOrMaterialize(ctx, incomingOffer("peer-A", "r2"), payload, wsID)
	if err != nil {
		t.Fatalf("convergeOrMaterialize: %v", err)
	}
	if got.Title != "Fresh" {
		t.Fatalf("materialize title=%q", got.Title)
	}
	all, _ := svc.List(ctx, store.TaskFilter{WorkspaceID: wsID})
	if len(all) != 1 {
		t.Fatalf("got %d tasks, want 1", len(all))
	}
}

func TestConvergeLeaseGuardProtectsLocalWork(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newConvergeSvc(t)

	// Local row actively claimed by a LOCAL session with a live lease.
	orig, err := svc.Create(ctx, CreateOptions{
		WorkspaceID: wsID, Title: "Local work", Status: "doing",
		Assignee: &Assignee{SessionID: "local-sess"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	future := time.Now().UTC().Add(3 * time.Minute)
	orig.LeaseExpiresAt = &future
	if err := db.UpdateTask(ctx, orig); err != nil {
		t.Fatalf("stamp lease: %v", err)
	}
	seedAcceptedOffer(t, db, "peer-A", "r3", orig.ID, wsID)

	// Peer pushes a status change — must NOT stomp the active local lease.
	payload := &p2p.TaskPayloadEnvelope{RemoteTaskID: "r3", Title: "Peer retitle", Status: "done"}
	got, err := svc.convergeOrMaterialize(ctx, incomingOffer("peer-A", "r3"), payload, wsID)
	if err != nil {
		t.Fatalf("convergeOrMaterialize: %v", err)
	}
	if got.Status == "done" {
		t.Fatalf("lease guard failed: peer status 'done' stomped active local 'doing'")
	}
	if got.Title != "Peer retitle" {
		t.Fatalf("descriptive field should still converge under lease guard, title=%q", got.Title)
	}
}
