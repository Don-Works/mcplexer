// worker_triggers_handler_test.go (M4) — HTTP integration coverage for
// the per-Worker mesh-trigger CRUD + per-peer trigger-grant convenience.
// Spins up the real sqlite-backed admin.Service so a passing test
// confirms the wire format AND the underlying validation hold.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
)

// contextTODO is a tiny helper so tests don't sprinkle context.Background()
// every line. Returns a fresh background context.
func contextTODO() context.Context { return context.Background() }

// newTriggersTestServer extends newWorkersTestServer with the
// mesh-trigger + peer-scope wiring the M4 endpoints need.
func newTriggersTestServer(t *testing.T) (*httptest.Server, string, string) {
	t.Helper()
	srv, db, wsID, scopeID := newWorkersTestServer(t)
	// Replace the bare svc with one that has trigger wiring — we
	// re-register the router so the worker_triggers routes pick up the
	// configured svc.
	svc := workersadmin.New(db, workersadmin.Options{Workspaces: db})
	svc.SetMeshTriggerStore(db)
	svc.SetPeerScopeStore(db)
	r := NewRouter(RouterDeps{
		APIToken:    "",
		Store:       db,
		WorkerAdmin: svc,
	})
	srv.Config.Handler = r
	return srv, wsID, scopeID
}

func TestMeshTriggerHTTPLifecycle(t *testing.T) {
	srv, wsID, scopeID := newTriggersTestServer(t)
	// Create the worker first.
	created := postJSON(t, srv.URL+"/api/v1/workers", map[string]any{
		"name":            "trigger-host",
		"model_provider":  "anthropic",
		"model_id":        "claude-opus-4-7",
		"secret_scope_id": scopeID,
		"prompt_template": "p",
		"schedule_spec":   "0 9 * * *",
		"workspace_id":    wsID,
	}, http.StatusCreated)
	workerID, _ := created["id"].(string)
	if workerID == "" {
		t.Fatalf("worker id missing: %+v", created)
	}

	// Empty list = []
	rows := mustListTriggers(t, srv, workerID)
	if len(rows) != 0 {
		t.Fatalf("expected 0 triggers, got %d", len(rows))
	}

	// Bad payload: missing criterion + no all_messages.
	postJSON(t, srv.URL+"/api/v1/workers/"+workerID+"/mesh-triggers",
		map[string]any{}, http.StatusBadRequest)

	// Valid create.
	trig := postJSON(t, srv.URL+"/api/v1/workers/"+workerID+"/mesh-triggers",
		map[string]any{
			"kind_match":       "alert",
			"tag_match":        "security",
			"throttle_seconds": 30,
			"max_chain_depth":  5,
		}, http.StatusCreated)
	triggerID, _ := trig["id"].(string)
	if triggerID == "" {
		t.Fatalf("trigger id missing: %+v", trig)
	}

	// List now has the row.
	rows = mustListTriggers(t, srv, workerID)
	if len(rows) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(rows))
	}

	// PATCH — disable.
	updated := postJSONMethod(t, http.MethodPatch,
		srv.URL+"/api/v1/workers/"+workerID+"/mesh-triggers/"+triggerID,
		map[string]any{"enabled": false},
		http.StatusOK,
	)
	if updated["enabled"].(bool) {
		t.Fatalf("enabled flag not flipped: %+v", updated)
	}

	// DELETE.
	postJSONMethod(t, http.MethodDelete,
		srv.URL+"/api/v1/workers/"+workerID+"/mesh-triggers/"+triggerID,
		nil, http.StatusNoContent)
	rows = mustListTriggers(t, srv, workerID)
	if len(rows) != 0 {
		t.Fatalf("expected 0 triggers after delete, got %d", len(rows))
	}
}

func TestPeerTriggerGrantHTTP(t *testing.T) {
	srv, _, _ := newTriggersTestServer(t)
	// Seed a paired peer via the store. We get the *sqlite.DB by
	// recovering it from the Router deps — but the server set up by
	// newTriggersTestServer doesn't expose it. Seed via the auth-token
	// PostgresJSON path instead: simulate via a sibling subtest that
	// directly grants on the same DB-backed svc.
	//
	// Practical approach: open a second sqlite DB just for this test,
	// or assume the trigger-grant endpoint returns 4xx when the peer
	// doesn't exist. The current store implementation returns ErrNotFound
	// from GrantPeerScope, which the handler maps to 404.
	resp := postJSONRaw(t, http.MethodPost,
		srv.URL+"/api/v1/peers/12D3UnknownPeer/trigger-grants",
		map[string]any{"worker_name": "x"},
	)
	// Unknown peer -> 404.
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown peer, got %d", resp.StatusCode)
	}
}

// TestPeerTriggerGrantHTTPSuccess seeds a peer directly on the store and
// confirms the grant endpoint persists the scope. We use a fresh test
// rig so the peer-store handle is available.
func TestPeerTriggerGrantHTTPSuccess(t *testing.T) {
	srv, db, _, _ := newWorkersTestServer(t)
	svc := workersadmin.New(db, workersadmin.Options{Workspaces: db})
	svc.SetMeshTriggerStore(db)
	svc.SetPeerScopeStore(db)
	r := NewRouter(RouterDeps{
		APIToken:    "",
		Store:       db,
		WorkerAdmin: svc,
	})
	srv.Config.Handler = r
	const peerID = "12D3KooWHttpTest"
	ctx := contextTODO()
	if err := db.AddPeer(ctx, &store.P2PPeer{PeerID: peerID, DisplayName: "rig"}); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	got := postJSON(t, srv.URL+"/api/v1/peers/"+peerID+"/trigger-grants",
		map[string]any{"worker_name": "audit-watcher"}, http.StatusCreated)
	if got["scope"].(string) != "trigger_worker:audit-watcher" {
		t.Fatalf("scope: %+v", got)
	}
	ok, _ := db.HasPeerScope(ctx, peerID, "trigger_worker:audit-watcher")
	if !ok {
		t.Fatal("scope not persisted")
	}

	// Revoke.
	postJSONMethod(t, http.MethodDelete,
		srv.URL+"/api/v1/peers/"+peerID+"/trigger-grants/audit-watcher",
		nil, http.StatusNoContent)
	ok, _ = db.HasPeerScope(ctx, peerID, "trigger_worker:audit-watcher")
	if ok {
		t.Fatal("scope still present after revoke")
	}
}

// mustListTriggers GETs the trigger list and decodes.
func mustListTriggers(t *testing.T, srv *httptest.Server, workerID string) []map[string]any {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/v1/workers/" + workerID + "/mesh-triggers")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status %d", resp.StatusCode)
	}
	var rows []map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&rows); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rows
}

// postJSONRaw mirrors postJSONMethod but returns the raw *http.Response
// so tests asserting non-2xx status without a JSON body can inspect it
// directly.
func postJSONRaw(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, err := http.NewRequest(method, url, &buf)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	// drain body so callers don't leak.
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}
