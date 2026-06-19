//go:build !p2p

package p2p

import (
	"context"
	"errors"
	"log/slog"
)

// MaxSkillBundleBytes mirrors the p2p-build constant so callers compile in
// stub mode. The cap matches skills.MaxBundleSize from M2.2.
const MaxSkillBundleBytes int64 = 100 * 1024 * 1024

// SkillShareErrors that the gateway tooling pattern-matches must exist in
// both build modes so callers can use errors.Is without a build-tag fence.
var (
	ErrPeerNotPaired       = errors.New("p2p: peer not paired")
	ErrSkillNotInstalled   = errors.New("p2p: skill not installed on peer")
	ErrSkillBundleTooLarge = errors.New("p2p: skill bundle exceeds size cap")
	ErrSkillShareDenied    = errors.New("p2p: skill share denied")
)

// SkillOffer mirrors the p2p-build shape so import sites compile.
type SkillOffer struct {
	Type         string `json:"type"`
	Name         string `json:"name"`
	Version      string `json:"version"`
	SignerPubkey string `json:"signer_pubkey"`
	ManifestJSON []byte `json:"manifest_json"`
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"size_bytes"`
}

// SkillRequest mirrors the p2p-build shape so import sites compile.
type SkillRequest struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version string `json:"version"`
}

// PairedPeer mirrors the p2p-build shape so import sites compile.
type PairedPeer struct {
	PeerID  string
	Scopes  []string
	Revoked bool
}

// PairedPeerLookup interface declared in stub mode too — same signature as
// the p2p-build version so the wiring code compiles in both modes. Distinct
// from the discovery PeerLookup; the latter only answers IsPaired.
type PairedPeerLookup interface {
	GetPairedPeer(ctx context.Context, peerID string) (PairedPeer, error)
}

// SkillProvider declared here for the same reason as PeerLookup.
type SkillProvider interface {
	GetSkillBundle(ctx context.Context, name, version string) ([]byte, []byte, error)
	GetInstalledOffer(ctx context.Context, name string) (*SkillOffer, error)
}

// SkillReceiver declared in stub mode for binary compatibility.
type SkillReceiver interface {
	HandleIncomingBundle(
		ctx context.Context, peerID string, bundle, sig []byte,
	) error
}

// SkillShareAuditor declared in stub mode for binary compatibility.
type SkillShareAuditor interface {
	RecordSkillShare(
		ctx context.Context, action, peerID, skill, status, errMsg string,
	)
}

// SkillShareService is a stub when the binary is built without `-tags p2p`.
// All operations return ErrP2PNotBuiltIn so callers can branch on the
// sentinel; the gateway tools translate this into a "p2p not enabled" reply.
type SkillShareService struct{}

// NewSkillShareService returns a non-nil stub so route registration in the
// gateway works in both build modes (mirrors NewPairingService).
func NewSkillShareService(
	_ *Host, _ PairedPeerLookup, _ SkillProvider,
	_ SkillReceiver, _ SkillShareAuditor, _ *slog.Logger,
) *SkillShareService {
	return &SkillShareService{}
}

// OfferSkill returns ErrP2PNotBuiltIn — skill share requires the p2p build tag.
func (s *SkillShareService) OfferSkill(_ context.Context, _, _ string) error {
	return ErrP2PNotBuiltIn
}

// RequestSkill returns ErrP2PNotBuiltIn — skill share requires the p2p build tag.
func (s *SkillShareService) RequestSkill(
	_ context.Context, _, _, _ string,
) (*SkillOffer, error) {
	return nil, ErrP2PNotBuiltIn
}

// LastOfferFor always returns (zero, false) in stub mode — no offers can ever
// have been received.
func (s *SkillShareService) LastOfferFor(_, _ string) (SkillOffer, bool) {
	return SkillOffer{}, false
}
