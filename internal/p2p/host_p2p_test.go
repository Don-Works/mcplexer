//go:build p2p

package p2p

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/libp2p/go-libp2p/core/peer"
)

// newTestEncryptor returns an ephemeral age encryptor backed by an in-memory
// key. Suitable for round-trip tests; does NOT survive process restart.
func newTestEncryptor(t *testing.T) Encryptor {
	t.Helper()
	enc, err := secrets.NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("NewEphemeralEncryptor: %v", err)
	}
	return enc
}

// TestNewHostDisabled verifies the master switch: when Enabled=false, NewHost
// is a no-op (returns nil, nil) so the daemon's behavior is unchanged.
func TestNewHostDisabled(t *testing.T) {
	t.Parallel()
	h, err := NewHost(context.Background(), Config{Enabled: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewHost(disabled): %v", err)
	}
	if h != nil {
		t.Fatalf("NewHost(disabled) = %v, want nil", h)
	}
}

// TestHostDiscoveryConcurrentPublication guards the discovery pointer's
// publication boundary. It is most valuable under -race: mDNS callbacks may
// read the service while daemon startup is attaching it.
func TestHostDiscoveryConcurrentPublication(t *testing.T) {
	t.Parallel()
	h := &Host{}
	services := make([]*DiscoveryService, 64)
	for i := range services {
		services[i] = &DiscoveryService{}
	}

	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for range 512 {
				_ = h.Discovery()
			}
		}()
	}
	close(start)
	for _, service := range services {
		h.setDiscovery(service)
	}
	wg.Wait()

	if got := h.Discovery(); got != services[len(services)-1] {
		t.Fatalf("Discovery() = %p, want latest service %p", got, services[len(services)-1])
	}
}

// TestLoadOrCreateIdentityRoundTrip exercises persistent identity: first call
// creates the file, second call loads it back to the same key.
func TestLoadOrCreateIdentityRoundTrip(t *testing.T) {
	t.Parallel()
	keyPath := filepath.Join(t.TempDir(), "identity.key")

	priv1, err := LoadOrCreateIdentity(keyPath)
	if err != nil {
		t.Fatalf("first LoadOrCreateIdentity: %v", err)
	}
	priv2, err := LoadOrCreateIdentity(keyPath)
	if err != nil {
		t.Fatalf("second LoadOrCreateIdentity: %v", err)
	}
	if !priv1.Equals(priv2) {
		t.Fatalf("identities differ across loads")
	}
}

// TestLoadIdentityEncryptedRoundTrip verifies that the encrypted-identity
// path (used in production) writes to keyPath+".age", round-trips a key
// across loads, and produces a file whose bytes are NOT the cleartext
// libp2p protobuf form.
func TestLoadIdentityEncryptedRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "identity.key")
	enc := newTestEncryptor(t)

	priv1, err := loadIdentity(keyPath, enc)
	if err != nil {
		t.Fatalf("first loadIdentity: %v", err)
	}
	priv2, err := loadIdentity(keyPath, enc)
	if err != nil {
		t.Fatalf("second loadIdentity: %v", err)
	}
	if !priv1.Equals(priv2) {
		t.Fatalf("encrypted identities differ across loads")
	}

	// .age file present, cleartext path absent.
	if _, err := os.Stat(keyPath + ".age"); err != nil {
		t.Fatalf("expected %s.age to exist: %v", keyPath, err)
	}
	if _, err := os.Stat(keyPath); !os.IsNotExist(err) {
		t.Fatalf("cleartext key should not exist at %s (err=%v)", keyPath, err)
	}
}

// TestTwoHostsPingLocalhost is the spike acceptance test: two libp2p hosts on
// localhost with separate identities discover each other (via direct dial)
// and round-trip a ping. mDNS isn't strictly required here — we exercise it
// elsewhere — so this test wires the connection explicitly to avoid flakes
// from environments without multicast (CI, sandboxes).
func TestTwoHostsPingLocalhost(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	// Build a /p2p/<id> multiaddr from B's first listen addr.
	if len(b.Addrs()) == 0 {
		t.Fatal("host B has no listen addrs")
	}
	target := fmt.Sprintf("%s/p2p/%s", b.Addrs()[0], b.ID())
	got, err := a.Connect(ctx, target)
	if err != nil {
		t.Fatalf("a.Connect(b): %v", err)
	}
	if got != b.ID() {
		t.Fatalf("connected peer ID = %s, want %s", got, b.ID())
	}

	if err := pingOnce(ctx, a, b.ID()); err != nil {
		t.Fatalf("ping a->b: %v", err)
	}
}

