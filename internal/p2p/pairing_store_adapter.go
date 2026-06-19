package p2p

import (
	"context"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// StorePairingAdapter bridges the PairingStore interface used by
// PairingService to the broader store.P2PPeerStore. Available in both build
// modes so wiring code in the daemon compiles regardless of -tags p2p.
type StorePairingAdapter struct {
	S store.P2PPeerStore
}

// CreatePendingPair persists a pending pair via the underlying store.
func (a StorePairingAdapter) CreatePendingPair(
	ctx context.Context, code, peerID string, addrs []string, expiresAt time.Time,
) error {
	return a.S.CreatePendingPair(ctx, &store.P2PPendingPair{
		Code:       code,
		PeerID:     peerID,
		Multiaddrs: addrs,
		CreatedAt:  time.Now().UTC(),
		ExpiresAt:  expiresAt,
	})
}

// GetPendingPair fetches a pending pair by code, returning fields directly
// to match the PairingStore signature.
func (a StorePairingAdapter) GetPendingPair(
	ctx context.Context, code string,
) (string, []string, time.Time, error) {
	p, err := a.S.GetPendingPair(ctx, code)
	if err != nil {
		return "", nil, time.Time{}, err
	}
	return p.PeerID, p.Multiaddrs, p.ExpiresAt, nil
}

// DeletePendingPair removes a pending pair (consumed or expired).
func (a StorePairingAdapter) DeletePendingPair(
	ctx context.Context, code string,
) error {
	return a.S.DeletePendingPair(ctx, code)
}
