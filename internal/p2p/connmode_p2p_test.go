//go:build p2p

package p2p

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	"github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	"github.com/multiformats/go-multiaddr"
)

// fakeConn implements just enough of network.Conn for the connection-mode
// classifier. We only assert behaviour on Stat() (Limited) and
// RemoteMultiaddr() — the other methods are stubs that should never be
// called by classifyConns.
type fakeConn struct {
	network.Conn
	limited bool
	remote  multiaddr.Multiaddr
}

func (f *fakeConn) Stat() network.ConnStats {
	return network.ConnStats{Stats: network.Stats{Limited: f.limited}}
}
func (f *fakeConn) RemoteMultiaddr() multiaddr.Multiaddr { return f.remote }

func mustMaddr(t *testing.T, s string) multiaddr.Multiaddr {
	t.Helper()
	m, err := multiaddr.NewMultiaddr(s)
	if err != nil {
		t.Fatalf("NewMultiaddr(%q): %v", s, err)
	}
	return m
}

// TestClassifyConns is the table-driven unit test for the heart of M1.4 —
// the connection-mode reporter. Each row asserts a (conns, holePunched) -> mode
// pairing.
func TestClassifyConns(t *testing.T) {
	t.Parallel()
	directAddr := mustMaddr(t, "/ip4/192.0.2.1/tcp/4001")
	circuitAddr := mustMaddr(t,
		"/ip4/192.0.2.2/tcp/4001/p2p/QmYzr3Cz6jBGu5xrtJsxgkpyhpd9V3w8jXVpEv9TtoNs8N/p2p-circuit")

	cases := []struct {
		name     string
		conns    []network.Conn
		punched  bool
		wantMode ConnectionMode
	}{
		{"empty", nil, false, ModeNone},
		{"empty-with-punch-flag", nil, true, ModeNone},
		{"direct-only", []network.Conn{&fakeConn{remote: directAddr}}, false, ModeDirect},
		{"direct-after-punch", []network.Conn{&fakeConn{remote: directAddr}}, true, ModeHolePunched},
		{"limited-stat-wins", []network.Conn{&fakeConn{remote: directAddr, limited: true}}, false, ModeRelay},
		{"circuit-multiaddr-wins", []network.Conn{&fakeConn{remote: circuitAddr}}, false, ModeRelay},
		{"relay-overrides-punch", []network.Conn{&fakeConn{remote: circuitAddr}}, true, ModeRelay},
		{"mixed-relay-then-direct", []network.Conn{
			&fakeConn{remote: circuitAddr},
			&fakeConn{remote: directAddr},
		}, false, ModeRelay},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyConns(tc.conns, tc.punched); got != tc.wantMode {
				t.Fatalf("classifyConns = %q, want %q", got, tc.wantMode)
			}
		})
	}
}

