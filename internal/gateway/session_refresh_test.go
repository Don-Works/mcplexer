package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/don-works/mcplexer/internal/routing"
)

// TestWorkspaceAncestors_RefreshOnVersionBump drives the core invalidation
// branch in workspaceAncestors(): when the routing engine's WorkspaceVersion
// advances past the session's last-seen value, the chain is re-resolved and
// lastWSVer is updated. Existing tests only call resolveChainForPath directly
// and never exercise the version-compare -> re-resolve -> update path.
func TestWorkspaceAncestors_RefreshOnVersionBump(t *testing.T) {
	st := &mockStore{
		workspaces: []mockWorkspace{
			{id: "ws-a", rootPath: "/code/proj"},
		},
	}
	eng := routing.NewEngine(st)

	sm := &sessionManager{store: st, engine: eng, transport: TransportSocket}
	// Bind: clientPath + initial chain + lastWSVer captured.
	if err := sm.create(t.Context(), ClientInfo{Name: "test"}, []Root{{URI: "file:///code/proj/src"}}, "", nil); err != nil {
		t.Fatalf("create: %v", err)
	}

	first := sm.workspaceAncestors(t.Context())
	if len(first) != 1 || first[0].ID != "ws-a" {
		t.Fatalf("initial chain = %+v, want [ws-a]", first)
	}
	startVer := sm.lastWSVer

	// Mutate the workspace set, then bump the engine version exactly as a
	// route/workspace mutation would (InvalidateAllRoutes increments wsVersion).
	st.workspaces = append(st.workspaces, mockWorkspace{id: "ws-root", rootPath: "/"})
	eng.InvalidateAllRoutes()

	refreshed := sm.workspaceAncestors(t.Context())
	if len(refreshed) != 2 {
		t.Fatalf("refreshed chain = %+v, want 2 entries after version bump", refreshed)
	}
	if refreshed[0].ID != "ws-a" || refreshed[1].ID != "ws-root" {
		t.Fatalf("refreshed chain order = %+v, want [ws-a ws-root]", refreshed)
	}
	if sm.lastWSVer <= startVer {
		t.Errorf("lastWSVer did not advance: was %d, now %d", startVer, sm.lastWSVer)
	}

	// No version change -> no further re-resolve: removing the engine-visible
	// store change should NOT be reflected because the version is unchanged.
	st.workspaces = st.workspaces[:1]
	stable := sm.workspaceAncestors(t.Context())
	if len(stable) != 2 {
		t.Errorf("chain changed without a version bump: %+v", stable)
	}
}

// TestCreate_FiresRepoBrainDiscoveryOnce asserts discoverRepoBrain is invoked
// exactly once at session bind, with the resolved client root + most-specific
// workspace id, and is NOT re-fired by a later workspaceAncestors() refresh.
func TestCreate_FiresRepoBrainDiscoveryOnce(t *testing.T) {
	st := &mockStore{
		workspaces: []mockWorkspace{
			{id: "ws-a", rootPath: "/code/proj"},
		},
	}
	eng := routing.NewEngine(st)
	sm := &sessionManager{store: st, engine: eng, transport: TransportSocket}

	var calls int
	var gotRoot, gotWS string
	sm.SetRepoBrainDiscovery(func(_ context.Context, clientRoot, workspaceID string) {
		calls++
		gotRoot = clientRoot
		gotWS = workspaceID
	})

	if err := sm.create(t.Context(), ClientInfo{Name: "test"}, []Root{{URI: "file:///code/proj/src"}}, "", nil); err != nil {
		t.Fatalf("create: %v", err)
	}

	if calls != 1 {
		t.Fatalf("discoverRepoBrain called %d times on create, want 1", calls)
	}
	if gotRoot != "/code/proj/src" {
		t.Errorf("clientRoot = %q, want %q", gotRoot, "/code/proj/src")
	}
	if gotWS != "ws-a" {
		t.Errorf("workspaceID = %q, want %q", gotWS, "ws-a")
	}

	// A refresh on version bump must NOT re-fire discovery.
	eng.InvalidateAllRoutes()
	sm.workspaceAncestors(t.Context())
	if calls != 1 {
		t.Errorf("discoverRepoBrain re-fired on refresh: now %d calls, want 1", calls)
	}
}

