// memory_offers_handler_test.go — coverage for the offer accept /
// decline REST surface, with a focus on the realtime contract: every
// state transition must fire a memory.Event on the service's Notify
// hook so the dashboard's /memory page lights up live.
//
// Pre-fix the handlers stamped the database row but never told the
// memory service the offer had moved, so the dashboard's "Incoming
// offers" tile + activity stream stayed silent until manual reload.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
)

// seedOffer inserts a memory offer row directly via the store and
// returns its id. Mirrors how the p2p memory-share recorder writes
// incoming offers, sidestepping the libp2p stack so the test stays
// hermetic.
func seedOffer(t *testing.T, st store.Store, peerID, remoteID string) string {
	t.Helper()
	o := &store.MemoryOffer{
		PeerID:   peerID,
		PeerName: "TestPeer",
		RemoteID: remoteID,
		Name:     "shared-fact",
		Kind:     store.MemoryKindNote,
	}
	if err := st.UpsertMemoryOffer(context.Background(), o); err != nil {
		t.Fatalf("seed offer: %v", err)
	}
	if o.ID == "" {
		t.Fatal("seed offer: id not stamped")
	}
	return o.ID
}

// captureNotify swaps in a sync-safe Notify hook and returns a
// snapshot fn. We assert from the test goroutine because the handler
// calls Notify inline.
func captureNotify(svc *memory.Service) (snapshot func() []memory.Event) {
	var mu sync.Mutex
	var evs []memory.Event
	svc.Notify = func(_ context.Context, ev memory.Event) {
		mu.Lock()
		defer mu.Unlock()
		evs = append(evs, ev)
	}
	return func() []memory.Event {
		mu.Lock()
		defer mu.Unlock()
		out := make([]memory.Event, len(evs))
		copy(out, evs)
		return out
	}
}

func TestMemoryOfferAcceptFiresNotify(t *testing.T) {
	srv, db, svc := newMemoryTestServer(t)
	// We need a real local memory id to satisfy the accept handler's
	// "local_memory_id is required" gate.
	localID := seedMemory(t, svc, "imported", "from-peer", store.MemoryKindNote, nil)
	// Drain notify events from the seedMemory write so the test only
	// observes the accept event.
	snap := captureNotify(svc)

	offerID := seedOffer(t, db, "peer-X", "remote-1")

	body, _ := json.Marshal(map[string]string{"local_memory_id": localID})
	resp, err := http.Post(
		srv.URL+"/api/v1/memory/offers/"+offerID+"/accept",
		"application/json", bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d, want 204", resp.StatusCode)
	}

	// Give the inline notify a beat — it's synchronous today, the
	// sleep guards against future async-ification.
	deadline := time.Now().Add(500 * time.Millisecond)
	var got []memory.Event
	for time.Now().Before(deadline) {
		got = snap()
		if len(got) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 notify event, got %d (%+v)", len(got), got)
	}
	if got[0].Kind != "offer_accepted" {
		t.Errorf("kind: want offer_accepted, got %q", got[0].Kind)
	}
	if got[0].OfferID != offerID {
		t.Errorf("offer_id: want %q, got %q", offerID, got[0].OfferID)
	}
	if got[0].MemoryID != localID {
		t.Errorf("memory_id: want %q, got %q", localID, got[0].MemoryID)
	}
}

func TestMemoryOfferDeclineFiresNotify(t *testing.T) {
	srv, db, svc := newMemoryTestServer(t)
	snap := captureNotify(svc)
	offerID := seedOffer(t, db, "peer-Y", "remote-2")

	resp, err := http.Post(
		srv.URL+"/api/v1/memory/offers/"+offerID+"/decline",
		"application/json", bytes.NewReader([]byte("{}")),
	)
	if err != nil {
		t.Fatalf("decline: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d, want 204", resp.StatusCode)
	}

	got := snap()
	if len(got) != 1 || got[0].Kind != "offer_declined" {
		t.Fatalf("expected offer_declined event, got %+v", got)
	}
	if got[0].OfferID != offerID {
		t.Errorf("offer_id: want %q, got %q", offerID, got[0].OfferID)
	}
}
