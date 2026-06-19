// task_sync_wire_test.go — coverage for the cmd-layer glue behind the
// /mcplexer/task-sync/1.0.0 gossip protocol: the daemon-side service
// construction (smoke), the pair checker, the per-workspace scope gate,
// and the store-backed event source. Runs in BOTH build flavours — in
// the slim build the constructor returns the stub, which is exactly the
// "daemon constructs the service" contract being pinned.
package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// newTaskSyncTestDB returns an in-memory sqlite store + a seeded
// workspace id.
func newTaskSyncTestDB(t *testing.T) (*sqlite.DB, *store.Workspace) {
	t.Helper()
	d, err := sqlite.New(context.Background(), ":memory:")
	if err != nil {
		t.Fatalf("open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	w := &store.Workspace{Name: "sync-ws", RootPath: "/tmp/sync", Tags: json.RawMessage("[]")}
	if err := d.CreateWorkspace(context.Background(), w); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	return d, w
}

// TestBuildTaskSyncServiceSmoke pins the wiring contract serve.go
// relies on: a non-nil service in every build mode, even with a nil
// host (p2p disabled), so d.defer_(d.taskSync.Stop) never panics.
func TestBuildTaskSyncServiceSmoke(t *testing.T) {
	d, _ := newTaskSyncTestDB(t)
	svc := buildTaskSyncService(nil, d, tasks.New(d), nil, nil, nil)
	if svc == nil {
		t.Fatalf("buildTaskSyncService returned nil")
	}
	svc.Stop() // must not panic in either build mode
}

// TestTaskSyncPairChecker covers the outer ACL: unknown and revoked
// peers are NOT paired; an active row is.
func TestTaskSyncPairChecker(t *testing.T) {
	ctx := context.Background()
	d, _ := newTaskSyncTestDB(t)
	checker := &taskSyncPairChecker{db: d}

	if ok, err := checker.IsPaired(ctx, "peer-unknown"); err != nil || ok {
		t.Fatalf("unknown peer: got (%v, %v), want (false, nil)", ok, err)
	}
	if err := d.AddPeer(ctx, &store.P2PPeer{PeerID: "peer-A", DisplayName: "a"}); err != nil {
		t.Fatalf("add peer: %v", err)
	}
	if ok, err := checker.IsPaired(ctx, "peer-A"); err != nil || !ok {
		t.Fatalf("active peer: got (%v, %v), want (true, nil)", ok, err)
	}
	if err := d.RevokePeer(ctx, "peer-A"); err != nil {
		t.Fatalf("revoke peer: %v", err)
	}
	if ok, err := checker.IsPaired(ctx, "peer-A"); err != nil || ok {
		t.Fatalf("revoked peer: got (%v, %v), want (false, nil)", ok, err)
	}
}

// TestTaskSyncScopeChecker_GrantShapes is the security gate: only the
// id-form grant (what link_workspace writes), the name-form grant (what
// a human picks in the dashboard), or the wildcard admit the pull. No
// grant — including unrelated task scopes — must deny.
func TestTaskSyncScopeChecker_GrantShapes(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name  string
		grant string // "" = no grant
		want  bool
	}{
		{name: "no grant denies", grant: "", want: false},
		{name: "unrelated scope denies", grant: "task_assign:sync-ws", want: false},
		{name: "workspace id grant admits", grant: "task_sync:<ID>", want: true},
		{name: "workspace name grant admits", grant: "task_sync:sync-ws", want: true},
		{name: "wildcard admits", grant: "task_sync:*", want: true},
		{name: "other workspace id denies", grant: "task_sync:some-other-ws", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ws := newTaskSyncTestDB(t)
			if err := d.AddPeer(ctx, &store.P2PPeer{PeerID: "peer-B", DisplayName: "b"}); err != nil {
				t.Fatalf("add peer: %v", err)
			}
			if tc.grant != "" {
				grant := tc.grant
				if grant == "task_sync:<ID>" {
					grant = "task_sync:" + ws.ID
				}
				if err := d.GrantPeerScope(ctx, "peer-B", grant); err != nil {
					t.Fatalf("grant %q: %v", grant, err)
				}
			}
			checker := &taskSyncScopeChecker{store: d}
			ok, err := checker.HasTaskSyncScope(ctx, "peer-B", ws.ID)
			if err != nil {
				t.Fatalf("HasTaskSyncScope: %v", err)
			}
			if ok != tc.want {
				t.Fatalf("HasTaskSyncScope = %v, want %v (grant %q)", ok, tc.want, tc.grant)
			}
		})
	}
}

// TestTaskSyncSource_ListAndWatermark pins the store→wire conversion:
// rows page out in HLC order as decodable events, the since-watermark
// filters already-seen rows, and MaxHLCForWorkspace tracks the newest
// stamp.
func TestTaskSyncSource_ListAndWatermark(t *testing.T) {
	ctx := context.Background()
	d, ws := newTaskSyncTestDB(t)
	svc := tasks.New(d)

	first, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: ws.ID, Title: "first", CreatedBySessionID: "s",
	})
	if err != nil {
		t.Fatalf("create first: %v", err)
	}
	second, err := svc.Create(ctx, tasks.CreateOptions{
		WorkspaceID: ws.ID, Title: "second", CreatedBySessionID: "s",
	})
	if err != nil {
		t.Fatalf("create second: %v", err)
	}

	src := &taskSyncSource{store: d, selfPeerID: "self-peer"}
	evts, err := src.ListTasksSinceHLC(ctx, ws.ID, "", 100)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(evts) != 2 {
		t.Fatalf("got %d events, want 2", len(evts))
	}
	if evts[0].TaskID != first.ID || evts[1].TaskID != second.ID {
		t.Fatalf("HLC order wrong: %s, %s", evts[0].TaskID, evts[1].TaskID)
	}
	var patch tasks.RemoteTaskPatch
	if err := json.Unmarshal(evts[0].FieldPatchesJSON, &patch); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if patch.Title != "first" {
		t.Fatalf("patch title = %q, want first", patch.Title)
	}
	if evts[0].ByPeer != "self-peer" {
		t.Fatalf("by_peer = %q, want self-peer (local rows attribute to self)", evts[0].ByPeer)
	}

	// Watermark filtering — only rows newer than the first stamp.
	tail, err := src.ListTasksSinceHLC(ctx, ws.ID, first.HlcAt, 100)
	if err != nil {
		t.Fatalf("list since: %v", err)
	}
	if len(tail) != 1 || tail[0].TaskID != second.ID {
		t.Fatalf("since-filter wrong: %+v", tail)
	}

	max, err := src.MaxHLCForWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("max hlc: %v", err)
	}
	if max != second.HlcAt {
		t.Fatalf("max = %q, want %q", max, second.HlcAt)
	}
}
