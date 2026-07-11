// workspace_links_handler_test.go — HTTP-level tests for the linked-
// workspace REST surface (migration 088). Mirrors the MCP control-tool
// coverage but over the in-process REST path the dashboard + the docker
// integration harness use.
package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func newLinksTestServer(t *testing.T) (*httptest.Server, *sqlite.DB) {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "links.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	srv := httptest.NewServer(NewRouter(RouterDeps{APIToken: "", Store: db}))
	t.Cleanup(srv.Close)
	return srv, db
}

func doReq(t *testing.T, srv *httptest.Server, method, path, body string) []byte {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = res.Body.Close() }()
	out, _ := io.ReadAll(res.Body)
	if res.StatusCode >= 400 {
		t.Fatalf("%s %s -> %d: %s", method, path, res.StatusCode, out)
	}
	return out
}

func TestWorkspaceLinkRESTLifecycle(t *testing.T) {
	srv, db := newLinksTestServer(t)
	ctx := context.Background()

	ws := &store.Workspace{Name: "gateway", RootPath: "/tmp/gw", Tags: json.RawMessage("[]")}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := db.AddPeer(ctx, &store.P2PPeer{PeerID: "peer-B", DisplayName: "vm"}); err != nil {
		t.Fatalf("add peer: %v", err)
	}

	// Create the link by workspace NAME.
	body := `{"peer_id":"peer-B","local_workspace":"gateway","remote_workspace_id":"remote-ws-1","remote_workspace_name":"bravo"}`
	resp := doReq(t, srv, http.MethodPost, "/api/v1/workspace-links", body)
	var created struct {
		Linked           bool   `json:"linked"`
		GrantedScope     string `json:"granted_scope"`
		GrantedSyncScope string `json:"granted_sync_scope"`
		LocalWsID        string `json:"local_workspace_id"`
	}
	if err := json.Unmarshal(resp, &created); err != nil {
		t.Fatalf("unmarshal create: %v (%s)", err, resp)
	}
	if !created.Linked || created.LocalWsID != ws.ID {
		t.Fatalf("unexpected create response: %+v", created)
	}
	if created.GrantedScope != "task_assign:bravo" {
		t.Fatalf("granted_scope = %q, want task_assign:bravo", created.GrantedScope)
	}
	// Parity with the control tool: the link also grants task_sync keyed
	// by the LOCAL workspace id so the peer can catch up over task-sync.
	wantSync := "task_sync:" + ws.ID
	if created.GrantedSyncScope != wantSync {
		t.Fatalf("granted_sync_scope = %q, want %q", created.GrantedSyncScope, wantSync)
	}
	if ok, _ := db.HasPeerScope(ctx, "peer-B", "task_assign:bravo"); !ok {
		t.Fatalf("link did not grant task_assign:bravo to peer-B")
	}
	if ok, _ := db.HasPeerScope(ctx, "peer-B", wantSync); !ok {
		t.Fatalf("link did not grant %s to peer-B", wantSync)
	}

	// List shows it.
	var links []linkView
	if err := json.Unmarshal(doReq(t, srv, http.MethodGet, "/api/v1/workspace-links", ""), &links); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(links) != 1 || links[0].PeerID != "peer-B" || links[0].LocalWorkspaceName != "gateway" {
		t.Fatalf("unexpected list: %+v", links)
	}

	// Unlink revokes BOTH scopes + drops the row.
	doReq(t, srv, http.MethodDelete, "/api/v1/workspace-links?peer_id=peer-B&remote_workspace_id=remote-ws-1", "")
	if ok, _ := db.HasPeerScope(ctx, "peer-B", "task_assign:bravo"); ok {
		t.Fatalf("unlink did not revoke task_assign:bravo")
	}
	if ok, _ := db.HasPeerScope(ctx, "peer-B", wantSync); ok {
		t.Fatalf("unlink did not revoke %s", wantSync)
	}
	links = nil
	if err := json.Unmarshal(doReq(t, srv, http.MethodGet, "/api/v1/workspace-links", ""), &links); err != nil {
		t.Fatalf("unmarshal list after unlink: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("got %d links after unlink, want 0", len(links))
	}
}
