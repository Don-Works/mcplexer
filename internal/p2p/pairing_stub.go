//go:build !p2p

package p2p

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// PairingResult mirrors the p2p-build shape so the API layer compiles in
// stub builds. Fields are zero values — the stub never returns a populated
// instance.
type PairingResult struct {
	Code      string    `json:"code"`
	QRPayload string    `json:"qr_payload"`
	ExpiresAt time.Time `json:"expires_at"`
}

// PairingStore is the storage interface used by PairingService in p2p builds.
// Declared here too so import sites compile in both modes.
type PairingStore interface {
	CreatePendingPair(ctx context.Context, code, peerID string, addrs []string, expiresAt time.Time) error
	GetPendingPair(ctx context.Context, code string) (peerID string, addrs []string, expiresAt time.Time, err error)
	DeletePendingPair(ctx context.Context, code string) error
}

// PairingService is a stub when the binary is built without `-tags p2p`. All
// operations return ErrP2PNotBuiltIn so callers can branch on the sentinel.
type PairingService struct{}

// NewPairingService returns a non-nil service whose methods all return
// ErrP2PNotBuiltIn. We deliberately don't return nil: the daemon wires the
// service into the API router unconditionally, and a non-nil pointer keeps
// the route registered (returning a useful 501) instead of falling through
// to the SPA HTML.
func NewPairingService(_ *Host, _ PairingStore) *PairingService {
	return &PairingService{}
}

// StartPair returns ErrP2PNotBuiltIn — pairing is not available without the
// p2p build tag.
func (s *PairingService) StartPair(_ context.Context) (*PairingResult, error) {
	return nil, ErrP2PNotBuiltIn
}

// CompletePair returns ErrP2PNotBuiltIn — pairing is not available without
// the p2p build tag.
func (s *PairingService) CompletePair(
	_ context.Context, _ string, _ string, _ []string,
) error {
	return ErrP2PNotBuiltIn
}

// PeerPersister is the subset of store.P2PPeerStore the responder side of
// a pairing handshake uses to record the initiator. Declared here so the
// daemon's wiring code compiles in stub builds.
type PeerPersister interface {
	AddPeer(ctx context.Context, p *store.P2PPeer) error
	UpdateLastSeen(ctx context.Context, peerID string, t time.Time) error
}

// SetPeerPersister is a no-op in stub builds.
func (s *PairingService) SetPeerPersister(_ PeerPersister) {}

// SetLogger is a no-op in stub builds.
func (s *PairingService) SetLogger(_ *slog.Logger) {}

// UserLinker mirrors the p2p-build interface so daemon wiring compiles
// without -tags p2p.
type UserLinker interface {
	UpsertUser(ctx context.Context, userID, displayName string) error
	LinkPeerToUser(ctx context.Context, peerID, userID string) error
}

// SetSelfIdentity is a no-op in stub builds.
func (s *PairingService) SetSelfIdentity(_ string, _ string) {}

// SetUserLinker is a no-op in stub builds.
func (s *PairingService) SetUserLinker(_ UserLinker) {}

// SyntheticUserIDForPeer is a no-op stub returning empty string. The HTTP
// handler guards on stub builds and never invokes it.
func SyntheticUserIDForPeer(_ string) string { return "" }

// DisplayNameProvider mirrors the p2p-build type so wiring code compiles.
// Stub never invokes the provider.
type DisplayNameProvider func() string

// SetDisplayNameProvider is a no-op in stub builds.
func (s *PairingService) SetDisplayNameProvider(_ DisplayNameProvider) {}
