package downstream

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// hangingInstance is a fake downstream that blocks Call until ctx is
// done, then returns ctx.Err(). Used to exercise the per-call deadline
// wired into Manager.Call.
type hangingInstance struct{}

func (h *hangingInstance) start(_ context.Context) error { return nil }
func (h *hangingInstance) stop()                         {}
func (h *hangingInstance) ListTools(ctx context.Context) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (h *hangingInstance) Call(ctx context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (h *hangingInstance) getState() InstanceState { return StateReady }
func (h *hangingInstance) waitRestartDone() <-chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}

// newTimeoutTestManager mints a Manager backed by a fresh sqlite DB and
// pre-registers a stdio downstream the test can inject a hanging
// instance for.
func newTimeoutTestManager(t *testing.T, serverID string, callTimeoutSec int) *Manager {
	t.Helper()
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
		CallTimeoutSec: callTimeoutSec,
	}
	if err := db.CreateDownstreamServer(context.Background(), srv); err != nil {
		t.Fatalf("CreateDownstreamServer: %v", err)
	}
	return NewManager(db, nil)
}

// TestManagerCall_PerCallTimeout asserts that Manager.Call cancels a
// wedged downstream after the per-server call_timeout_sec and surfaces
// ErrCallTimeout to the caller. This is the load-bearing fix for the
// 2026-05-27 incident — a wedged HTTP/2 stream on the Linear MCP
// downstream blocked client calls indefinitely.
func TestManagerCall_PerCallTimeout(t *testing.T) {
	const serverID = "srv-timeout"
	// 1-second timeout — long enough to confirm the deadline is honored,
	// short enough to keep the test snappy.
	m := newTimeoutTestManager(t, serverID, 1)

	// Inject a hanging instance directly into the manager so the test
	// doesn't depend on launching a real subprocess.
	m.mu.Lock()
	m.instances[InstanceKey{ServerID: serverID}] = &hangingInstance{}
	m.mu.Unlock()

	start := time.Now()
	_, err := m.Call(context.Background(), serverID, "", "noop", json.RawMessage(`{}`))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Manager.Call returned nil error; expected ErrCallTimeout")
	}
	if !errors.Is(err, ErrCallTimeout) {
		t.Errorf("Manager.Call err = %v; want errors.Is(err, ErrCallTimeout)", err)
	}
	if elapsed < 900*time.Millisecond {
		t.Errorf("Manager.Call returned too fast (%v) — deadline may not have fired", elapsed)
	}
	if elapsed > 3*time.Second {
		t.Errorf("Manager.Call returned too slowly (%v) — deadline not enforced", elapsed)
	}
}

// TestManagerCall_DefaultCallTimeout asserts that when the per-server
// call_timeout_sec is zero the gateway-wide DefaultCallTimeout applies.
// We shadow the default to a tiny value to keep the test fast.
func TestManagerCall_DefaultCallTimeout(t *testing.T) {
	prev := DefaultCallTimeout
	DefaultCallTimeout = 500 * time.Millisecond
	t.Cleanup(func() { DefaultCallTimeout = prev })

	const serverID = "srv-default"
	m := newTimeoutTestManager(t, serverID, 0) // zero -> falls back to DefaultCallTimeout

	m.mu.Lock()
	m.instances[InstanceKey{ServerID: serverID}] = &hangingInstance{}
	m.mu.Unlock()

	start := time.Now()
	_, err := m.Call(context.Background(), serverID, "", "noop", json.RawMessage(`{}`))
	elapsed := time.Since(start)

	if !errors.Is(err, ErrCallTimeout) {
		t.Errorf("Manager.Call err = %v; want errors.Is(err, ErrCallTimeout)", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("Manager.Call did not honor DefaultCallTimeout: elapsed=%v", elapsed)
	}
}

// TestCallTimeoutFor_DefaultsWhenUnset verifies the helper's fallback
// behaviour without booting a Manager.
func TestCallTimeoutFor_DefaultsWhenUnset(t *testing.T) {
	tests := []struct {
		name string
		in   *store.DownstreamServer
		want time.Duration
	}{
		{"nil server -> default", nil, DefaultCallTimeout},
		{"zero column -> default", &store.DownstreamServer{CallTimeoutSec: 0}, DefaultCallTimeout},
		{"negative -> default", &store.DownstreamServer{CallTimeoutSec: -5}, DefaultCallTimeout},
		{"positive -> seconds", &store.DownstreamServer{CallTimeoutSec: 7}, 7 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := callTimeoutFor(tt.in)
			if got != tt.want {
				t.Errorf("callTimeoutFor() = %v, want %v", got, tt.want)
			}
		})
	}
}
