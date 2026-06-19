package mesh

// Regression coverage for the two confirmed mesh bugs plus the load-bearing
// Send/Receive/dispatchP2P/cursor/PendingCount paths that previously had no
// unit tests (only TestAgentDisplayName lived in mesh_test.go).
//
//   - Bug 1 (p2p_bridge.go dispatch guard): a default-audience mesh__send
//     must broadcast to paired peers. Send resolved an empty audience to a
//     LOCAL "*" but never wrote it back to req.Audience, so dispatchP2P saw
//     "" and short-circuited — local insert succeeded, wire broadcast was
//     silently skipped. Fixed by writing the resolved audience back.
//   - Bug 2 (mesh.go Receive cursor): filter=new advanced the cursor to the
//     max id among the priority-ordered, LIMIT-truncated batch, so a lower-
//     priority message with a smaller id that got truncated fell <= cursor
//     and was never re-fetched — silent loss on bursty streams. Fixed by
//     delivering the OLDEST batch by id and advancing the cursor to it.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/p2p"
)

// fakeTransport implements the unexported p2pTransport interface so tests can
// observe exactly which dispatch path Send took (broadcast vs targeted) and
// inject failures. All methods are concurrency-safe because dispatchP2P fans
// broadcasts onto a detached goroutine.
type fakeTransport struct {
	mu             sync.Mutex
	broadcasts     []*p2p.MeshEnvelope
	targeted       []targetedSend
	broadcastErr   error
	sendToPeerErr  error
	broadcastPeers int
	ch             chan p2p.MeshEnvelope
}

type targetedSend struct {
	peerID string
	env    *p2p.MeshEnvelope
}

func newFakeTransport() *fakeTransport {
	return &fakeTransport{ch: make(chan p2p.MeshEnvelope, 1)}
}

func (f *fakeTransport) SendToPeer(_ context.Context, peerID string, env *p2p.MeshEnvelope) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.targeted = append(f.targeted, targetedSend{peerID: peerID, env: env})
	return f.sendToPeerErr
}

func (f *fakeTransport) SendBroadcast(_ context.Context, env *p2p.MeshEnvelope) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.broadcasts = append(f.broadcasts, env)
	return f.broadcastPeers, f.broadcastErr
}

func (f *fakeTransport) Subscribe() <-chan p2p.MeshEnvelope { return f.ch }

func (f *fakeTransport) broadcastCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.broadcasts)
}

func (f *fakeTransport) targetedSends() []targetedSend {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]targetedSend, len(f.targeted))
	copy(out, f.targeted)
	return out
}

// waitForBroadcast polls until at least one broadcast has been recorded or the
// deadline passes — the broadcast dispatch is fire-and-forget on a goroutine.
func waitForBroadcast(t *testing.T, f *fakeTransport) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if f.broadcastCount() > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("expected a libp2p broadcast but none was recorded within 2s")
}

// TestDispatchP2P_Routing is the headline regression for Bug 1: an
// empty-audience send (the common mesh__send-with-no-audience path) MUST hit
// SendBroadcast, and an explicit to_peer MUST route via SendToPeer.
func TestDispatchP2P_Routing(t *testing.T) {
	t.Parallel()
	const selfPeer = "12D3KooWSelfTestPeerOfReasonableLengthAAA"
	const targetPeer = "12D3KooWTargetTestPeerOfReasonableLenBBB"

	cases := []struct {
		name          string
		req           SendRequest
		wantBroadcast bool
		wantTargeted  string // peer id, "" = none
	}{
		{
			name:          "empty audience broadcasts (bug 1)",
			req:           SendRequest{Kind: "finding", Content: "default broadcast"},
			wantBroadcast: true,
		},
		{
			name: "local_only suppresses default broadcast",
			req: SendRequest{
				Kind:      "finding",
				Content:   "worker output stays local",
				LocalOnly: true,
			},
		},
		{
			name:          "explicit star audience broadcasts",
			req:           SendRequest{Kind: "event", Content: "explicit star", Audience: "*"},
			wantBroadcast: true,
		},
		{
			name:         "targeted to_peer routes to SendToPeer",
			req:          SendRequest{Kind: "event", Content: "to bob", ToPeer: targetPeer},
			wantTargeted: targetPeer,
		},
		{
			name: "targeted session audience does not cross the wire",
			req:  SendRequest{Kind: "event", Content: "private", Audience: "some-session-id"},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := newTestDB(t)
			mgr := NewManager(db)
			ft := newFakeTransport()
			mgr.SetP2PTransport(ft, selfPeer)
			ctx := context.Background()

			meta := SessionMeta{
				SessionID:    "sender-session",
				WorkspaceIDs: []string{"global"},
				ClientType:   "test",
			}
			if _, err := mgr.Send(ctx, meta, tc.req); err != nil {
				t.Fatalf("Send: %v", err)
			}

			if tc.wantBroadcast {
				waitForBroadcast(t, ft)
			} else if ft.broadcastCount() != 0 {
				// Give the goroutine a beat to (incorrectly) fire.
				time.Sleep(50 * time.Millisecond)
				if ft.broadcastCount() != 0 {
					t.Fatalf("did not expect a broadcast, got %d", ft.broadcastCount())
				}
			}

			sends := ft.targetedSends()
			if tc.wantTargeted == "" {
				if len(sends) != 0 {
					t.Fatalf("expected no targeted send, got %d", len(sends))
				}
			} else {
				if len(sends) != 1 {
					t.Fatalf("expected exactly 1 targeted send, got %d", len(sends))
				}
				if sends[0].peerID != tc.wantTargeted {
					t.Fatalf("targeted peer = %q, want %q", sends[0].peerID, tc.wantTargeted)
				}
			}
		})
	}
}

