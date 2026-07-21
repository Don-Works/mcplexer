package sqlite_test

import (
	"context"
	"testing"
)

// TestWorkerExecuteScriptsRoundTrip proves the pre_execute_script /
// post_execute_script columns persist through create/get/update and survive a
// clear-on-update (back to "").
func TestWorkerExecuteScriptsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "hooked-worker")
	w.PreExecuteScript = `const r = fetch.fetch({url:"https://x"}); if (!r) abort("no gate");`
	w.PostExecuteScript = `if ((hook.run.output||"").length < 5) abort("too short");`
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}

	got, err := db.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if got.PreExecuteScript != w.PreExecuteScript {
		t.Fatalf("pre_execute_script round-trip = %q, want %q", got.PreExecuteScript, w.PreExecuteScript)
	}
	if got.PostExecuteScript != w.PostExecuteScript {
		t.Fatalf("post_execute_script round-trip = %q, want %q", got.PostExecuteScript, w.PostExecuteScript)
	}

	// Clearing a hook on update persists as "".
	got.PreExecuteScript = ""
	if err := db.UpdateWorker(ctx, got); err != nil {
		t.Fatalf("update worker: %v", err)
	}
	reGot, err := db.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatalf("re-get worker: %v", err)
	}
	if reGot.PreExecuteScript != "" {
		t.Fatalf("cleared pre_execute_script = %q, want empty", reGot.PreExecuteScript)
	}
	if reGot.PostExecuteScript != w.PostExecuteScript {
		t.Fatalf("post_execute_script after update = %q, want %q", reGot.PostExecuteScript, w.PostExecuteScript)
	}
}

// TestWorkerExecuteScriptsDefaultEmpty proves unset hooks persist as "".
func TestWorkerExecuteScriptsDefaultEmpty(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	wsID, scopeID := seedWorkspaceAndScope(t, db, ctx)

	w := newWorker(wsID, scopeID, "plain-hooks-worker")
	if err := db.CreateWorker(ctx, w); err != nil {
		t.Fatalf("create worker: %v", err)
	}
	got, err := db.GetWorker(ctx, w.ID)
	if err != nil {
		t.Fatalf("get worker: %v", err)
	}
	if got.PreExecuteScript != "" || got.PostExecuteScript != "" {
		t.Fatalf("unset scripts = (%q, %q), want both empty", got.PreExecuteScript, got.PostExecuteScript)
	}
}
