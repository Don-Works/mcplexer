//go:build p2p

package p2p

import (
	"context"
	"errors"
	"testing"
	"time"
)

// meshWSLookup is an in-memory MeshWorkspaceLookup for tests: maps a peer ID
// to the set of local workspace IDs it is bound to. A nil entry means the
// peer is unbound (default-deny). `failure` forces every call to error.
type meshWSLookup struct {
	bindings map[string][]string
	failure  error
}

func (m *meshWSLookup) ListLocalWorkspaceIDsForPeer(_ context.Context, peerID string) ([]string, error) {
	if m.failure != nil {
		return nil, m.failure
	}
	return m.bindings[peerID], nil
}

// TestPeerAuthorizedForWorkspace is the table-driven proof that the outbound
// broadcast ACL never leaks a workspace-scoped envelope to a peer that is not
// bound to that workspace. This is the core security invariant of
// workspace-scoped pairing on the mesh channel.
func TestPeerAuthorizedForWorkspace(t *testing.T) {
	t.Parallel()

	const (
		peerBound   = "peerA" // bound to ws-x (and ws-z)
		peerOther   = "peerB" // bound to ws-y only
		peerUnbound = "peerC" // no bindings at all
	)
	lookup := &meshWSLookup{bindings: map[string][]string{
		peerBound: {"ws-x", "ws-z"},
		peerOther: {"ws-y"},
		// peerUnbound intentionally absent.
	}}

	cases := []struct {
		name        string
		wsLookup    MeshWorkspaceLookup
		peerID      string
		envWS       string
		wantAllowed bool
	}{
		// Empty / global envelopes fan out to everyone regardless of binding.
		{"empty workspace -> all peers", lookup, peerOther, "", true},
		{"global workspace -> all peers", lookup, peerUnbound, "global", true},
		// Scoped envelope reaches the bound peer.
		{"scoped ws reaches bound peer", lookup, peerBound, "ws-x", true},
		{"scoped second ws reaches bound peer", lookup, peerBound, "ws-z", true},
		// Scoped envelope must NOT reach a peer bound to a different workspace.
		{"scoped ws-x does NOT reach ws-y peer", lookup, peerOther, "ws-x", false},
		// Scoped envelope must NOT reach an entirely unbound peer.
		{"scoped ws does NOT reach unbound peer", lookup, peerUnbound, "ws-x", false},
		// Missing ACL wiring must fail closed for scoped envelopes.
		{"nil lookup denies scoped", nil, peerUnbound, "ws-x", false},
		// Empty/global broadcasts remain explicitly unscoped even without a lookup.
		{"nil lookup allows global", nil, peerUnbound, "global", true},
		// A lookup error is default-deny for scoped envelopes.
		{
			name:        "lookup error -> deny scoped",
			wsLookup:    &meshWSLookup{failure: errors.New("db down")},
			peerID:      peerBound,
			envWS:       "ws-x",
			wantAllowed: false,
		},
		// ...but an error still allows empty/global (short-circuits before lookup).
		{
			name:        "lookup error -> allow global",
			wsLookup:    &meshWSLookup{failure: errors.New("db down")},
			peerID:      peerBound,
			envWS:       "global",
			wantAllowed: true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			tr := &MeshTransport{logger: testLogger()}
			tr.SetWorkspaceLookup(tc.wsLookup)
			got := tr.peerAuthorizedForWorkspace(context.Background(), tc.peerID, tc.envWS)
			if got != tc.wantAllowed {
				t.Fatalf("peerAuthorizedForWorkspace(%q, ws=%q) = %v, want %v",
					tc.peerID, tc.envWS, got, tc.wantAllowed)
			}
		})
	}
}