// TestDispatchP2P_TargetedFailureEnqueues verifies the dispatch-failure →
// outbound-queue fallback wiring through Send: a SendToPeer failure with an
// OutboundQueue wired parks the envelope instead of erroring.
func TestDispatchP2P_TargetedFailureEnqueues(t *testing.T) {
	t.Parallel()
	const targetPeer = "12D3KooWBobOfflineTestPeerOfReasonableLengthCCC"
	db := newTestDB(t)
	mgr := NewManager(db)
	ft := newFakeTransport()
	ft.sendToPeerErr = errors.New("dial timeout")
	mgr.SetP2PTransport(ft, "12D3KooWSelfTestPeerOfReasonableLengthAAA")
	q := NewOutboundQueue(db, ft, nil, nil, nil)
	mgr.SetOutboundQueue(q)
	ctx := context.Background()

	meta := SessionMeta{SessionID: "s1", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	if _, err := mgr.Send(ctx, meta, SendRequest{
		Kind:    "event",
		Content: "to offline bob",
		ToPeer:  targetPeer,
	}); err != nil {
		t.Fatalf("Send should swallow the dispatch error via the queue, got: %v", err)
	}

	rows, err := db.ListPendingMeshOutbound(ctx, time.Now().UTC(), 10)
	if err != nil {
		t.Fatalf("ListPendingMeshOutbound: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 queued row after dispatch failure, got %d", len(rows))
	}
	if rows[0].TargetPeerID != targetPeer {
		t.Fatalf("queued row peer = %q, want %q", rows[0].TargetPeerID, targetPeer)
	}
}

// TestDispatchP2P_TargetedFailureNoQueueErrors locks the historical behaviour:
// with no OutboundQueue wired, a SendToPeer failure surfaces as an error.
func TestDispatchP2P_TargetedFailureNoQueueErrors(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	mgr := NewManager(db)
	ft := newFakeTransport()
	ft.sendToPeerErr = errors.New("dial timeout")
	mgr.SetP2PTransport(ft, "12D3KooWSelfTestPeerOfReasonableLengthAAA")
	ctx := context.Background()

	meta := SessionMeta{SessionID: "s1", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	_, err := mgr.Send(ctx, meta, SendRequest{
		Kind:    "event",
		Content: "to offline bob",
		ToPeer:  "12D3KooWBobOfflineTestPeerOfReasonableLengthCCC",
	})
	if err == nil {
		t.Fatal("expected Send to surface the dispatch error when no queue is wired")
	}
}

// TestReceiveCursorNeverSkips is the headline regression for Bug 2: insert
// many new messages mixing priorities, drain via repeated filter=new receives
// with a small MaxResults, and assert every message is delivered exactly once
// with none skipped.
func TestReceiveCursorNeverSkips(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		total      int
		maxResults int
	}{
		{"25 msgs, batch 10", 25, 10},
		{"50 msgs, batch 7", 50, 7},
		{"3 msgs, batch 10 (undersized)", 3, 10},
	}
	priorities := []string{"low", "normal", "high", "critical"}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := newTestDB(t)
			mgr := NewManager(db)
			ctx := context.Background()

			sender := SessionMeta{
				SessionID:    "burst-sender",
				WorkspaceIDs: []string{"global"},
				ClientType:   "test",
			}
			want := make(map[string]bool, tc.total)
			for i := 0; i < tc.total; i++ {
				msg, err := mgr.Send(ctx, sender, SendRequest{
					Kind:        "event",
					Content:     fmt.Sprintf("burst %d", i),
					Priority:    priorities[i%len(priorities)],
					ToWorkspace: "global",
				})
				if err != nil {
					t.Fatalf("send %d: %v", i, err)
				}
				want[msg.ID] = true
				// ULIDs embed millisecond timestamps; nudge the clock so ids
				// are strictly monotonic across the burst.
				time.Sleep(time.Millisecond)
			}

			receiver := SessionMeta{
				SessionID:    "burst-receiver",
				WorkspaceIDs: []string{"ws-other"},
				ClientType:   "test",
			}
			got := make(map[string]int)
			// Generous round cap: ceil(total/maxResults) plus slack.
			maxRounds := tc.total/tc.maxResults + 5
			for round := 0; round < maxRounds; round++ {
				res, err := mgr.Receive(ctx, receiver, ReceiveRequest{
					Filter:     "new",
					MaxResults: tc.maxResults,
				})
				if err != nil {
					t.Fatalf("receive round %d: %v", round, err)
				}
				if len(res.Messages) == 0 {
					break
				}
				if len(res.Messages) > tc.maxResults {
					t.Fatalf("round %d returned %d > MaxResults %d", round, len(res.Messages), tc.maxResults)
				}
				for _, m := range res.Messages {
					got[m.ID]++
				}
			}

			if len(got) != tc.total {
				t.Fatalf("delivered %d distinct messages, want %d (skipped %d)",
					len(got), tc.total, tc.total-len(got))
			}
			for id := range want {
				if got[id] == 0 {
					t.Errorf("message %s was never delivered (silent loss)", id)
				}
			}
		})
	}
}

