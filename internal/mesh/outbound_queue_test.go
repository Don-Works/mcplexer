package mesh

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// fakeSender records every SendToPeer call. When fail is non-nil, the next
// (and every subsequent) call returns it; switching to nil mid-test lets
// us simulate "peer came back online". atomic counter so the test can
// observe drains without racing the goroutine.
type fakeSender struct {
	mu    sync.Mutex
	calls int64
	fail  error
}

func newFakeSender(initialErr error) *fakeSender {
	return &fakeSender{fail: initialErr}
}

func (s *fakeSender) SendToPeer(_ context.Context, _ string, _ *p2p.MeshEnvelope) error {
	atomic.AddInt64(&s.calls, 1)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.fail
}

func (s *fakeSender) setFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail = err
}

// newTestDB spins up a throwaway sqlite db with the full migration suite
// applied. Returned cleanup runs on t.Cleanup.
func newTestDB(t *testing.T) *sqlite.DB {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "queue-test.db")
	db, err := sqlite.New(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

// makeEnvelope builds a representative envelope for queue tests.
func makeEnvelope(id, peerID string) *p2p.MeshEnvelope {
	return &p2p.MeshEnvelope{
		ID:           id,
		SenderPeerID: "12D3KooWSenderTestPeerOfReasonableLength",
		Kind:         "event",
		Content:      "queued payload " + id,
		Recipient:    p2p.Recipient{Kind: "peer", Value: peerID},
		TS:           time.Now().UnixMilli(),
	}
}

// TestOfflineQueue_DispatchEnqueuesOnFailure simulates the canonical case:
// Alice's Send hits a libp2p SendToPeer that fails (peer offline) and the
// envelope lands in the mesh_outbound_queue.
func TestOfflineQueue_DispatchEnqueuesOnFailure(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	sender := newFakeSender(errors.New("dial timeout"))
	q := NewOutboundQueue(db, sender, nil, nil, nil)

	env := makeEnvelope("01H0000000000000000000ENQ1", "12D3KooWBobOfflineTestPeerLongEnough")
	if err := q.Enqueue(ctx, env.Recipient.Value, "", env, errors.New("dial timeout")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	rows, err := db.ListPendingMeshOutbound(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ListPendingMeshOutbound: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 queued row, got %d", len(rows))
	}
	if rows[0].MessageID != env.ID {
		t.Fatalf("message_id mismatch: got %q want %q", rows[0].MessageID, env.ID)
	}
	if rows[0].LastError == "" {
		t.Fatal("expected last_error to be populated from dispatchErr")
	}
	if rows[0].Attempts < 1 {
		t.Fatalf("expected attempts >= 1, got %d", rows[0].Attempts)
	}
}

// TestOfflineQueue_DrainOnReconnect enqueues a row while the sender is
// failing, then "brings the peer online" (sender returns nil), kicks
// DrainForPeer, and asserts delivered_at is set.
func TestOfflineQueue_DrainOnReconnect(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	sender := newFakeSender(errors.New("dial timeout"))
	q := NewOutboundQueue(db, sender, nil, nil, nil)

	peerID := "12D3KooWBobOfflineTestPeerLongEnough"
	env := makeEnvelope("01H0000000000000000000DRN1", peerID)
	if err := q.Enqueue(ctx, peerID, "", env, errors.New("dial timeout")); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	// Now simulate "Bob comes back online" and warp time past
	// next_attempt_at so DrainForPeer doesn't have to wait the backoff.
	sender.setFailure(nil)
	q.clk = func() time.Time { return time.Now().UTC().Add(time.Hour) }
	q.DrainForPeer(ctx, peerID)

	rows, err := db.ListPendingMeshOutbound(ctx, q.clk(), 10)
	if err != nil {
		t.Fatalf("ListPendingMeshOutbound: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 pending rows after drain, got %d (first id=%s)",
			len(rows), rows[0].MessageID)
	}
	if atomic.LoadInt64(&sender.calls) < 1 {
		t.Fatal("expected at least one SendToPeer call during drain")
	}
}

// TestOfflineQueue_Expiry enqueues a row with an already-past expires_at
// and asserts the drain treats it as a no-op (no delivery attempt fires)
// and the prune sweep removes it.
func TestOfflineQueue_Expiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	sender := newFakeSender(nil)
	q := NewOutboundQueue(db, sender, nil, nil, nil)

	peerID := "12D3KooWExpiredTestPeerOfReasonableLen"
	envID := "01H0000000000000000000EXP1"

	// Write a row directly via the store so we can backdate expires_at.
	past := time.Now().UTC().Add(-1 * time.Hour)
	wire, _ := encodeForTest(makeEnvelope(envID, peerID))
	row := &store.MeshOutbound{
		MessageID:     envID,
		TargetPeerID:  peerID,
		Envelope:      wire,
		Attempts:      0,
		EnqueuedAt:    past.Add(-24 * time.Hour),
		NextAttemptAt: past.Add(-24 * time.Hour),
		ExpiresAt:     past, // already expired
	}
	if err := db.EnqueueMeshOutbound(ctx, row); err != nil {
		t.Fatalf("seed enqueue: %v", err)
	}

	// DrainForPeer should NOT pick up expired rows.
	q.DrainForPeer(ctx, peerID)
	if got := atomic.LoadInt64(&sender.calls); got != 0 {
		t.Fatalf("expected 0 SendToPeer calls on expired row, got %d", got)
	}

	// Prune sweep should remove it.
	q.runPrune(ctx)
	all, err := db.ListPendingMeshOutbound(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ListPendingMeshOutbound: %v", err)
	}
	expired, err := db.ListExpiredMeshOutbound(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ListExpiredMeshOutbound: %v", err)
	}
	if len(all)+len(expired) != 0 {
		t.Fatalf("expected row pruned, still found %d pending + %d expired",
			len(all), len(expired))
	}
}

// TestOfflineQueue_DedupByMessageID asserts that a second Enqueue with the
// same message_id is a no-op — the original row stands.
func TestOfflineQueue_DedupByMessageID(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := newTestDB(t)
	sender := newFakeSender(errors.New("dial timeout"))
	q := NewOutboundQueue(db, sender, nil, nil, nil)

	peerID := "12D3KooWDedupTestPeerOfReasonableLength"
	env := makeEnvelope("01H0000000000000000000DUP1", peerID)
	if err := q.Enqueue(ctx, peerID, "", env, errors.New("first error")); err != nil {
		t.Fatalf("first Enqueue: %v", err)
	}
	if err := q.Enqueue(ctx, peerID, "", env, errors.New("second error")); err != nil {
		t.Fatalf("second Enqueue should be a no-op, got: %v", err)
	}

	rows, err := db.ListPendingMeshOutbound(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ListPendingMeshOutbound: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row after dedup, got %d", len(rows))
	}
	// The first error must be the one that stuck.
	if rows[0].LastError != "first error" {
		t.Fatalf("dedup overwrote the row; last_error = %q", rows[0].LastError)
	}
}

// encodeForTest mirrors what Enqueue does for the wire-format envelope
// blob, so we can seed pre-cooked rows without going through Enqueue.
func encodeForTest(env *p2p.MeshEnvelope) ([]byte, error) {
	return json.Marshal(queuedEnvelope{Envelope: *env})
}
