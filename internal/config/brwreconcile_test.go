package config

import (
	"context"
	"testing"
)

// ReconcileBrwProfiles must apply the roster — write servers + routes — even
// though it never takes a DryRun argument. This is the gateway auto-discovery
// happy path: a fresh store gains one stdio server + one route per daemon.
func TestReconcileBrwProfiles_AppliesRoster(t *testing.T) {
	ctx := context.Background()
	db := newBrwTestStore(t)
	svc := NewService(db)

	plan, err := ReconcileBrwProfiles(ctx, svc, db, sampleBrwDaemons(), SyncOptions{
		Workspaces: []string{"global"},
		// DryRun intentionally left true to prove ReconcileBrwProfiles
		// overrides it to false.
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if plan.DryRun {
		t.Fatalf("ReconcileBrwProfiles must force DryRun=false")
	}
	if got := countActions(plan, ActionCreated); got != 4 {
		t.Fatalf("created actions = %d, want 4 (2 servers + 2 routes)", got)
	}
	if got := countBrwServers(t, db); got != 2 {
		t.Errorf("brw server count = %d, want 2", got)
	}
	if got := countBrwRoutes(t, db); got != 2 {
		t.Errorf("brw route count = %d, want 2", got)
	}
}

// Re-running a reconcile against the same roster must be a no-op: no new
// servers/routes, every action unchanged. Idempotency is the whole point of
// the periodic interval fallback firing on top of the file_watch.
func TestReconcileBrwProfiles_Idempotent(t *testing.T) {
	ctx := context.Background()
	db := newBrwTestStore(t)
	svc := NewService(db)
	daemons := sampleBrwDaemons()
	opts := SyncOptions{Workspaces: []string{"global"}}

	if _, err := ReconcileBrwProfiles(ctx, svc, db, daemons, opts); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	plan, err := ReconcileBrwProfiles(ctx, svc, db, daemons, opts)
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if c := countActions(plan, ActionCreated); c != 0 {
		t.Errorf("re-run created %d, want 0", c)
	}
	if c := countActions(plan, ActionUpdated); c != 0 {
		t.Errorf("re-run updated %d, want 0", c)
	}
	if c := countActions(plan, ActionUnchanged); c != 4 {
		t.Errorf("re-run unchanged %d, want 4", c)
	}
	if got := countBrwServers(t, db); got != 2 {
		t.Errorf("brw server count after re-run = %d, want 2", got)
	}
}

// With Prune set, a reconcile against an empty roster removes the
// source="brw" rows a previous reconcile created — the "brwd shut down, its
// namespace disappears" path.
func TestReconcileBrwProfiles_PruneRemovesStale(t *testing.T) {
	ctx := context.Background()
	db := newBrwTestStore(t)
	svc := NewService(db)

	if _, err := ReconcileBrwProfiles(ctx, svc, db, sampleBrwDaemons(), SyncOptions{
		Workspaces: []string{"global"},
	}); err != nil {
		t.Fatalf("seed reconcile: %v", err)
	}
	if got := countBrwServers(t, db); got != 2 {
		t.Fatalf("precondition: brw server count = %d, want 2", got)
	}

	plan, err := ReconcileBrwProfiles(ctx, svc, db, nil, SyncOptions{
		Workspaces: []string{"global"}, Prune: true,
	})
	if err != nil {
		t.Fatalf("prune reconcile: %v", err)
	}
	// 2 servers + 2 routes pruned.
	if c := countActions(plan, ActionPruned); c != 4 {
		t.Errorf("pruned actions = %d, want 4", c)
	}
	if got := countBrwServers(t, db); got != 0 {
		t.Errorf("brw server count after prune = %d, want 0", got)
	}
	if got := countBrwRoutes(t, db); got != 0 {
		t.Errorf("brw route count after prune = %d, want 0", got)
	}
}

// Without Prune, an empty roster leaves the previously-created rows in place
// — the interval fallback firing while brwctl is momentarily unreachable must
// not nuke a working namespace.
func TestReconcileBrwProfiles_NoPruneKeepsRows(t *testing.T) {
	ctx := context.Background()
	db := newBrwTestStore(t)
	svc := NewService(db)

	if _, err := ReconcileBrwProfiles(ctx, svc, db, sampleBrwDaemons(), SyncOptions{
		Workspaces: []string{"global"},
	}); err != nil {
		t.Fatalf("seed reconcile: %v", err)
	}
	if _, err := ReconcileBrwProfiles(ctx, svc, db, nil, SyncOptions{
		Workspaces: []string{"global"}, // Prune defaults false
	}); err != nil {
		t.Fatalf("empty reconcile: %v", err)
	}
	if got := countBrwServers(t, db); got != 2 {
		t.Errorf("brw server count = %d, want 2 (no prune)", got)
	}
}

func TestParseBrwDaemons(t *testing.T) {
	raw := []byte(`
	[
	  {
	    "name": "work-profile",
	    "kind": "bridge",
	    "workspace": "brw",
	    "http_addr": "http://127.0.0.1:17310",
	    "reachable": true,
	    "identity": {"workspace": "brw", "profile": "work-profile", "mode": "bridge"}
	  }
	]
	`)
	daemons, err := ParseBrwDaemons(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(daemons) != 1 {
		t.Fatalf("len = %d, want 1", len(daemons))
	}
	d := daemons[0]
	if d.Name != "work-profile" {
		t.Errorf("name = %q, want work-profile", d.Name)
	}
	if d.HTTPAddr != "http://127.0.0.1:17310" {
		t.Errorf("http_addr = %q", d.HTTPAddr)
	}
	if d.Identity.Workspace != "brw" || d.Identity.Profile != "work-profile" {
		t.Errorf("identity = %+v", d.Identity)
	}
}

func TestParseBrwDaemons_EmptyIsNilNoError(t *testing.T) {
	for _, in := range []string{"", "  \n ", "[]", "null"} {
		daemons, err := ParseBrwDaemons([]byte(in))
		if err != nil {
			t.Errorf("ParseBrwDaemons(%q) err = %v", in, err)
		}
		if len(daemons) != 0 {
			t.Errorf("ParseBrwDaemons(%q) len = %d, want 0", in, len(daemons))
		}
	}
}

func TestParseBrwDaemons_BadJSON(t *testing.T) {
	if _, err := ParseBrwDaemons([]byte(`{not json`)); err == nil {
		t.Fatalf("expected error on malformed JSON")
	}
}
