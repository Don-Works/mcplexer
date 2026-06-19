//go:build !p2p

package p2p

import (
	"context"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Reconnector stub — no-op when the binary is built without `-tags p2p`.
// Constructing one is allowed so call sites in the daemon don't have to gate
// on build tags.
type Reconnector struct{}

// PairedPeerLister stub — present only so cmd-side code compiles in both
// build modes. The stub Reconnector ignores it.
type PairedPeerLister interface {
	ListPeerIDs(ctx context.Context) ([]string, error)
}

// ReconnectStatus stub mirror of the p2p-build type. Always zero-value in
// stub mode. The fields stay JSON-tagged so the API encoder produces the
// same shape (just empty strings) regardless of build tag.
type ReconnectStatus struct {
	LastAttempt time.Time `json:"last_dial_attempt_at,omitempty"`
	LastError   string    `json:"last_dial_error,omitempty"`
	State       string    `json:"reconnect_state,omitempty"`
}

// NewReconnector in stub mode always returns nil — there's no live host to
// reconnect through. Call sites must tolerate a nil *Reconnector (Start/Close
// are nil-safe in this build).
func NewReconnector(_ *Host, _ PairedPeerLister, _ time.Duration, _ *slog.Logger) *Reconnector {
	return nil
}

// Start is a no-op in stub mode.
func (r *Reconnector) Start(_ context.Context) {}

// Close is a no-op in stub mode.
func (r *Reconnector) Close() {}

// Kick is a no-op in stub mode.
func (r *Reconnector) Kick(_ peer.ID) {}

// MarkConnected is a no-op in stub mode — without libp2p there's no
// reconnect_state to refresh.
func (r *Reconnector) MarkConnected(_ peer.ID) {}

// LivenessOracle stub — present so cmd-side code compiles in both build
// modes. The stub Reconnector ignores any oracle passed in.
type LivenessOracle interface {
	OfflineSince(p peer.ID) (time.Time, bool)
}

// SetLivenessOracle is a no-op in stub mode.
func (r *Reconnector) SetLivenessOracle(_ LivenessOracle) {}

// PeerStatusByID returns the zero ReconnectStatus in stub mode.
func (r *Reconnector) PeerStatusByID(_ string) ReconnectStatus { return ReconnectStatus{} }

// AllPeerStatus returns an empty map in stub mode.
func (r *Reconnector) AllPeerStatus() map[string]ReconnectStatus {
	return map[string]ReconnectStatus{}
}

// AddOnlineObserver is a no-op in stub mode — without libp2p there's no
// reconnect signal to observe. The mesh outbound queue still works via
// its 30s sweep; it just won't react instantly to peer reconnects.
func (r *Reconnector) AddOnlineObserver(_ func(peerID string)) {}