// TestMDNSDiscoveryLocalhost boots two hosts with mDNS enabled and waits for
// each to learn the other's addrs. Skipped if multicast is unavailable.
func TestMDNSDiscoveryLocalhost(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping mDNS discovery test in short mode")
	}
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tag := fmt.Sprintf("mcplexer-p2p-test-%d", time.Now().UnixNano())
	a := startTestHostWithTag(t, "a", tag)
	defer func() { _ = a.Close() }()
	b := startTestHostWithTag(t, "b", tag)
	defer func() { _ = b.Close() }()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if peerKnown(a, b.ID()) && peerKnown(b, a.ID()) {
			return
		}
		select {
		case <-ctx.Done():
			t.Skipf("context done before mDNS discovery: %v", ctx.Err())
		case <-time.After(150 * time.Millisecond):
		}
	}
	t.Skipf("mDNS discovery did not converge — likely no multicast on this host")
}

// startTestHost boots a Host with localhost-only TCP listeners and a temp
// identity file, mDNS off. Useful for unit tests that don't need discovery.
func startTestHost(t *testing.T, name string) *Host {
	t.Helper()
	return startTestHostWithTag(t, name, "")
}

// startTestHostWithTag boots a Host with optional mDNS service tag. Empty tag
// disables mDNS.
func startTestHostWithTag(t *testing.T, name, mdnsTag string) *Host {
	t.Helper()
	cfg := Config{
		Enabled:      true,
		IdentityPath: filepath.Join(t.TempDir(), name+"-identity.key"),
		ListenAddrs: []string{
			"/ip4/127.0.0.1/tcp/0",
		},
		EnableMDNS:        mdnsTag != "",
		EnableHolePunch:   false,
		EnableRelayClient: false,
		EnableAutoNAT:     false,
		MDNSServiceTag:    mdnsTag,
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

// TestConfigDefaultsApplyBootstrapRelays verifies that when the relay client
// is enabled and BootstrapRelays is empty, withDefaults populates it from
// DefaultBootstrapRelays so the host has a working static fallback list.
func TestConfigDefaultsApplyBootstrapRelays(t *testing.T) {
	t.Parallel()
	c := Config{Enabled: true, EnableRelayClient: true}.withDefaults()
	if got := len(c.BootstrapRelays); got != 3 {
		t.Fatalf("default bootstrap relays = %d, want 3", got)
	}
	if c.ConnMgrLowWater != 50 || c.ConnMgrHighWater != 200 {
		t.Fatalf("default conn mgr watermarks = %d/%d, want 50/200",
			c.ConnMgrLowWater, c.ConnMgrHighWater)
	}
}

// TestConnectionModeDirect boots two hosts on localhost and verifies that
// the connection-mode reporter classifies the dial-in conn as "direct"
// (no relay, no hole-punch involved).
func TestConnectionModeDirect(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	target := fmt.Sprintf("%s/p2p/%s", b.Addrs()[0], b.ID())
	if _, err := a.Connect(ctx, target); err != nil {
		t.Fatalf("a.Connect(b): %v", err)
	}
	if got := a.ConnectionMode(b.ID()); got != ModeDirect {
		t.Fatalf("ConnectionMode(b) = %q, want %q", got, ModeDirect)
	}
	if got := a.ConnectionMode(peer.ID("nonexistent")); got != ModeNone {
		t.Fatalf("ConnectionMode(nonexistent) = %q, want %q", got, ModeNone)
	}
}

// TestPeerModesEnumerates verifies PeerModes returns one entry per active
// connection. We use the same direct-dial setup as the other tests because
// real relay scenarios need three hosts and a circuit-relay server, which
// is exercised in TestConnectionModeRelay.
func TestPeerModesEnumerates(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	a := startTestHost(t, "a")
	defer func() { _ = a.Close() }()
	b := startTestHost(t, "b")
	defer func() { _ = b.Close() }()

	target := fmt.Sprintf("%s/p2p/%s", b.Addrs()[0], b.ID())
	if _, err := a.Connect(ctx, target); err != nil {
		t.Fatalf("a.Connect(b): %v", err)
	}
	modes := a.PeerModes()
	if len(modes) != 1 {
		t.Fatalf("PeerModes() len = %d, want 1", len(modes))
	}
	if modes[0].Mode != ModeDirect {
		t.Fatalf("PeerModes()[0].Mode = %q, want %q", modes[0].Mode, ModeDirect)
	}
	if modes[0].Peer != b.ID().String() {
		t.Fatalf("PeerModes()[0].Peer = %q, want %q", modes[0].Peer, b.ID())
	}
}

// pingOnce drains a single result from the ping service.
func pingOnce(ctx context.Context, from *Host, target peer.ID) error {
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	results := from.Pinger().Ping(pingCtx, target)
	select {
	case res, ok := <-results:
		if !ok {
			return fmt.Errorf("ping channel closed")
		}
		if res.Error != nil {
			return res.Error
		}
		return nil
	case <-pingCtx.Done():
		return pingCtx.Err()
	}
}

// peerKnown returns true if h has at least one address for target in its
// peerstore.
func peerKnown(h *Host, target peer.ID) bool {
	return len(h.Inner().Peerstore().Addrs(target)) > 0
}
