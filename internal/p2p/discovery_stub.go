//go:build !p2p

package p2p

import (
	"context"
	"errors"
	"log/slog"
)

// PeerLookup is satisfied identically in both build modes.
type PeerLookup interface {
	IsPaired(ctx context.Context, peerID string) (bool, error)
	MarkConnectionMode(ctx context.Context, peerID string, mode ConnectionMode)
	RememberPeerAddrs(ctx context.Context, peerID string, addrs []string)
	LoadPeerAddrs(ctx context.Context, peerID string) []string
}

// DiscoveryService stub: does nothing in builds without `-tags p2p`.
type DiscoveryService struct{}

// NewDiscoveryService returns a no-op service so call sites in the daemon's
// wiring code compile in slim builds. h is always nil here.
func NewDiscoveryService(_ *Host, _ PeerLookup, _ *slog.Logger) *DiscoveryService {
	return &DiscoveryService{}
}

// Close is a no-op.
func (d *DiscoveryService) Close() error { return nil }

// ModeFor always returns "" in stub builds.
func (d *DiscoveryService) ModeFor(_ string) ConnectionMode { return "" }

// LastSeenAddrs is a no-op in stub mode.
func (h *Host) LastSeenAddrs(_ string) []string { return nil }

// Discovery is always nil in stub builds.
func (h *Host) Discovery() *DiscoveryService { return nil }

// ErrTableMissing matches the production sentinel so callers can switch
// against it in either build mode.
var ErrTableMissing = errors.New("p2p: p2p_peers table missing (M1.2 not merged)")

// IsTableMissing is a stub helper that always returns false in slim builds.
func IsTableMissing(_ error) bool { return false }
