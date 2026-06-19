//go:build p2p

package p2p

import (
	"context"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// hydratePeerstore loads the most recently observed addrs for every paired
// peer and pushes them into the libp2p peerstore. Called once at startup
// from NewDiscoveryService; idempotent. Best-effort — per-peer failures are
// logged and skipped.
//
// This is the hot-start half of M1.5: combined with the DiscoveryService's
// persistKnownAddrs hook (called on every non-relay connection), the
// reconnector sees a populated peerstore on the very first iteration after
// a daemon restart instead of waiting up to one full DHT walk.
func (d *DiscoveryService) hydratePeerstore() {
	if d.lookup == nil || d.host == nil {
		return
	}
	lister, ok := d.lookup.(PairedPeerLister)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ids, err := lister.ListPeerIDs(ctx)
	if err != nil {
		d.logger.Debug("p2p discovery: hydrate list peers failed", "err", err)
		return
	}
	for _, idStr := range ids {
		d.hydrateOne(ctx, idStr)
	}
}

// hydrateOne loads + parses + adds addrs for a single paired peer.
// Per-addr parse failures are logged and skipped (don't block the rest of
// the list). A peer with zero loaded addrs is a silent no-op.
func (d *DiscoveryService) hydrateOne(ctx context.Context, idStr string) {
	pid, err := peer.Decode(idStr)
	if err != nil {
		return
	}
	raw := d.lookup.LoadPeerAddrs(ctx, idStr)
	if len(raw) == 0 {
		return
	}
	mas := make([]multiaddr.Multiaddr, 0, len(raw))
	for _, s := range raw {
		ma, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			d.logger.Debug("p2p discovery: hydrate parse addr",
				"peer", idStr, "addr", s, "err", err)
			continue
		}
		mas = append(mas, ma)
	}
	if len(mas) == 0 {
		return
	}
	d.host.h.Peerstore().AddAddrs(pid, mas, peerstoreTTL)
	d.logger.Debug("p2p discovery: hydrated peerstore",
		"peer", idStr, "count", len(mas))
}

// persistKnownAddrs snapshots the peer's libp2p peerstore addrs (filtered
// to dialable, non-relay entries) and hands them to the lookup for storage.
// Called from handleConnected on every non-relay transition; the next
// daemon boot's hydratePeerstore reads them back.
func (d *DiscoveryService) persistKnownAddrs(p peer.ID) {
	if d.lookup == nil || d.host == nil {
		return
	}
	raw := multiaddrsAsStrings(d.host.h.Peerstore().Addrs(p))
	addrs := dialableAddrsForPairing(raw)
	if len(addrs) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	d.lookup.RememberPeerAddrs(ctx, p.String(), addrs)
}
