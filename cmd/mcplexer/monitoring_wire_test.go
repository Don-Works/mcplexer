package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestBuildMonitoringLateBindsMeshSender is the regression for the
// log-watcher LXC rollout. During daemon boot buildMonitoring is first
// called before the mesh.Manager exists (meshMgr=nil); monitoringOnce
// then seals the dispatcher. A later call carrying a live manager must
// still wire the kind=mesh sender onto that sealed dispatcher — before
// the late-bind fix a configured Monitoring mesh channel logged
// "escalate: no sender wired for channel kind" forever and never
// delivered. We prove the fix end-to-end: a critical incident is only
// persisted onto the workspace mesh AFTER the non-nil-later build.
func TestBuildMonitoringLateBindsMeshSender(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "monitoring-wire.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}

	ws := &store.Workspace{ID: "ws-logwatch", Name: "Log Watch"}
	if err := db.CreateWorkspace(ctx, ws); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	ch := &store.MonitoringChannel{
		WorkspaceID: ws.ID,
		Name:        "mesh-alerts",
		Kind:        store.ChannelKindMesh,
		MinSeverity: store.SeverityWarn,
		Enabled:     true,
	}
	if err := db.CreateMonitoringChannel(ctx, ch); err != nil {
		t.Fatalf("CreateMonitoringChannel: %v", err)
	}

	// Drive a fresh nil-first/non-nil-later sequence regardless of any
	// prior test that touched the process-wide monitoring singletons.
	resetMonitoringSingletons()
	t.Cleanup(resetMonitoringSingletons)

	notif := distill.Notification{
		WorkspaceID:    ws.ID,
		Severity:       store.SeverityCritical,
		Title:          "disk pressure",
		RemoteHostName: "lxc-logwatch",
		Test:           true, // bypass throttle; still fans out to senders
	}

	// Phase 1 — first build with meshMgr=nil (boot order). The dispatcher
	// exists but has no mesh sender, so notify delivers nothing to mesh.
	buildMonitoring(db, nil, nil)
	if err := monitoringDispatch.Notify(ctx, notif); err != nil {
		t.Fatalf("notify (nil mesh): %v", err)
	}
	if n := countLiveMeshMessages(ctx, t, db, ws.ID); n != 0 {
		t.Fatalf("mesh messages before late-bind = %d, want 0", n)
	}

	// Phase 2 — a later build carrying a live manager must late-bind the
	// kind=mesh sender onto the already-sealed dispatcher.
	mgr := mesh.NewManager(db)
	buildMonitoring(db, nil, mgr)
	if err := monitoringDispatch.Notify(ctx, notif); err != nil {
		t.Fatalf("notify (mesh wired): %v", err)
	}
	if n := countLiveMeshMessages(ctx, t, db, ws.ID); n != 1 {
		t.Fatalf("mesh messages after late-bind = %d, want 1", n)
	}
}

func countLiveMeshMessages(ctx context.Context, t *testing.T, db store.Store, wsID string) int {
	t.Helper()
	msgs, err := db.QueryMeshMessages(ctx, store.MeshMessageFilter{
		WorkspaceIDs: []string{wsID},
		StatusLive:   true,
	})
	if err != nil {
		t.Fatalf("QueryMeshMessages: %v", err)
	}
	return len(msgs)
}

// resetMonitoringSingletons clears the process-wide monitoring wiring so
// a test can replay the daemon's build sequence from scratch.
func resetMonitoringSingletons() {
	monitoringOnce = sync.Once{}
	monitoringColOnce = sync.Once{}
	monitoringDispatch = nil
	monitoringQry = nil
	monitoringCollector = nil
}
