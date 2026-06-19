//go:build p2p

package p2p

import (
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// libp2pPrivKey aliases the crypto package's PrivKey to keep host.go imports
// short and avoid leaking the libp2p crypto package across our public API
// (callers should use LoadOrCreateIdentity, not the alias).
type libp2pPrivKey = crypto.PrivKey

// peerstoreTTL controls how long mDNS-discovered addresses are kept.
const peerstoreTTL = 10 * time.Minute

// multiaddrsAsStrings converts a slice of multiaddrs to strings for logging.
func multiaddrsAsStrings(addrs []multiaddr.Multiaddr) []string {
	out := make([]string, len(addrs))
	for i, a := range addrs {
		out[i] = a.String()
	}
	return out
}

// dialableAddrsForPairing returns just the direct, dialable multiaddrs from a
// host's listen-addr list — stripping circuit-relay, webtransport, and
// webrtc-direct entries. The QR pairing payload has a hard size limit
// (~3 KB binary, less in alphanumeric mode), and once auto-relay is on, the
// raw Addrs() list balloons with dozens of relay-mediated addresses that
// don't help an LAN-or-Tailscale peer dial back. The post-pair DHT finds
// any addrs we omit.
func dialableAddrsForPairing(addrs []string) []string {
	skip := []string{
		"/p2p-circuit", "/webtransport", "/webrtc-direct", "/certhash",
		"/ip6/fe80:", "/ip6/::1/",
	}
	out := make([]string, 0, len(addrs))
addr:
	for _, a := range addrs {
		if a == "" {
			continue
		}
		for _, s := range skip {
			if strings.Contains(a, s) {
				continue addr
			}
		}
		out = append(out, a)
	}
	return out
}

// parseRelays converts a slice of multiaddr strings (each containing /p2p/ID)
// into AddrInfo records suitable for libp2p.EnableAutoRelayWithStaticRelays.
func parseRelays(addrs []string) ([]peer.AddrInfo, error) {
	out := make([]peer.AddrInfo, 0, len(addrs))
	for _, s := range addrs {
		ma, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			return nil, fmt.Errorf("p2p: parse relay %q: %w", s, err)
		}
		info, err := peer.AddrInfoFromP2pAddr(ma)
		if err != nil {
			return nil, fmt.Errorf("p2p: relay addr info %q: %w", s, err)
		}
		out = append(out, *info)
	}
	return out, nil
}