// TestReceiveCursorReturnsPriorityFirst confirms the delivered batch is still
// ordered priority-first (critical before low) within a single receive — the
// burst-safe selection must not regress the display contract.
func TestReceiveCursorReturnsPriorityFirst(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	mgr := NewManager(db)
	ctx := context.Background()

	sender := SessionMeta{SessionID: "s", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	// Send low first, then critical, so id order and priority order disagree.
	for _, p := range []string{"low", "low", "critical"} {
		if _, err := mgr.Send(ctx, sender, SendRequest{
			Kind: "event", Content: "x", Priority: p, ToWorkspace: "global",
		}); err != nil {
			t.Fatalf("send: %v", err)
		}
		time.Sleep(time.Millisecond)
	}

	receiver := SessionMeta{SessionID: "r", WorkspaceIDs: []string{"ws-other"}, ClientType: "test"}
	res, err := mgr.Receive(ctx, receiver, ReceiveRequest{Filter: "new", MaxResults: 10})
	if err != nil {
		t.Fatalf("receive: %v", err)
	}
	if len(res.Messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(res.Messages))
	}
	if res.Messages[0].Priority != "critical" {
		t.Fatalf("expected critical message first, got priority %q", res.Messages[0].Priority)
	}
}

// TestPendingCount exercises PendingCount across the global namespace: it
// returns 0 before any message, then the live count once messages land, and
// stays scoped to the agent's audience.
func TestPendingCount(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	mgr := NewManager(db)
	ctx := context.Background()

	receiver := SessionMeta{
		SessionID:    "pending-receiver",
		WorkspaceIDs: []string{""}, // global namespace
		ClientType:   "test",
	}

	// Before registration there are no pending messages.
	if n, err := mgr.PendingCount(ctx, receiver); err != nil || n != 0 {
		t.Fatalf("PendingCount before any message = (%d, %v), want (0, nil)", n, err)
	}

	// Register the agent so a cursor row exists.
	if err := mgr.RegisterAgent(ctx, receiver); err != nil {
		t.Fatalf("RegisterAgent: %v", err)
	}

	sender := SessionMeta{SessionID: "pending-sender", WorkspaceIDs: []string{""}, ClientType: "test"}
	for i := 0; i < 3; i++ {
		if _, err := mgr.Send(ctx, sender, SendRequest{
			Kind: "event", Content: fmt.Sprintf("p%d", i), ToWorkspace: "global",
		}); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}

	n, err := mgr.PendingCount(ctx, receiver)
	if err != nil {
		t.Fatalf("PendingCount: %v", err)
	}
	if n != 3 {
		t.Fatalf("PendingCount after 3 global sends = %d, want 3", n)
	}
}
