package sqlite_test

import (
	"context"
	"errors"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestWorkerMeshTriggerCRUD walks one trigger row through every operation
// the dispatcher + admin surfaces depend on. Covers defaults, FromFilters
// round-trip, enable toggle, and the cascade-delete tied to worker deletion.
func TestWorkerMeshTriggerCRUD(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	w := newWorker(wsID, scopeID, "trigger-host")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}

	// Create with minimal fields — defaults must populate throttle +
	// chain-depth + from_filters.
	trig := &store.WorkerMeshTrigger{
		WorkerID:  w.ID,
		KindMatch: "alert",
		Enabled:   true,
	}
	if err := db.CreateWorkerMeshTrigger(ctx, trig); err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if trig.ID == "" {
		t.Fatal("expected generated ID")
	}

	got, err := db.GetWorkerMeshTrigger(ctx, trig.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ThrottleSeconds != 60 || got.MaxChainDepth != 3 {
		t.Fatalf("defaults missing: throttle=%d depth=%d",
			got.ThrottleSeconds, got.MaxChainDepth)
	}
	if got.FromFilters == nil {
		t.Fatal("FromFilters must serialise as [], not nil")
	}
	if !got.Enabled {
		t.Fatal("enabled flag lost")
	}

	// Update — set FromFilters + content regex + tags + flip enabled off.
	got.FromFilters = []store.TriggerFromFilter{
		{PeerID: "12D3RemotePeer"},
		{AgentName: "audit-watcher"},
		{Role: "security"},
	}
	got.TagMatch = "p2p,critical"
	got.ContentRegex = "(?i)breach"
	got.Enabled = false
	got.ThrottleSeconds = 30
	got.MaxChainDepth = 5
	if err := db.UpdateWorkerMeshTrigger(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}

	updated, _ := db.GetWorkerMeshTrigger(ctx, trig.ID)
	if updated.Enabled {
		t.Fatal("enabled should be false")
	}
	if updated.ThrottleSeconds != 30 || updated.MaxChainDepth != 5 {
		t.Fatalf("throttle/depth not persisted: %+v", updated)
	}
	if len(updated.FromFilters) != 3 {
		t.Fatalf("FromFilters round-trip: %+v", updated.FromFilters)
	}
	if updated.FromFilters[0].PeerID != "12D3RemotePeer" {
		t.Fatalf("FromFilters[0] = %+v", updated.FromFilters[0])
	}

	// Enabled-only query excludes the now-disabled trigger.
	enabled, err := db.ListAllEnabledMeshTriggers(ctx)
	if err != nil {
		t.Fatalf("list enabled: %v", err)
	}
	if len(enabled) != 0 {
		t.Fatalf("disabled trigger leaked into enabled list: %+v", enabled)
	}

	// Re-enable and confirm it appears.
	updated.Enabled = true
	if err := db.UpdateWorkerMeshTrigger(ctx, updated); err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	enabled, _ = db.ListAllEnabledMeshTriggers(ctx)
	if len(enabled) != 1 || enabled[0].ID != trig.ID {
		t.Fatalf("expected re-enabled row: %+v", enabled)
	}

	// Per-worker list mirrors enabled state.
	perWorker, err := db.ListWorkerMeshTriggers(ctx, w.ID)
	if err != nil {
		t.Fatalf("list per worker: %v", err)
	}
	if len(perWorker) != 1 {
		t.Fatalf("per-worker list: %d", len(perWorker))
	}

	// Delete by id, then confirm gone.
	if err := db.DeleteWorkerMeshTrigger(ctx, trig.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := db.GetWorkerMeshTrigger(ctx, trig.ID); !errors.Is(err, store.ErrWorkerMeshTriggerNotFound) {
		t.Fatalf("expected not-found after delete, got %v", err)
	}
}

// TestWorkerMeshTriggerStatusTransitionRoundtrip confirms the
// status_from_match / status_to_match columns persist through create,
// update, and the enabled-list scan path the dispatcher hydrates from.
func TestWorkerMeshTriggerStatusTransitionRoundtrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	w := newWorker(wsID, scopeID, "transition-host")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}

	trig := &store.WorkerMeshTrigger{
		WorkerID:        w.ID,
		KindMatch:       "task_event",
		StatusToMatch:   "review",
		StatusFromMatch: "doing",
		Enabled:         true,
	}
	if err := db.CreateWorkerMeshTrigger(ctx, trig); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	got, err := db.GetWorkerMeshTrigger(ctx, trig.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.StatusToMatch != "review" || got.StatusFromMatch != "doing" {
		t.Fatalf("transition fields not persisted: from=%q to=%q",
			got.StatusFromMatch, got.StatusToMatch)
	}

	// Update clears from, keeps to.
	got.StatusFromMatch = ""
	got.StatusToMatch = "ready"
	if err := db.UpdateWorkerMeshTrigger(ctx, got); err != nil {
		t.Fatalf("update: %v", err)
	}

	// The dispatcher hydrates via ListAllEnabledMeshTriggers — verify the
	// scan path carries the columns too.
	enabled, err := db.ListAllEnabledMeshTriggers(ctx)
	if err != nil {
		t.Fatalf("list enabled: %v", err)
	}
	if len(enabled) != 1 {
		t.Fatalf("expected 1 enabled trigger, got %d", len(enabled))
	}
	if enabled[0].StatusFromMatch != "" || enabled[0].StatusToMatch != "ready" {
		t.Fatalf("updated transition fields wrong on scan: from=%q to=%q",
			enabled[0].StatusFromMatch, enabled[0].StatusToMatch)
	}
}

// TestWorkerMeshTriggerCascadeOnWorkerDelete verifies the foreign-key
// ON DELETE CASCADE wipes a worker's triggers when the worker is
// removed. This is the contract the admin surface relies on so deleting
// a worker can't leak orphaned trigger rows.
func TestWorkerMeshTriggerCascadeOnWorkerDelete(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)
	w := newWorker(wsID, scopeID, "doomed-worker")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		trig := &store.WorkerMeshTrigger{WorkerID: w.ID, Enabled: true}
		if err := db.CreateWorkerMeshTrigger(ctx, trig); err != nil {
			t.Fatalf("create trigger %d: %v", i, err)
		}
	}
	if err := db.DeleteWorker(ctx, w.ID); err != nil {
		t.Fatalf("delete worker: %v", err)
	}
	remaining, err := db.ListWorkerMeshTriggers(ctx, w.ID)
	if err != nil {
		t.Fatalf("list after cascade: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("cascade did not run; remaining=%d", len(remaining))
	}
}

