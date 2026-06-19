package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/downstream"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// newHealthTestDeps spins a sqlite store + a manager and seeds a
// downstream server row. Returns everything wired so each test case
// stays declarative.
func newHealthTestDeps(t *testing.T, serverID string) (*downstreamHealthHandler, *downstream.Manager) {
	t.Helper()
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if serverID != "" {
		srv := &store.DownstreamServer{
			ID:        serverID,
			Name:      serverID,
			Transport: "stdio",
			Command:   "/bin/true",
			Args:      json.RawMessage(`[]`),
			Source:    "test",
		}
		if err := db.CreateDownstreamServer(context.Background(), srv); err != nil {
			t.Fatalf("seed server: %v", err)
		}
	}
	mgr := downstream.NewManager(db, nil)
	h := &downstreamHealthHandler{store: db, manager: mgr}
	return h, mgr
}

func runHealth(t *testing.T, h *downstreamHealthHandler, id string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/downstreams/"+id+"/health", nil)
	req.SetPathValue("id", id)
	rr := httptest.NewRecorder()
	h.get(rr, req)
	return rr
}

// TestDownstreamHealth_FreshServer returns a zero snapshot for a
// server that's been registered but never seen a failure or success.
// The dashboard relies on this to render a "healthy" tile on day one.
func TestDownstreamHealth_FreshServer(t *testing.T) {
	const id = "srv-fresh"
	h, _ := newHealthTestDeps(t, id)
	rr := runHealth(t, h, id)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var snap downstream.ServerHealth
	if err := json.NewDecoder(rr.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.ServerID != id {
		t.Errorf("ServerID = %q, want %q", snap.ServerID, id)
	}
	if snap.ConsecutiveFailures != 0 {
		t.Errorf("ConsecutiveFailures = %d, want 0", snap.ConsecutiveFailures)
	}
	if snap.AutoReloads24h != 0 {
		t.Errorf("AutoReloads24h = %d, want 0", snap.AutoReloads24h)
	}
}

// TestDownstreamHealth_AfterFailures reflects the in-memory tracker
// after RecordFailure has been called — the snapshot endpoint is the
// dashboard's window into the stuck-detector state.
func TestDownstreamHealth_AfterFailures(t *testing.T) {
	const id = "srv-flaky"
	h, mgr := newHealthTestDeps(t, id)
	now := time.Now()
	mgr.Health().RecordFailure(id, "timeout", now)
	mgr.Health().RecordFailure(id, "timeout", now.Add(time.Second))

	rr := runHealth(t, h, id)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var snap downstream.ServerHealth
	if err := json.NewDecoder(rr.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.ConsecutiveFailures != 2 {
		t.Errorf("ConsecutiveFailures = %d, want 2", snap.ConsecutiveFailures)
	}
	if snap.LastFailureReason != "timeout" {
		t.Errorf("LastFailureReason = %q, want timeout", snap.LastFailureReason)
	}
}

// TestDownstreamHealth_AfterReload exposes the auto-reload count so
// the dashboard tile "N downstreams auto-recovered today" has a data
// source.
func TestDownstreamHealth_AfterReload(t *testing.T) {
	const id = "srv-recovered"
	h, mgr := newHealthTestDeps(t, id)
	mgr.Health().MarkReload(id, time.Now())

	rr := runHealth(t, h, id)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var snap downstream.ServerHealth
	_ = json.NewDecoder(rr.Body).Decode(&snap)
	if snap.AutoReloads24h != 1 {
		t.Errorf("AutoReloads24h = %d, want 1", snap.AutoReloads24h)
	}
	if snap.LastAutoReloadAt.IsZero() {
		t.Errorf("LastAutoReloadAt should be set")
	}
}

// TestDownstreamHealth_UnknownServer returns 404 when the store has
// no record. Stops the dashboard from silently showing health for a
// deleted server whose tracker entry still lingers in memory.
func TestDownstreamHealth_UnknownServer(t *testing.T) {
	h, _ := newHealthTestDeps(t, "") // no seed
	rr := runHealth(t, h, "ghost")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

// TestDownstreamHealth_MissingID returns 400 — pre-flight guard for
// router misconfiguration.
func TestDownstreamHealth_MissingID(t *testing.T) {
	h, _ := newHealthTestDeps(t, "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/downstreams//health", nil)
	rr := httptest.NewRecorder()
	h.get(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}