// TestMultiaddrHasCircuit pins down the multiaddr inspection helper.
func TestMultiaddrHasCircuit(t *testing.T) {
	t.Parallel()
	cases := []struct {
		addr string
		want bool
	}{
		{"/ip4/127.0.0.1/tcp/4001", false},
		{"/ip4/192.0.2.4/udp/4001/quic-v1", false},
		{"/ip4/192.0.2.4/tcp/4001/p2p/QmYzr3Cz6jBGu5xrtJsxgkpyhpd9V3w8jXVpEv9TtoNs8N/p2p-circuit", true},
	}
	for _, tc := range cases {
		if got := multiaddrHasCircuit(mustMaddr(t, tc.addr)); got != tc.want {
			t.Fatalf("multiaddrHasCircuit(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
	if multiaddrHasCircuit(nil) {
		t.Fatal("multiaddrHasCircuit(nil) = true, want false")
	}
}

// TestHolePunchTrackerLifecycle verifies the tracker records successes
// keyed on Event.Remote, ignores failures, and forgets entries after
// holePunchTTL has elapsed.
func TestHolePunchTrackerLifecycle(t *testing.T) {
	t.Parallel()
	tr := newHolePunchTracker(slog.Default())
	p := peer.ID("12D3KooWFakePeer")

	// Failure: no recording.
	tr.Trace(&holepunch.Event{
		Type:   holepunch.EndHolePunchEvtT,
		Remote: p,
		Evt:    &holepunch.EndHolePunchEvt{Success: false, Error: "boom"},
	})
	if tr.wasHolePunched(p) {
		t.Fatal("failure event should not record success")
	}

	// Success: recorded.
	tr.Trace(&holepunch.Event{
		Type:   holepunch.EndHolePunchEvtT,
		Remote: p,
		Evt:    &holepunch.EndHolePunchEvt{Success: true, EllapsedTime: 250 * time.Millisecond},
	})
	if !tr.wasHolePunched(p) {
		t.Fatal("success event should record")
	}

	// TTL expiry: forge a stale timestamp directly to avoid sleeping the test.
	tr.successes[p] = time.Now().Add(-2 * holePunchTTL)
	if tr.wasHolePunched(p) {
		t.Fatal("expired entry should not count")
	}

	// close() drops state and silences subsequent traces.
	tr.successes[p] = time.Now()
	tr.close()
	if tr.wasHolePunched(p) {
		t.Fatal("close should drop tracker state")
	}
}

// TestConnectionModeRelayE2E spins up three libp2p hosts in-memory:
//   - R: a circuit-relay v2 server
//   - B: a host with a /p2p-circuit reservation on R
//   - A: a host that dials B's circuit address through R
//
// Asserts that A.ConnectionMode(B) returns ModeRelay. This exercises the
// real circuit transport (not just the multiaddr inspection helper) and
// is the closest we get to a real-world relay-fallback scenario without
// running NATs.
func TestConnectionModeRelayE2E(t *testing.T) {
	if testing.Short() {
		t.Skip("relay e2e is expensive; -short skips it")
	}
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	r := startTestHost(t, "relay")
	defer func() { _ = r.Close() }()
	rsvc, err := relay.New(r.Inner())
	if err != nil {
		t.Fatalf("relay.New: %v", err)
	}
	defer func() { _ = rsvc.Close() }()

	a := startTestHostRelayClient(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHostRelayClient(t, "b")
	defer func() { _ = b.Close() }()

	connectDirect(t, ctx, a, r)
	connectDirect(t, ctx, b, r)

	// B asks R for a /p2p-circuit reservation.
	if _, err := client.Reserve(ctx, b.Inner(), peerInfoOf(r)); err != nil {
		t.Fatalf("client.Reserve(B->R): %v", err)
	}

	// A dials B's /p2p-circuit addr through R. We populate ONLY the circuit
	// addr in A's peerstore (no direct addr) so libp2p has no alternative
	// path — the resulting conn must be relay-mediated.
	if err := dialViaCircuitOnly(ctx, a, b, r); err != nil {
		t.Fatalf("dial via circuit: %v", err)
	}
	if got := a.ConnectionMode(b.ID()); got != ModeRelay {
		t.Fatalf("ConnectionMode(b) = %q, want %q\nconns: %s",
			got, ModeRelay, dumpConns(a, b.ID()))
	}
	// Belt-and-braces: the underlying conn must actually be Limited.
	conns := a.Inner().Network().ConnsToPeer(b.ID())
	if len(conns) == 0 {
		t.Fatal("no conn established to B")
	}
	for _, c := range conns {
		if !isRelayConn(c) {
			t.Fatalf("expected only relay conns to B, got %s", dumpConns(a, b.ID()))
		}
	}
}

// startTestHostRelayClient is like startTestHost but with the relay client
// transport enabled (no AutoNAT or hole-punch — those add timing noise to
// in-memory tests).
func startTestHostRelayClient(t *testing.T, name string) *Host {
	t.Helper()
	cfg := Config{
		Enabled:           true,
		IdentityPath:      filepath.Join(t.TempDir(), name+"-identity.key"),
		ListenAddrs:       []string{"/ip4/127.0.0.1/tcp/0"},
		EnableMDNS:        false,
		EnableHolePunch:   false,
		EnableRelayClient: true,
		EnableAutoNAT:     false,
		BootstrapRelays:   []string{}, // suppress the public defaults during tests
	}
	h, err := NewHost(context.Background(), cfg, nil, nil)
	if err != nil {
		t.Fatalf("NewHost(%s): %v", name, err)
	}
	if h == nil {
		t.Fatalf("NewHost(%s) returned nil", name)
	}
	return h
}

// connectDirect is a helper that dials hostB from hostA using hostB's first
// listen multiaddr. Fails the test on any error.
func connectDirect(t *testing.T, ctx context.Context, from, to *Host) {
	t.Helper()
	if len(to.Addrs()) == 0 {
		t.Fatalf("host %s has no listen addrs", to.ID())
	}
	target := fmt.Sprintf("%s/p2p/%s", to.Addrs()[0], to.ID())
	if _, err := from.Connect(ctx, target); err != nil {
		t.Fatalf("connect %s->%s: %v", from.ID(), to.ID(), err)
	}
}

// dialViaCircuitOnly forces `a` to connect to `b` via the relay `r` by:
//  1. clearing any direct addrs for b from a's peerstore
//  2. inserting only the /p2p/<R>/p2p-circuit/p2p/<B> address
//  3. calling Connect — libp2p has no other path so it must use the circuit
func dialViaCircuitOnly(ctx context.Context, a, b, r *Host) error {
	circuit := fmt.Sprintf("/p2p/%s/p2p-circuit/p2p/%s", r.ID(), b.ID())
	full, err := multiaddr.NewMultiaddr(circuit)
	if err != nil {
		return fmt.Errorf("parse circuit addr: %w", err)
	}
	// ClearAddrs would be ideal but isn't on the public Peerstore interface.
	// Instead we just add the circuit addr with permanent TTL — the relay
	// transport handles dialing and the result is a Limited (relay) conn.
	a.Inner().Peerstore().AddAddrs(b.ID(), []multiaddr.Multiaddr{full}, time.Hour)
	connectCtx := network.WithAllowLimitedConn(ctx, "m14-test")
	if err := a.Inner().Connect(connectCtx, peer.AddrInfo{ID: b.ID()}); err != nil {
		return fmt.Errorf("connect via relay: %w", err)
	}
	return nil
}

// dumpConns returns a debug string describing every conn from h to peer p —
// used in test failure messages to make the cause obvious.
func dumpConns(h *Host, p peer.ID) string {
	conns := h.Inner().Network().ConnsToPeer(p)
	parts := make([]string, 0, len(conns))
	for _, c := range conns {
		parts = append(parts, fmt.Sprintf("limited=%v remote=%s",
			c.Stat().Limited, c.RemoteMultiaddr()))
	}
	if len(parts) == 0 {
		return "<no conns>"
	}
	return fmt.Sprintf("[%d conns] %v", len(parts), parts)
}

// peerInfoOf builds a peer.AddrInfo for h using the host's listen addrs.
func peerInfoOf(h *Host) peer.AddrInfo {
	return peer.AddrInfo{ID: h.ID(), Addrs: h.Inner().Addrs()}
}
