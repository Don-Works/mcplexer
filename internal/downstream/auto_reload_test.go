package downstream

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// trackedHanging returns ctx.Err() once ctx fires and records every
// time stop() was called, which is the signal the manager evicted it
// during an auto-reload.
type trackedHanging struct {
	stops atomic.Int32
}

func (h *trackedHanging) start(_ context.Context) error { return nil }
func (h *trackedHanging) stop()                         { h.stops.Add(1) }
func (h *trackedHanging) ListTools(ctx context.Context) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (h *trackedHanging) Call(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (h *trackedHanging) getState() InstanceState { return StateReady }
func (h *trackedHanging) waitRestartDone() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// TestManager_AutoReload_OnThreshold drives the full stuck-detector
// path: three consecutive per-call timeouts trip the threshold,
// performAutoReload evicts the wedged instance and the registered
// hook fires exactly once.
func TestManager_AutoReload_OnThreshold(t *testing.T) {
	// Shrink knobs so the test finishes in ~3s.
	prevCount := StuckThresholdCount
	prevWindow := StuckThresholdWindow
	prevMin := MinReloadBackoff
	t.Cleanup(func() {
		StuckThresholdCount = prevCount
		StuckThresholdWindow = prevWindow
		MinReloadBackoff = prevMin
	})
	StuckThresholdCount = 3
	StuckThresholdWindow = 60 * time.Second
	MinReloadBackoff = 60 * time.Second // we only fire once in this test

	const serverID = "srv-stuck"
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	srv := &store.DownstreamServer{
		ID:             serverID,
		Name:           serverID,
		Transport:      "stdio",
		Command:        "/bin/true",
		Args:           json.RawMessage(`[]`),
		ToolNamespace:  serverID,
		Discovery:      "static",
		Source:         "test",
		CallTimeoutSec: 1,
	}
	if err := db.CreateDownstreamServer(context.Background(), srv); err != nil {
		t.Fatalf("CreateDownstreamServer: %v", err)
	}
	m := NewManager(db, nil)

	var hookFired atomic.Int32
	var lastSnap atomic.Value
	m.SetAutoReloadHook(func(_ string, snap ServerHealth) {
		hookFired.Add(1)
		lastSnap.Store(snap)
	})

	// Pre-seed the wedged instance so getOrStart returns it without a
	// real subprocess. After eviction, the map is empty and a fresh
	// getOrStart would try to spawn /bin/true — which is fine, /bin/true
	// is a valid binary even if it isn't an MCP server. We don't make
	// any further calls after eviction.
	wedge := &trackedHanging{}
	m.mu.Lock()
	m.instances[InstanceKey{ServerID: serverID}] = wedge
	m.mu.Unlock()

	// The stuck-detector only auto-reloads servers that have served at
	// least one success (a never-healthy server is mis-configured, not
	// wedged). Seed that precondition before driving the timeouts.
	m.Health().RecordSuccess(serverID, time.Now())

	for i := 0; i < 3; i++ {
		_, err := m.Call(context.Background(), serverID, "", "noop", json.RawMessage(`{}`))
		if !errors.Is(err, ErrCallTimeout) {
			t.Fatalf("call %d: want ErrCallTimeout, got %v", i, err)
		}
		// Re-seed after the first timeout in case the previous goroutine
		// already evicted it. The 3rd call is the one that should trip.
		m.mu.Lock()
		if _, ok := m.instances[InstanceKey{ServerID: serverID}]; !ok {
			m.instances[InstanceKey{ServerID: serverID}] = wedge
		}
		m.mu.Unlock()
	}

	// Auto-reload runs in a goroutine. Poll for completion up to 2s.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if hookFired.Load() == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if hookFired.Load() != 1 {
		t.Fatalf("auto-reload hook fired %d times, want 1", hookFired.Load())
	}
	if wedge.stops.Load() == 0 {
		t.Errorf("wedged instance was not stop()'d on auto-reload")
	}
	snap, _ := lastSnap.Load().(ServerHealth)
	if snap.AutoReloads24h != 1 {
		t.Errorf("snap.AutoReloads24h = %d, want 1", snap.AutoReloads24h)
	}
	if snap.LastFailureReason == "" {
		t.Errorf("snap.LastFailureReason should be set; got empty")
	}
}

// TestManager_RecordCallFailure_NoTrip_WhenHookNil verifies the path
// is nil-safe when the host hasn't installed an auto-reload hook.
func TestManager_RecordCallFailure_NoTrip_WhenHookNil(t *testing.T) {
	prevCount := StuckThresholdCount
	t.Cleanup(func() { StuckThresholdCount = prevCount })
	StuckThresholdCount = 2

	const serverID = "srv-nohook"
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	srv := &store.DownstreamServer{
		ID:        serverID,
		Name:      serverID,
		Transport: "stdio",
		Command:   "/bin/true",
		Args:      json.RawMessage(`[]`),
	}
	_ = db.CreateDownstreamServer(context.Background(), srv)
	m := NewManager(db, nil)

	// Seed a success so the never-healthy gate doesn't suppress the reload
	// (the path under test is "wedged once-healthy server, nil hook").
	m.Health().RecordSuccess(serverID, time.Now())

	// No SetAutoReloadHook — performAutoReload must not panic.
	m.recordCallFailure(serverID, "first")
	m.recordCallFailure(serverID, "second")
	// Allow the eviction goroutine to settle.
	time.Sleep(50 * time.Millisecond)

	snap := m.Health().Snapshot(serverID, time.Now())
	if snap.AutoReloads24h != 1 {
		t.Errorf("AutoReloads24h = %d, want 1 (path should fire even with nil hook)", snap.AutoReloads24h)
	}
}
