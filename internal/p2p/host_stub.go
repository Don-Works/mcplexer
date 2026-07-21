//go:build !p2p

package p2p

import (
	"context"
	"log/slog"
)

// Host is a stub when the binary is built without the `p2p` build tag. All
// operations return ErrP2PNotBuiltIn (or a no-op for Close). This lets call
// sites in the daemon compile and run identically in both build modes; only
// the actual p2p functionality is unavailable.
type Host struct{}

// NewHost in stub mode returns (nil, nil) when cfg.Enabled is false (so the
// daemon's behaviour is unchanged) and (nil, ErrP2PNotBuiltIn) when p2p was
// requested at runtime but the binary wasn't built with `-tags p2p`. The
// encryptor argument matches the production signature so call sites compile
// in both modes.
func NewHost(_ context.Context, cfg Config, _ Encryptor, _ *slog.Logger) (*Host, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	return nil, ErrP2PNotBuiltIn
}

// PeerID returns "" — no identity exists in stub mode.
func (h *Host) PeerID() string { return "" }

// Addrs returns nil — no listeners exist in stub mode.
func (h *Host) Addrs() []string { return nil }

// PeerModes returns nil in stub mode — no live peers exist.
func (h *Host) PeerModes() []PeerMode { return nil }

// ConnectString returns ErrP2PNotBuiltIn — no host exists in stub mode.
func (h *Host) ConnectString(_ context.Context, _ string) (string, error) {
	return "", ErrP2PNotBuiltIn
}

// Close is a no-op in stub mode (NewHost never returns a non-nil Host when
// p2p is requested, so this only ever runs against a nil receiver).
func (h *Host) Close() error { return nil }