// TestSendBroadcastWorkspaceScoping is the end-to-end proof over real libp2p
// hosts: A is paired with B (bound to ws-x) and C (bound to ws-y). A
// broadcasts a ws-x-scoped envelope; only B must receive it, never C.
func TestSendBroadcastWorkspaceScoping(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()
	c := startTestHost(t, "c")
	defer func() { _ = c.Close() }()
	connectHosts(t, ctx, a, b)
	connectHosts(t, ctx, a, c)

	pairs := newMeshLookup(a.PeerID(), b.PeerID(), c.PeerID())
	aTrans := NewMeshTransport(a, pairs, nil, nil)
	// B is bound to ws-x; C is bound to ws-y. A ws-x broadcast must reach
	// B only.
	aTrans.SetWorkspaceLookup(&meshWSLookup{bindings: map[string][]string{
		b.PeerID(): {"ws-x"},
		c.PeerID(): {"ws-y"},
	}})
	bTrans := NewMeshTransport(b, pairs, nil, nil)
	cTrans := NewMeshTransport(c, pairs, nil, nil)
	bTrans.Start()
	cTrans.Start()
	defer func() { _ = aTrans.Close() }()
	defer func() { _ = bTrans.Close() }()
	defer func() { _ = cTrans.Close() }()

	bRx := bTrans.Subscribe()
	cRx := cTrans.Subscribe()

	env := &MeshEnvelope{
		ID:          newULID(),
		Kind:        "finding",
		Content:     "ws-x only",
		WorkspaceID: "ws-x",
		Recipient:   Recipient{Kind: "audience", Value: "*"},
	}
	sent, err := aTrans.SendBroadcast(ctx, env)
	if err != nil {
		t.Fatalf("SendBroadcast: %v", err)
	}
	if sent != 1 {
		t.Fatalf("SendBroadcast sent = %d, want 1 (only the ws-x-bound peer)", sent)
	}

	// B (bound to ws-x) must receive it.
	select {
	case got := <-bRx:
		if got.ID != env.ID {
			t.Fatalf("B rx id = %q, want %q", got.ID, env.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: ws-x-bound peer B did not receive the broadcast")
	}

	// C (bound to ws-y) must NOT receive it — the leak this whole change
	// exists to prevent.
	select {
	case got := <-cRx:
		t.Fatalf("LEAK: ws-y peer C received a ws-x envelope: %+v", got)
	case <-time.After(500 * time.Millisecond):
		// Expected: nothing crossed.
	}
}

// TestSendBroadcastGlobalReachesAll confirms an empty/global-workspace
// broadcast still fans out to every connected paired peer (legacy + explicit
// broadcast semantics are preserved).
func TestSendBroadcastGlobalReachesAll(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()
	c := startTestHost(t, "c")
	defer func() { _ = c.Close() }()
	connectHosts(t, ctx, a, b)
	connectHosts(t, ctx, a, c)

	pairs := newMeshLookup(a.PeerID(), b.PeerID(), c.PeerID())
	aTrans := NewMeshTransport(a, pairs, nil, nil)
	// Even with a strict workspace lookup wired, a workspace-less envelope
	// must reach all peers.
	aTrans.SetWorkspaceLookup(&meshWSLookup{bindings: map[string][]string{
		b.PeerID(): {"ws-x"},
		c.PeerID(): {"ws-y"},
	}})
	bTrans := NewMeshTransport(b, pairs, nil, nil)
	cTrans := NewMeshTransport(c, pairs, nil, nil)
	bTrans.Start()
	cTrans.Start()
	defer func() { _ = aTrans.Close() }()
	defer func() { _ = bTrans.Close() }()
	defer func() { _ = cTrans.Close() }()

	bRx := bTrans.Subscribe()
	cRx := cTrans.Subscribe()

	env := &MeshEnvelope{
		ID:        newULID(),
		Kind:      "finding",
		Content:   "global broadcast",
		Recipient: Recipient{Kind: "audience", Value: "*"},
		// WorkspaceID intentionally empty -> global.
	}
	sent, err := aTrans.SendBroadcast(ctx, env)
	if err != nil {
		t.Fatalf("SendBroadcast: %v", err)
	}
	if sent != 2 {
		t.Fatalf("SendBroadcast sent = %d, want 2 (both peers)", sent)
	}
	for name, rx := range map[string]<-chan MeshEnvelope{"B": bRx, "C": cRx} {
		select {
		case got := <-rx:
			if got.ID != env.ID {
				t.Fatalf("%s rx id = %q, want %q", name, got.ID, env.ID)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout: peer %s did not receive the global broadcast", name)
		}
	}
}