func TestCreate_RebindsAfterRepoBrainDiscoveryMaterializesChild(t *testing.T) {
	st := &mockStore{
		workspaces: []mockWorkspace{
			{id: "parent", rootPath: "/code/workspace1"},
		},
	}
	eng := routing.NewEngine(st)
	sm := &sessionManager{store: st, engine: eng, transport: TransportSocket}

	var callbackWorkspace string
	sm.SetRepoBrainDiscovery(func(_ context.Context, _ string, workspaceID string) {
		callbackWorkspace = workspaceID
		st.workspaces = append(st.workspaces, mockWorkspace{
			id:       "folder2",
			rootPath: "/code/workspace1/folder1/folder2",
			parentID: "parent",
		})
		eng.InvalidateAllRoutes()
	})

	if err := sm.create(t.Context(), ClientInfo{Name: "test"}, []Root{{URI: "file:///code/workspace1/folder1/folder2"}}, "", nil); err != nil {
		t.Fatalf("create: %v", err)
	}
	if callbackWorkspace != "parent" {
		t.Fatalf("discovery callback workspace = %q, want initially resolved parent", callbackWorkspace)
	}
	if got := sm.workspaceID(); got != "folder2" {
		t.Fatalf("workspaceID after discovery = %q, want folder2", got)
	}
	if sm.session == nil || sm.session.WorkspaceID == nil || *sm.session.WorkspaceID != "folder2" {
		t.Fatalf("persisted session workspace = %+v, want folder2", sm.session)
	}
}

// TestWorkspaceAncestors_ConcurrentRefreshNoRace drives many goroutines through
// workspaceAncestors()/isAdminTrusted() while the engine version keeps changing,
// reproducing the shared-sessionManager data race (server.run spawns one
// dispatch goroutine per JSON-RPC line; the worker gateway shares one Server
// across concurrent runs). Run with -race to detect torn wsChain / transient
// adminTrusted reads. Without the mutex this fails under the race detector.
func TestWorkspaceAncestors_ConcurrentRefreshNoRace(t *testing.T) {
	st := &mockStore{
		workspaces: []mockWorkspace{
			{id: "ws-a", rootPath: "/code/proj", tags: rawTrusted()},
			{id: "ws-root", rootPath: "/"},
		},
	}
	eng := routing.NewEngine(st)
	sm := &sessionManager{store: st, engine: eng, transport: TransportSocket}
	if err := sm.create(t.Context(), ClientInfo{Name: "test"}, []Root{{URI: "file:///code/proj/src"}}, "", nil); err != nil {
		t.Fatalf("create: %v", err)
	}

	var stop atomic.Bool
	var bumper sync.WaitGroup
	var readersWG sync.WaitGroup

	// Bumper: invalidates routes so the refresh branch in workspaceAncestors
	// fires, interleaving re-resolution with concurrent reads. Bounded to a
	// few hundred bumps (ample interleaving without flooding the refresh log)
	// and stops early if the readers finish first.
	bumper.Add(1)
	go func() {
		defer bumper.Done()
		for i := 0; i < 300 && !stop.Load(); i++ {
			eng.InvalidateAllRoutes()
		}
	}()

	// Readers: hammer the lock-guarded accessors concurrently. Each runs a
	// fixed iteration count and exits.
	const readers = 8
	for i := 0; i < readers; i++ {
		readersWG.Add(1)
		go func() {
			defer readersWG.Done()
			for j := 0; j < 2000; j++ {
				_ = sm.workspaceAncestors(t.Context())
				_ = sm.isAdminTrusted()
				_ = sm.workspaceID()
				_ = sm.workspaceRoots()
				_ = sm.clientRoot()
			}
		}()
	}

	readersWG.Wait()
	stop.Store(true)
	bumper.Wait()

	// The admin-trusted workspace is always in the chain, so trust must hold.
	if !sm.isAdminTrusted() {
		t.Error("expected adminTrusted to remain true (ws-a carries the tag)")
	}
}

func rawTrusted() []byte {
	return []byte(`["admin-trusted"]`)
}
