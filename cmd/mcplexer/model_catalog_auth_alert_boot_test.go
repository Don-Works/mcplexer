package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// This file guards the operator's "unauthenticated harness = red flag -> mesh
// notification" feature against the failure mode this codebase keeps hitting:
// correct, well-tested code that NOTHING CALLS. The auth-alert evaluator is
// dead unless the refresh loop invokes it, so the wiring is asserted at BOTH
// levels — a runtime test that the boot constructor attaches the hook, and a
// source test that fails if the wiring line is removed.

// TestModelCatalogAuthAlertWiredIntoRefresh is the level-1 guard: the boot
// constructor attaches the auth-alert evaluation to the refresh loop.
func TestModelCatalogAuthAlertWiredIntoRefresh(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "auth-alert-boot.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	r := newModelCatalogRefresher(db, mesh.NewManager(db))
	if r == nil {
		t.Fatal("newModelCatalogRefresher returned nil")
	}
	if !r.HasRefreshHook() {
		t.Fatal("auth-alert evaluation is NOT wired into the catalog refresh loop — " +
			"an enabled-but-unauthenticated provider would never notify the operator, " +
			"which is exactly the red flag this feature exists to raise")
	}
}

// fakeMeshPoster records the mesh sends the notifier makes.
type fakeMeshPoster struct {
	metas []mesh.SessionMeta
	reqs  []mesh.SendRequest
}

func (f *fakeMeshPoster) Send(_ context.Context, meta mesh.SessionMeta, req mesh.SendRequest) (*store.MeshMessage, error) {
	f.metas = append(f.metas, meta)
	f.reqs = append(f.reqs, req)
	return &store.MeshMessage{}, nil
}

// TestMeshAuthAlertNotifierMapping — the notifier maps an AuthAlert onto a
// human-facing, operator-wide mesh alert (global namespace, NotifyUser, correct
// priority per direction) and leaks no secret.
func TestMeshAuthAlertNotifierMapping(t *testing.T) {
	fake := &fakeMeshPoster{}
	n := meshAuthAlertNotifier{mesh: fake}

	msg := "Delegation provider grok_cli is ENABLED but UNAUTHENTICATED — ..."
	if err := n.Notify(context.Background(), models.AuthAlert{Provider: "grok_cli", Message: msg}); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	if err := n.Notify(context.Background(), models.AuthAlert{Provider: "grok_cli", Recovered: true, Message: "recovered"}); err != nil {
		t.Fatalf("Notify recovery: %v", err)
	}
	if len(fake.reqs) != 2 {
		t.Fatalf("got %d sends, want 2", len(fake.reqs))
	}

	alert := fake.reqs[0]
	if alert.Content != msg {
		t.Fatalf("alert content mangled: %q", alert.Content)
	}
	if alert.Priority != "high" {
		t.Fatalf("unauthenticated alert priority = %q, want high", alert.Priority)
	}
	if alert.ToWorkspace != "*" {
		t.Fatalf("alert not operator-wide: ToWorkspace = %q, want *", alert.ToWorkspace)
	}
	if !alert.NotifyUser {
		t.Fatal("alert does not buzz the operator (NotifyUser=false)")
	}
	if alert.ActorKind != "system" {
		t.Fatalf("alert ActorKind = %q, want system", alert.ActorKind)
	}
	if !strings.Contains(alert.Tags, "auth-unauthenticated") || !strings.Contains(alert.Tags, "grok_cli") {
		t.Fatalf("alert tags not classifiable: %q", alert.Tags)
	}
	if fake.metas[0].SessionID == "" || fake.metas[0].ClientType != "system" {
		t.Fatalf("alert session meta not system-scoped: %+v", fake.metas[0])
	}

	if rec := fake.reqs[1]; rec.Priority != "normal" || !strings.Contains(rec.Tags, "auth-recovered") {
		t.Fatalf("recovery send mis-mapped: priority=%q tags=%q", rec.Priority, rec.Tags)
	}
}

// TestMeshAuthAlertNotifierNilMeshSafe — a notifier with no transport no-ops
// rather than panicking (boot before the mesh manager exists).
func TestMeshAuthAlertNotifierNilMeshSafe(t *testing.T) {
	n := meshAuthAlertNotifier{}
	if err := n.Notify(context.Background(), models.AuthAlert{Provider: "grok_cli", Message: "x"}); err != nil {
		t.Fatalf("nil-mesh notify: %v", err)
	}
}

// TestServeBootWiresModelCatalogAuthAlert is the level-2 guard: it reads the
// wire source and fails if the daemon stops constructing the auth-alert
// evaluator or stops attaching it as the refresh hook.
func TestServeBootWiresModelCatalogAuthAlert(t *testing.T) {
	src, err := os.ReadFile("model_catalog_wire.go")
	if err != nil {
		t.Fatalf("read model_catalog_wire.go: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "models.NewAuthAlerter(") {
		t.Error("model_catalog_wire.go no longer constructs the auth-alert evaluator")
	}
	if !strings.Contains(s, "OnRefresh: alerter.Evaluate") {
		t.Error("model_catalog_wire.go no longer attaches the auth-alert evaluation " +
			"to the refresh loop — enabled-but-unauthenticated providers would go unreported")
	}
}
