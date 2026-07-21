//go:build !p2p

package p2p

import (
	"context"
	"errors"
	"log/slog"
)

// RegistryShareProtocol is exported in the stub so non-p2p builds can
// reference the name without compiling the libp2p machinery.
const RegistryShareProtocol = "/mcplexer/skill-registry/1.0.0"

// ErrRegistryEntryNotFound mirrors the typed error from the p2p
// implementation so callers can errors.Is against it identically.
var ErrRegistryEntryNotFound = errors.New("p2p: registry entry not found")

// RegistryRequest is the wire shape for a registry pull. Defined in
// the stub so REST handlers can construct it without the p2p tag.
type RegistryRequest struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	Version int    `json:"version"`
}

// RegistryProvider is the responder-side hook. Defined here so the
// gateway can compile its adapter struct without the p2p tag.
type RegistryProvider interface {
	GetRegistryEntry(
		ctx context.Context, name string, version int,
	) (body string, bundle []byte, sha256 string, err error)
}

// RegistryReceiver is the requester-side hook. Defined here for the
// same reason as RegistryProvider above.
type RegistryReceiver interface {
	HandleIncomingRegistryEntry(
		ctx context.Context, peerID, name, body string, bundle []byte,
	) error
}

// RegistryShareService is a no-op in non-p2p builds. Constructing it
// is safe; calling RequestRegistrySkill returns ErrFeatureUnavailable.
type RegistryShareService struct{}

// NewRegistryShareService returns a non-nil stub so route registration
// in the gateway works in both build modes (mirrors NewSkillShareService).
func NewRegistryShareService(
	_ any, _ PairedPeerLookup,
	_ RegistryProvider, _ RegistryReceiver,
	_ HubIndexProvider, _ HubSearchProvider,
	_ SkillShareAuditor, _ *slog.Logger,
) *RegistryShareService {
	return &RegistryShareService{}
}

// RequestRegistrySkill is a no-op in non-p2p builds.
func (s *RegistryShareService) RequestRegistrySkill(
	_ context.Context, _, _ string, _ int,
) (string, error) {
	return "", errors.New("registry share requires the p2p build tag")
}

// RequestHubIndex is a no-op in non-p2p builds.
func (s *RegistryShareService) RequestHubIndex(
	_ context.Context, _ string,
) ([]HubIndexEntry, error) {
	return nil, errors.New("registry hub index requires the p2p build tag")
}

// RequestHubSearch is a no-op in non-p2p builds.
func (s *RegistryShareService) RequestHubSearch(
	_ context.Context, _, _ string, _ int,
) ([]HubSearchHit, error) {
	return nil, errors.New("registry hub search requires the p2p build tag")
}
