package downstream

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// fakeInternal implements InternalBackend for tests. ListTools returns
// `result` after waiting `delay` (or until ctx is done — whichever first).
type fakeInternal struct {
	delay  time.Duration
	result json.RawMessage
}

func (f *fakeInternal) ListTools(ctx context.Context) (json.RawMessage, error) {
	select {
	case <-time.After(f.delay):
		return f.result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (f *fakeInternal) Call(_ context.Context, _ string, _ json.RawMessage) (json.RawMessage, error) {
	return nil, nil
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewManager(db, nil)
}

func registerInternalServer(t *testing.T, m *Manager, id string, b InternalBackend) {
	t.Helper()
	srv := &store.DownstreamServer{
		ID:            id,
		Name:          id,
		Transport:     "internal",
		ToolNamespace: id,
		Discovery:     "static",
		Source:        "test",
	}
	if err := m.store.CreateDownstreamServer(context.Background(), srv); err != nil {
		t.Fatalf("CreateDownstreamServer(%s): %v", id, err)
	}
	m.RegisterInternal(id, b)
}

// TestListToolsForServers_PerServerTimeout asserts that one slow server cannot
// stall the aggregation past the per-server timeout — the call returns within
// the timeout window with the fast server's data, and the slow server is
// dropped from the result.
func TestListToolsForServers_PerServerTimeout(t *testing.T) {
	// Shrink the production timeout for this test only so we don't burn 15s
	// per run. The package-level var is intentionally mutable to support
	// exactly this pattern.
	prev := PerServerListToolsTimeout
	PerServerListToolsTimeout = 1 * time.Second
	t.Cleanup(func() { PerServerListToolsTimeout = prev })

	m := newTestManager(t)
	fastResult := json.RawMessage(`{"tools":[{"name":"fast"}]}`)
	registerInternalServer(t, m, "fast", &fakeInternal{delay: 10 * time.Millisecond, result: fastResult})
	registerInternalServer(t, m, "slow", &fakeInternal{delay: PerServerListToolsTimeout + 10*time.Second, result: json.RawMessage(`{"tools":[{"name":"slow"}]}`)})

	start := time.Now()
	got, err := m.ListToolsForServers(context.Background(), []string{"fast", "slow"})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("ListToolsForServers: %v", err)
	}

	maxAcceptable := PerServerListToolsTimeout + 2*time.Second
	if elapsed > maxAcceptable {
		t.Errorf("ListToolsForServers blocked past per-server timeout: %v > %v", elapsed, maxAcceptable)
	}

	if _, ok := got["fast"]; !ok {
		t.Errorf("expected 'fast' in result; got keys %v", keys(got))
	}
	if _, ok := got["slow"]; ok {
		t.Errorf("expected 'slow' to be dropped (timed out); got keys %v", keys(got))
	}
}

func keys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
