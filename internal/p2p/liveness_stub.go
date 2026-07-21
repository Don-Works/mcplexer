//go:build !p2p

package p2p

import (
	"context"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// LivenessMonitor stub — no-op when the binary is built without `-tags p2p`.
// Constructing one is allowed so call sites in the daemon don't have to gate
// on build tags.
type LivenessMonitor struct{}

// PeerLivenessToucher stub — present only so cmd-side code compiles in both
// build modes. The stub LivenessMonitor ignores it.
type PeerLivenessToucher interface {
	UpdateLastSeen(ctx context.Context, peerID string, t time.Time) error
}

// ReconnectMarker stub — same shape as the p2p build. The stub LivenessMonitor
// ignores it; SetReconnectMarker is a no-op.
type ReconnectMarker interface {
	MarkConnected(p peer.ID)
}

// SetReconnectMarker is a no-op in stub mode.
func (m *LivenessMonitor) SetReconnectMarker(_ ReconnectMarker) {}

// LivenessStatus stub mirror of the p2p-build type. Always zero-value in
// stub mode. Field shape stays identical so the API encoder produces the
// same JSON regardless of build tag.
type LivenessStatus struct {
	Online              bool          `json:"online"`
	OnlineSince         time.Time     `json:"online_since,omitempty"`
	OfflineSince        time.Time     `json:"offline_since,omitempty"`
	LastPingAt          time.Time     `json:"last_ping_at,omitempty"`
	LastPingRTT         time.Duration `json:"last_ping_rtt_ns,omitempty"`
	ConsecutiveFailures int           `json:"consecutive_failures,omitempty"`
}

// NewLivenessMonitor in stub mode always returns nil — no live host exists.
// Call sites must tolerate a nil *LivenessMonitor (Start/Close + every
// reader are nil-safe in this build).
func NewLivenessMonitor(_ *Host, _ PairedPeerLister, _ PeerLivenessToucher, _ *slog.Logger) *LivenessMonitor {
	return nil
}

// Start is a no-op in stub mode.
func (m *LivenessMonitor) Start(_ context.Context) {}

// Close is a no-op in stub mode.
func (m *LivenessMonitor) Close() {}

// OfflineSince returns zero time + false in stub mode.
func (m *LivenessMonitor) OfflineSince(_ peer.ID) (time.Time, bool) {
	return time.Time{}, false
}

// PeerLiveness returns the zero value in stub mode.
func (m *LivenessMonitor) PeerLiveness(_ peer.ID) LivenessStatus { return LivenessStatus{} }

// AllLiveness returns an empty map in stub mode.
func (m *LivenessMonitor) AllLiveness() map[string]LivenessStatus {
	return map[string]LivenessStatus{}
}

// IsOnline returns false in stub mode — without libp2p there's no liveness
// signal to consult. Callers should treat the result as "no information"
// and apply their own fallback (e.g. always-true so the dial path runs).
func (m *LivenessMonitor) IsOnline(_ string) bool { return false }
