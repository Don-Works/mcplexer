package mesh

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestDeNamespaceRemoteSession pins the transform that lets a targeted
// to_agent send reach ONE remote session instead of broadcasting: the
// agent-directory sink stores a peer's agents as "peer:<peerID>:<realSession>",
// and the wire must carry <realSession> (what the receiver keys its local
// sessions by). It returns "" — caller falls back to peer-addressing — only
// when the audience is not a session namespaced for THIS peer.
func TestDeNamespaceRemoteSession(t *testing.T) {
	const peer = "12D3KooWTargetPeerOfReasonableLength0001"
	const other = "12D3KooWOtherPeerOfReasonableLength00002"
	cases := []struct {
		name     string
		audience string
		toPeer   string
		want     string
	}{
		{"namespaced for this peer -> real session", "peer:" + peer + ":sess-abc", peer, "sess-abc"},
		{"real session may contain colons", "peer:" + peer + ":sess:with:colons", peer, "sess:with:colons"},
		{"namespaced for a DIFFERENT peer -> fall back", "peer:" + other + ":sess-abc", peer, ""},
		{"bare peer id (ingest fallback) -> fall back", "peer:" + peer, peer, ""},
		{"plain local session id -> fall back", "some-local-session", peer, ""},
		{"star audience -> fall back", "*", peer, ""},
		{"empty audience -> fall back", "", peer, ""},
		{"empty peer -> fall back", "peer:" + peer + ":sess-abc", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deNamespaceRemoteSession(tc.audience, tc.toPeer); got != tc.want {
				t.Fatalf("deNamespaceRemoteSession(%q, %q) = %q, want %q", tc.audience, tc.toPeer, got, tc.want)
			}
		})
	}
}

// TestDispatchP2P_RemoteToAgentCarriesRealSession is the end-to-end regression
// for the remote-over-broadcast bug: a to_agent send to a peer-origin agent
// must put that agent's REAL (de-namespaced) session id on the wire as
// Recipient{Kind:"audience"} so the receiver files it to the one addressed
// session — NOT Recipient{Kind:"peer"} (which the receiver stores Audience="*"
// and delivers to every agent in the workspace).
func TestDispatchP2P_RemoteToAgentCarriesRealSession(t *testing.T) {
	t.Parallel()
	const selfPeer = "12D3KooWSelfPeerOfReasonableLengthAAAAAAA"
	const targetPeer = "12D3KooWTargetPeerOfReasonableLenBBBBBBBB"
	const realSession = "remote-worker-real-session"

	db := newTestDB(t)
	mgr := NewManager(db)
	ft := newFakeTransport()
	mgr.SetP2PTransport(ft, selfPeer)
	ctx := context.Background()
	now := time.Now().UTC()

	// A peer-origin agent as the directory sink would store it: session id
	// namespaced with the source peer, origin = "peer:<peerID>".
	if err := db.UpsertMeshAgent(ctx, &store.MeshAgent{
		SessionID:  store.MeshAgentOriginPeerPrefix + targetPeer + ":" + realSession,
		WorkspaceID: "global",
		Name:       "remote-worker",
		ClientType: "test",
		Origin:     store.MeshAgentOriginPeerPrefix + targetPeer,
		LastSeenAt: now,
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("seed remote agent: %v", err)
	}

	sender := SessionMeta{SessionID: "sender-session", WorkspaceIDs: []string{"global"}, ClientType: "test"}
	if _, err := mgr.Send(ctx, sender, SendRequest{
		Kind:    "event",
		Content: "targeted coordination for the remote worker",
		ToAgent: "remote-worker",
	}); err != nil {
		t.Fatalf("send to_agent: %v", err)
	}

	sends := ft.targetedSends()
	if len(sends) != 1 {
		t.Fatalf("expected exactly 1 targeted send, got %d", len(sends))
	}
	if sends[0].peerID != targetPeer {
		t.Fatalf("routed to peer %q, want %q", sends[0].peerID, targetPeer)
	}
	rc := sends[0].env.Recipient
	if rc.Kind != "audience" {
		t.Fatalf("Recipient.Kind = %q, want \"audience\" (peer-addressing would broadcast on the receiver)", rc.Kind)
	}
	if rc.Value != realSession {
		t.Fatalf("Recipient.Value = %q, want the de-namespaced real session %q", rc.Value, realSession)
	}
	// Must NOT leak the namespaced form or the peer id onto the wire.
	if rc.Value == store.MeshAgentOriginPeerPrefix+targetPeer+":"+realSession {
		t.Fatal("wire carried the namespaced session id; receiver would match no local session")
	}
}
