//go:build p2p

package p2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p-kad-dht/amino"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// dhtFindTimeout caps a single FindPeer query. Walking the DHT can take a
// while; we don't want a single stuck query to block the reconnector loop.
const dhtFindTimeout = 30 * time.Second

// wireDHT constructs a kad-dht in AutoServer mode so the host accepts DHT
// queries when reachable (helping the global routing table) and falls back to
// client mode when behind a NAT. The DHT bootstraps against bootstrapAddrs;
// callers must call Bootstrap on the returned DHT to start the periodic
// refresh loop.
func wireDHT(
	ctx context.Context,
	h host.Host,
	bootstrapAddrs []peer.AddrInfo,
	logger *slog.Logger,
) (*dht.IpfsDHT, error) {
	if h == nil {
		return nil, errors.New("p2p dht: nil host")
	}
	kdht, err := dht.New(ctx, h,
		dht.Mode(dht.ModeAutoServer),
		dht.BootstrapPeers(bootstrapAddrs...),
		dht.RoutingTablePeerDiversityFilter(dht.NewRTPeerDiversityFilter(
			h,
			amino.DefaultMaxPeersPerIPGroupPerCpl,
			amino.DefaultMaxPeersPerIPGroup,
		)),
	)
	if err != nil {
		return nil, fmt.Errorf("p2p dht: new: %w", err)
	}
	if err := kdht.Bootstrap(ctx); err != nil {
		_ = kdht.Close()
		return nil, fmt.Errorf("p2p dht: bootstrap: %w", err)
	}

	// Kick a goroutine that dials the bootstrap peers once. The DHT joins the
	// network as those connections complete; without this we'd wait until the
	// first organic connection.
	go connectBootstraps(ctx, h, bootstrapAddrs, logger)
	return kdht, nil
}

// connectBootstraps dials each bootstrap peer with a short per-peer timeout.
// Failures are logged at debug; one reachable bootstrap is enough to seed the
// DHT routing table.
func connectBootstraps(
	ctx context.Context, h host.Host,
	addrs []peer.AddrInfo, logger *slog.Logger,
) {
	if logger == nil {
		logger = slog.Default()
	}
	var wg sync.WaitGroup
	for _, a := range addrs {
		wg.Add(1)
		go func(a peer.AddrInfo) {
			defer wg.Done()
			dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			if err := h.Connect(dialCtx, a); err != nil {
				logger.Debug("p2p dht: bootstrap dial failed",
					"peer", a.ID, "err", err)
				return
			}
			logger.Debug("p2p dht: bootstrap connected", "peer", a.ID)
		}(a)
	}
	wg.Wait()
}

// FindPeer asks the DHT for the current AddrInfo of peerID. Returns
// ErrPeerNotFoundInDHT when no record can be located within dhtFindTimeout.
func (h *Host) FindPeer(ctx context.Context, peerID peer.ID) (peer.AddrInfo, error) {
	if h == nil || h.dht == nil {
		return peer.AddrInfo{}, ErrDHTUnavailable
	}
	queryCtx, cancel := context.WithTimeout(ctx, dhtFindTimeout)
	defer cancel()
	info, err := h.dht.FindPeer(queryCtx, peerID)
	if err != nil {
		return peer.AddrInfo{}, fmt.Errorf("p2p dht: find peer %s: %w", peerID, err)
	}
	if len(info.Addrs) == 0 {
		return info, ErrPeerNotFoundInDHT
	}
	return info, nil
}
