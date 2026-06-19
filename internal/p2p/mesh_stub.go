//go:build !p2p

package p2p

import (
	"context"
	"log/slog"
)

// MeshAuditor mirrors the production interface so call sites compile in
// slim builds.
type MeshAuditor interface {
	Record(ctx context.Context, kind, peerID, reason, envID string)
}

// MeshPeerLookup mirrors the production interface so call sites compile in
// slim builds.
type MeshPeerLookup interface {
	IsPaired(ctx context.Context, peerID string) (bool, error)
	ListPeerIDs(ctx context.Context) ([]string, error)
}

// MeshWorkspaceLookup mirrors the production interface so call sites compile
// in slim builds.
type MeshWorkspaceLookup interface {
	ListLocalWorkspaceIDsForPeer(ctx context.Context, peerID string) ([]string, error)
}

// MeshTransport is a no-op transport in slim builds. Every Send call
// returns ErrP2PNotBuiltIn; Subscribe returns a closed channel.
type MeshTransport struct{}

// NewMeshTransport returns a stub transport.
func NewMeshTransport(_ *Host, _ MeshPeerLookup, _ MeshAuditor, _ *slog.Logger) *MeshTransport {
	return &MeshTransport{}
}

// Start is a no-op — there is no protocol handler to mount.
func (*MeshTransport) Start() {}

// SetWorkspaceLookup is a no-op in slim builds.
func (*MeshTransport) SetWorkspaceLookup(_ MeshWorkspaceLookup) {}

// Subscribe returns a pre-closed channel; consumers receive the zero value
// once and then exit their range loop.
func (*MeshTransport) Subscribe() <-chan MeshEnvelope {
	ch := make(chan MeshEnvelope)
	close(ch)
	return ch
}

// SendToPeer always returns ErrP2PNotBuiltIn.
func (*MeshTransport) SendToPeer(_ context.Context, _ string, _ *MeshEnvelope) error {
	return ErrP2PNotBuiltIn
}

// SendBroadcast always returns ErrP2PNotBuiltIn.
func (*MeshTransport) SendBroadcast(_ context.Context, _ *MeshEnvelope) (int, error) {
	return 0, ErrP2PNotBuiltIn
}

// Close is a no-op in stub mode.
func (*MeshTransport) Close() error { return nil }
