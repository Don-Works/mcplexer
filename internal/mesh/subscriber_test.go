package mesh_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestSubscriberFiresOnSend verifies the subscription primitive is wired
// into the local Send path. A subscriber registered before Send receives
// the inserted MeshMessage; after the returned unsubscribe runs, no
// further calls arrive.
func TestSubscriberFiresOnSend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "sub.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	mgr := mesh.NewManager(db)

	var (
		mu       sync.Mutex
		received []*store.MeshMessage
	)
	unsub := mgr.Subscribe(func(_ context.Context, msg *store.MeshMessage) {
		mu.Lock()
		defer mu.Unlock()
		received = append(received, msg)
	})

	meta := mesh.SessionMeta{
		SessionID:    "test-session",
		WorkspaceIDs: []string{"global"},
		ClientType:   "test",
	}
	_, err = mgr.Send(ctx, meta, mesh.SendRequest{
		Kind:    "event",
		Content: "hello triggers",
		Tags:    "test,subscribe",
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	mu.Lock()
	if len(received) != 1 {
		t.Fatalf("subscriber not called: got %d messages", len(received))
	}
	if received[0].Content != "hello triggers" {
		t.Fatalf("wrong message: %+v", received[0])
	}
	mu.Unlock()

	// Unsubscribe — a second Send should NOT reach the callback.
	unsub()
	_, _ = mgr.Send(ctx, meta, mesh.SendRequest{Kind: "event", Content: "after unsub"})
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("subscriber fired after unsubscribe: got %d", len(received))
	}
}

// TestSubscriberPanicIsolation confirms a panicking subscriber does NOT
// abort Send or prevent later subscribers from receiving the message.
func TestSubscriberPanicIsolation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, err := sqlite.New(ctx, filepath.Join(t.TempDir(), "panic.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	mgr := mesh.NewManager(db)

	var goodCalls atomic.Int32
	mgr.Subscribe(func(_ context.Context, _ *store.MeshMessage) {
		panic("intentional")
	})
	mgr.Subscribe(func(_ context.Context, _ *store.MeshMessage) {
		goodCalls.Add(1)
	})
	_, err = mgr.Send(ctx, mesh.SessionMeta{
		SessionID:    "panic-session",
		WorkspaceIDs: []string{"global"},
		ClientType:   "test",
	}, mesh.SendRequest{Kind: "event", Content: "x"})
	if err != nil {
		t.Fatalf("Send returned error despite panic: %v", err)
	}
	if goodCalls.Load() != 1 {
		t.Fatalf("non-panicking subscriber not invoked: count=%d", goodCalls.Load())
	}
}

// TestSubscribeNilSafety covers the (likely) bad-input paths that the
// Manager exposes to wiring code: nil func, nil manager.
func TestSubscribeNilSafety(t *testing.T) {
	t.Parallel()
	var nilMgr *mesh.Manager
	if unsub := nilMgr.Subscribe(func(context.Context, *store.MeshMessage) {}); unsub == nil {
		t.Fatal("nil manager Subscribe must return a no-op unsubscribe func")
	} else {
		unsub() // must not panic
	}
	mgr := mesh.NewManager(nil)
	if unsub := mgr.Subscribe(nil); unsub == nil {
		t.Fatal("nil fn Subscribe must return a no-op unsubscribe func")
	} else {
		unsub()
	}
}