// TestHasPeerScope exercises the dispatcher's permission gate. Verifies
// granted scopes match, missing scopes return false, and unknown /
// revoked peers return (false, nil) without leaking peer existence.
func TestHasPeerScope(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	peer := &store.P2PPeer{
		PeerID:      "12D3KooWPeerA",
		DisplayName: "lab-laptop",
	}
	if err := db.AddPeer(ctx, peer); err != nil {
		t.Fatalf("add peer: %v", err)
	}

	// Empty inputs return (false, nil).
	if ok, err := db.HasPeerScope(ctx, "", "trigger_worker:*"); err != nil || ok {
		t.Fatalf("empty peer_id: ok=%v err=%v", ok, err)
	}
	if ok, err := db.HasPeerScope(ctx, peer.PeerID, ""); err != nil || ok {
		t.Fatalf("empty scope: ok=%v err=%v", ok, err)
	}

	// No scope yet.
	ok, err := db.HasPeerScope(ctx, peer.PeerID, "trigger_worker:audit-watcher")
	if err != nil || ok {
		t.Fatalf("pre-grant: ok=%v err=%v", ok, err)
	}

	// Grant + verify.
	if err := db.GrantPeerScope(ctx, peer.PeerID, "trigger_worker:audit-watcher"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	ok, err = db.HasPeerScope(ctx, peer.PeerID, "trigger_worker:audit-watcher")
	if err != nil || !ok {
		t.Fatalf("post-grant: ok=%v err=%v", ok, err)
	}

	// Wildcard grant.
	if err := db.GrantPeerScope(ctx, peer.PeerID, "trigger_worker:*"); err != nil {
		t.Fatalf("grant wildcard: %v", err)
	}
	ok, _ = db.HasPeerScope(ctx, peer.PeerID, "trigger_worker:*")
	if !ok {
		t.Fatal("wildcard scope missing after grant")
	}

	// Revoke removes the specific scope.
	if err := db.RevokePeerScope(ctx, peer.PeerID, "trigger_worker:audit-watcher"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	ok, _ = db.HasPeerScope(ctx, peer.PeerID, "trigger_worker:audit-watcher")
	if ok {
		t.Fatal("scope not revoked")
	}

	// Unknown peer returns (false, nil) — no leak.
	ok, err = db.HasPeerScope(ctx, "12D3UnknownPeer", "trigger_worker:*")
	if err != nil || ok {
		t.Fatalf("unknown peer: ok=%v err=%v", ok, err)
	}
}
