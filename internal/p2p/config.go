package p2p

import (
	"os"
	"path/filepath"
)

// Config controls the embedded libp2p Host.
//
// Defaults (when Enabled is true and other fields are zero):
//   - IdentityPath: ~/.mcplexer/p2p/identity.key
//   - ListenAddrs:  TCP/0 + QUIC-v1/0 on all interfaces
//   - EnableMDNS, EnableHolePunch, EnableRelayClient, EnableAutoNAT: true
//   - BootstrapRelays: DefaultBootstrapRelays() — three public IPFS bootstrap
//     nodes that double as circuit-relay v2 servers. Override with your own
//     addresses if you operate dedicated relays.
//   - ConnMgrLowWater / ConnMgrHighWater: 50 / 200
type Config struct {
	// Enabled is the master switch. When false, NewHost returns (nil, nil) and
	// the daemon behaves identically to a build without libp2p wired in.
	Enabled bool

	// IdentityPath is the absolute path to the Ed25519 private-key file.
	// When NewHost is called with an Encryptor, the on-disk file is at
	// IdentityPath + ".age" (encrypted). When called without one, the key
	// is at IdentityPath in cleartext (tests, dev). Files are created with
	// 0600; parent dirs with 0700.
	IdentityPath string

	// ListenAddrs are libp2p multiaddrs. If empty, sensible defaults are used:
	// TCP and QUIC-v1 on all interfaces with OS-assigned ports.
	ListenAddrs []string

	// EnableMDNS turns on local-network peer discovery via multicast DNS.
	EnableMDNS bool

	// EnableHolePunch turns on DCUtR for NAT traversal.
	EnableHolePunch bool

	// EnableRelayClient lets this host use circuit-v2 relays as a client.
	// We never operate as a relay server — that's a public-good role we
	// deliberately don't take on.
	EnableRelayClient bool

	// EnableAutoNAT turns on AutoNAT (reachability detection). Defaults to
	// true when Enabled is true. Required for hole-punching to know whether
	// our address is publicly reachable.
	EnableAutoNAT bool

	// EnableDHT turns on the libp2p Kademlia DHT in AutoServer mode. Required
	// for cross-network peer rediscovery: without it, the host can only find
	// peers via mDNS (same LAN) and is helpless after either side moves IP.
	// Defaults to true when Enabled is true.
	EnableDHT bool

	// BootstrapRelays are static circuit-v2 relay multiaddrs to dial on
	// startup. When empty, DefaultBootstrapRelays() is used.
	BootstrapRelays []string

	// MDNSServiceTag scopes mDNS discovery to peers using the same tag.
	// Defaults to "mcplexer-p2p".
	MDNSServiceTag string

	// ConnMgrLowWater is the connection-manager low-water mark. When the
	// number of connections exceeds the high-water mark, the manager prunes
	// down to this number. Defaults to 50.
	ConnMgrLowWater int

	// ConnMgrHighWater is the connection-manager high-water mark. Defaults
	// to 200. The manager is a coarse cap — most desktop installations will
	// stay well below this.
	ConnMgrHighWater int
}

// DefaultBootstrapRelays returns three well-known public libp2p bootstrap
// nodes that operate circuit-relay v2. Sourced from the IPFS Kubo fallback
// list (see ipfs/boxo autoconf.FallbackBootstrapPeers). These are operated
// by Protocol Labs as a public good — we are clients only and never serve
// as a relay ourselves.
//
// If you operate your own relays, override Config.BootstrapRelays.
func DefaultBootstrapRelays() []string {
	return []string{
		// Protocol Labs bootstrap nodes (also act as circuit-v2 relays).
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmNnooDu7bfjPFoTZYxMNLWUQJyrVwtbZg5gBMjTezGAJN",
		"/dnsaddr/bootstrap.libp2p.io/p2p/QmQCU2EcMqAqQPR2i9bChDtGNJchTbq5TbXJJ16u19uLTa",
		"/dnsaddr/va1.bootstrap.libp2p.io/p2p/12D3KooWKnDdG3iXw9eTFijk3EWSunZcFi54Zka4wmtqtt6rPxc8",
	}
}

// DefaultIdentityPath returns ~/.mcplexer/p2p/identity.key, falling back to a
// CWD-relative path if the home dir can't be resolved.
func DefaultIdentityPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".mcplexer", "p2p", "identity.key")
	}
	return filepath.Join(home, ".mcplexer", "p2p", "identity.key")
}

// DefaultListenAddrs returns the default TCP + QUIC listen multiaddrs.
func DefaultListenAddrs() []string {
	return []string{
		"/ip4/0.0.0.0/tcp/0",
		"/ip6/::/tcp/0",
		"/ip4/0.0.0.0/udp/0/quic-v1",
		"/ip6/::/udp/0/quic-v1",
	}
}

// withDefaults fills in zero-value fields with sensible defaults.
func (c Config) withDefaults() Config {
	if c.IdentityPath == "" {
		c.IdentityPath = DefaultIdentityPath()
	}
	if len(c.ListenAddrs) == 0 {
		c.ListenAddrs = DefaultListenAddrs()
	}
	if c.MDNSServiceTag == "" {
		c.MDNSServiceTag = "mcplexer-p2p"
	}
	// Only fall back to defaults when BootstrapRelays is nil. Callers that
	// pass an empty (but non-nil) slice are explicitly opting out — useful
	// for tests that exercise the relay transport without auto-relay.
	if (c.EnableRelayClient || c.EnableDHT) && c.BootstrapRelays == nil {
		c.BootstrapRelays = DefaultBootstrapRelays()
	}
	if c.ConnMgrLowWater == 0 {
		c.ConnMgrLowWater = 50
	}
	if c.ConnMgrHighWater == 0 {
		c.ConnMgrHighWater = 200
	}
	return c
}
