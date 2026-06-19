package admin_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/workers/admin"
)

// newTriggerService spins up the admin Service with the mesh-trigger
// store + peer-scope store wired (both point at the live sqlite store
// because that's what production wires).
func newTriggerService(t *testing.T) (*admin.Service, *sqlite.DB, string, string) {
	t.Helper()
	svc, db, wsID, scopeID := newTestService(t)
	svc.SetMeshTriggerStore(db)
	svc.SetPeerScopeStore(db)
	return svc, db, wsID, scopeID
}

// TestMeshTriggerCRUD walks one trigger through the admin surface +
// confirms validation enforces the documented invariants.
func TestMeshTriggerCRUD(t *testing.T) {
	svc, _, wsID, scopeID := newTriggerService(t)
	ctx := context.Background()
	w, err := svc.Create(ctx, baseCreate(wsID, scopeID))
	if err != nil {
		t.Fatalf("create worker: %v", err)
	}

	// Reject: no match criteria + no AllMessages.
	if _, err := svc.CreateMeshTrigger(ctx, admin.MeshTriggerInput{WorkerID: w.ID}); err == nil {
		t.Fatal("expected error on empty trigger")
	}

	// Reject: invalid regex.
	_, err = svc.CreateMeshTrigger(ctx, admin.MeshTriggerInput{
		WorkerID: w.ID, ContentRegex: "((",
	})
	if err == nil {
		t.Fatal("expected error on invalid regex")
	}

	// Reject: out-of-range chain depth.
	_, err = svc.CreateMeshTrigger(ctx, admin.MeshTriggerInput{
		WorkerID: w.ID, KindMatch: "alert", MaxChainDepth: 11,
	})
	if err == nil {
		t.Fatal("expected error on out-of-range chain depth")
	}

	// Reject: invalid kind.
	_, err = svc.CreateMeshTrigger(ctx, admin.MeshTriggerInput{
		WorkerID: w.ID, KindMatch: "bogus",
	})
	if err == nil {
		t.Fatal("expected error on invalid kind_match")
	}

	// Accept: valid trigger.
	trig, err := svc.CreateMeshTrigger(ctx, admin.MeshTriggerInput{
		WorkerID:        w.ID,
		KindMatch:       "alert",
		TagMatch:        "security,critical",
		ThrottleSeconds: 30,
		MaxChainDepth:   5,
	})
	if err != nil {
		t.Fatalf("create trigger: %v", err)
	}
	if !trig.Enabled {
		t.Fatal("default enabled should be true")
	}
	if trig.ThrottleSeconds != 30 || trig.MaxChainDepth != 5 {
		t.Fatalf("custom throttle/depth lost: %+v", trig)
	}

	// AllMessages shortcut bypasses the "at least one criterion" rule.
	all, err := svc.CreateMeshTrigger(ctx, admin.MeshTriggerInput{
		WorkerID:    w.ID,
		AllMessages: true,
	})
	if err != nil {
		t.Fatalf("all_messages create: %v", err)
	}
	if all.TagMatch != "" || all.KindMatch != "" {
		t.Fatalf("all_messages didn't clear criteria: %+v", all)
	}

	// List + get.
	listed, err := svc.ListMeshTriggers(ctx, w.ID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("expected 2 triggers, got %d", len(listed))
	}
	got, err := svc.GetMeshTrigger(ctx, trig.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != trig.ID {
		t.Fatalf("get returned wrong row: %+v", got)
	}

	// Update — flip enabled off.
	enabledFalse := false
	upd, err := svc.UpdateMeshTrigger(ctx, admin.MeshTriggerInput{
		ID:      trig.ID,
		Enabled: &enabledFalse,
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if upd.Enabled {
		t.Fatal("Enabled false not persisted")
	}

	// Delete + confirm gone.
	if err := svc.DeleteMeshTrigger(ctx, trig.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	listed, _ = svc.ListMeshTriggers(ctx, w.ID)
	if len(listed) != 1 {
		t.Fatalf("expected 1 trigger after delete, got %d", len(listed))
	}
}

// TestMeshTriggerReloaderFires asserts the dispatcher reloader is called
// after every successful mutation so the in-memory cache stays current.
func TestMeshTriggerReloaderFires(t *testing.T) {
	svc, _, wsID, scopeID := newTriggerService(t)
	ctx := context.Background()
	w, _ := svc.Create(ctx, baseCreate(wsID, scopeID))

	var reloadCalls int32
	svc.SetDispatcherReloader(reloaderFunc(func(_ context.Context) error {
		atomic.AddInt32(&reloadCalls, 1)
		return nil
	}))

	trig, err := svc.CreateMeshTrigger(ctx, admin.MeshTriggerInput{
		WorkerID:  w.ID,
		KindMatch: "alert",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if atomic.LoadInt32(&reloadCalls) != 1 {
		t.Fatalf("create did not reload: %d", reloadCalls)
	}

	enabledFalse := false
	if _, err := svc.UpdateMeshTrigger(ctx, admin.MeshTriggerInput{
		ID: trig.ID, Enabled: &enabledFalse,
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if atomic.LoadInt32(&reloadCalls) != 2 {
		t.Fatalf("update did not reload: %d", reloadCalls)
	}

	if err := svc.DeleteMeshTrigger(ctx, trig.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if atomic.LoadInt32(&reloadCalls) != 3 {
		t.Fatalf("delete did not reload: %d", reloadCalls)
	}
}

// TestMeshTriggerPeerGrant exercises the peer-scope grant convenience
// methods + verifies they format the scope string the dispatcher
// expects.
func TestMeshTriggerPeerGrant(t *testing.T) {
	svc, db, _, _ := newTriggerService(t)
	ctx := context.Background()
	const peerID = "12D3KooWMyPeer"
	if err := db.AddPeer(ctx, &store.P2PPeer{PeerID: peerID, DisplayName: "lab"}); err != nil {
		t.Fatalf("add peer: %v", err)
	}

	scope, err := svc.GrantTriggerToPeer(ctx, peerID, "audit-watcher")
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if scope != "trigger_worker:audit-watcher" {
		t.Fatalf("scope = %q", scope)
	}
	ok, _ := db.HasPeerScope(ctx, peerID, scope)
	if !ok {
		t.Fatal("scope not persisted")
	}

	// Wildcard grant.
	scope, err = svc.GrantTriggerToPeer(ctx, peerID, "*")
	if err != nil {
		t.Fatalf("wildcard grant: %v", err)
	}
	if scope != "trigger_worker:*" {
		t.Fatalf("wildcard scope = %q", scope)
	}

	// Revoke.
	if _, err := svc.RevokeTriggerGrant(ctx, peerID, "audit-watcher"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	ok, _ = db.HasPeerScope(ctx, peerID, "trigger_worker:audit-watcher")
	if ok {
		t.Fatal("scope not removed")
	}

	// Empty inputs return errors.
	if _, err := svc.GrantTriggerToPeer(ctx, "", "x"); err == nil {
		t.Fatal("expected error on empty peer_id")
	}
	if _, err := svc.GrantTriggerToPeer(ctx, peerID, ""); err == nil {
		t.Fatal("expected error on empty worker_name")
	}
}

// reloaderFunc adapts a closure to the DispatcherReloader interface.
type reloaderFunc func(ctx context.Context) error

func (f reloaderFunc) Reload(ctx context.Context) error { return f(ctx) }
