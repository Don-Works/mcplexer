//go:build p2p

package p2p

import (
	"context"
	"fmt"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// seedPeerAddrs feeds the libp2p peerstore with addresses for pid before
// dialing during a pair handshake. When addrs are supplied (the legacy QR
// payload that still embeds multiaddrs) we use them directly; otherwise we
// walk the DHT via host.FindPeer.
//
// Returns a non-nil error only when neither source produced any usable
// addresses — the caller can't dial without them. Errors here surface up to
// the user as "complete pair: locate peer …", which the UI shows as a
// retryable failure.
func (s *PairingService) seedPeerAddrs(
	ctx context.Context, pid peer.ID, addrs []string,
) error {
	if len(addrs) > 0 {
		mas, err := parseMultiaddrs(addrs)
		if err != nil {
			return err
		}
		s.host.Inner().Peerstore().AddAddrs(pid, mas, peerstoreTTL)
		return nil
	}
	// No addrs in the payload — ask the DHT. New StartPair payloads omit
	// addrs by design; the DHT is the source of truth.
	info, err := s.host.FindPeer(ctx, pid)
	if err != nil {
		return fmt.Errorf("locate peer (no addrs in payload, dht lookup failed): %w", err)
	}
	if len(info.Addrs) == 0 {
		return fmt.Errorf("locate peer %s: dht returned empty addrs", pid)
	}
	s.host.Inner().Peerstore().AddAddrs(pid, info.Addrs, peerstoreTTL)
	return nil
}

// parseMultiaddrs converts strings to []multiaddr.Multiaddr. Empty input is
// allowed (caller may have only the peer ID + rely on the DHT).
func parseMultiaddrs(addrs []string) ([]multiaddr.Multiaddr, error) {
	out := make([]multiaddr.Multiaddr, 0, len(addrs))
	for _, s := range addrs {
		ma, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			return nil, fmt.Errorf("parse multiaddr %q: %w", s, err)
		}
		out = append(out, ma)
	}
	return out, nil
}
