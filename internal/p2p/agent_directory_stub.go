//go:build !p2p

package p2p

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Stub types + constructors for the agent-directory protocol so the
// daemon wiring in cmd/mcplexer compiles in slim builds. Everything is
// inert: the agent-directory only does anything when libp2p is built
// in (the protocol needs streams).

// AgentRecord stub matches the wire shape used in p2p builds. Defined
// here so adapter code in main package compiles either way.
type AgentRecord struct {
	SessionID  string
	Name       string
	Role       string
	ClientType string
	LastSeenAt time.Time
}

// LocalAgentSource stub interface — same shape as the p2p build.
type LocalAgentSource interface {
	ListLocalAgents(ctx context.Context, peerID string) ([]AgentRecord, error)
}

// PeerWorkspaceLookup stub interface — same shape as the p2p build.
type PeerWorkspaceLookup interface {
	ListLocalWorkspaceIDsForPeer(ctx context.Context, peerID string) ([]string, error)
}

// RemoteAgentSink stub interface.
type RemoteAgentSink interface {
	ApplyRemoteSnapshot(ctx context.Context, fromPeerID string, agents []AgentRecord) error
	ApplyRemoteDelta(ctx context.Context, fromPeerID string, added []AgentRecord, removed []string) error
	HandleRemoteBye(ctx context.Context, fromPeerID string) error
}

// AgentDirectoryAuditor stub interface.
type AgentDirectoryAuditor interface {
	RecordAgentDirectory(ctx context.Context, action, peerID, status, errMsg string)
}

// AgentDirectoryService stub: does nothing in builds without `-tags p2p`.
type AgentDirectoryService struct{}

// NewAgentDirectoryService returns a no-op service so call sites in the
// daemon's wiring compile in slim builds.
func NewAgentDirectoryService(
	_ *Host, _ PeerPairChecker, _ LocalAgentSource, _ RemoteAgentSink, _ PeerWorkspaceLookup, _ AgentDirectoryAuditor, _ *slog.Logger,
) *AgentDirectoryService {
	return &AgentDirectoryService{}
}

// Stop is a no-op.
func (s *AgentDirectoryService) Stop() {}

// ConnectToPeer is a no-op in stub mode (always returns nil).
func (s *AgentDirectoryService) ConnectToPeer(_ context.Context, _ string) error { return nil }

// BroadcastDelta is a no-op in stub mode.
func (s *AgentDirectoryService) BroadcastDelta(_ context.Context, _ []AgentRecord, _ []string) {}

// NewAgentDirectorySource returns a stub LocalAgentSource that emits
// nothing — slim builds don't ship local-agent state to peers.
func NewAgentDirectorySource(_ store.MeshStore) LocalAgentSource {
	return stubAgentDirectorySource{}
}

type stubAgentDirectorySource struct{}

func (stubAgentDirectorySource) ListLocalAgents(_ context.Context, _ string) ([]AgentRecord, error) {
	return nil, nil
}

// NewAgentDirectorySink returns a stub RemoteAgentSink that drops every
// inbound frame on the floor — slim builds have no libp2p, so no
// remote frames ever arrive anyway.
func NewAgentDirectorySink(_ store.MeshStore) RemoteAgentSink {
	return stubAgentDirectorySink{}
}

type stubAgentDirectorySink struct{}

func (stubAgentDirectorySink) ApplyRemoteSnapshot(_ context.Context, _ string, _ []AgentRecord) error {
	return nil
}
func (stubAgentDirectorySink) ApplyRemoteDelta(_ context.Context, _ string, _ []AgentRecord, _ []string) error {
	return nil
}
func (stubAgentDirectorySink) HandleRemoteBye(_ context.Context, _ string) error { return nil }

// PeerPairChecker stub interface — production version lives in the
// p2p-tagged build alongside the rest of the protocol code.
type PeerPairChecker interface {
	IsPaired(ctx context.Context, peerID string) (bool, error)
}
