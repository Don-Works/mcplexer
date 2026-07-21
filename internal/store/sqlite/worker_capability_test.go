package sqlite_test

import (
	"context"
	"testing"
)

// TestWorkerCapabilityProfileRoundTrip proves the capability_profile_json
// column persists through create/get/update and defaults to "" (no profile =
// allow-all back-compat) when unset.
func TestWorkerCapabilityProfileRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "scoped-worker")
	w.CapabilityProfileJSON = `{"preset":"coder","namespace_allow":["github","mcpx"]}`
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}

	got, err := db.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if got.CapabilityProfileJSON != w.CapabilityProfileJSON {
		t.Fatalf("capability_profile_json round-trip = %q, want %q",
			got.CapabilityProfileJSON, w.CapabilityProfileJSON)
	}

	// Update to a new profile.
	got.CapabilityProfileJSON = `{"preset":"minimal","namespace_allow":["mcpx"]}`
	if err := db.UpdateWorker(ctx, got); err != nil {
		t.Fatalf("update worker: %v", err)
	}
	reGot, err := db.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatalf("re-get worker: %v", err)
	}
	if reGot.CapabilityProfileJSON != got.CapabilityProfileJSON {
		t.Fatalf("post-update capability_profile_json = %q, want %q",
			reGot.CapabilityProfileJSON, got.CapabilityProfileJSON)
	}
}

// TestWorkerCapabilityProfileDefaultsEmpty proves an unset profile persists
// as "" — the back-compat allow-all signal.
func TestWorkerCapabilityProfileDefaultsEmpty(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "plain-worker")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	got, err := db.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if got.CapabilityProfileJSON != "" {
		t.Fatalf("unset capability_profile_json = %q, want empty", got.CapabilityProfileJSON)
	}
}
