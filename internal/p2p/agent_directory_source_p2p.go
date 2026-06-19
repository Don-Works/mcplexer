//go:build p2p

package p2p

import (
	"context"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// agentDirectoryActiveWindow caps how recently a local agent must have
// touched the mesh to be included in outbound snapshots. Matches the
// window used by mesh.Manager.ResolveAgentName so what a remote peer
// receives matches what local list_agents returns.
const agentDirectoryActiveWindow = 30 * time.Minute

// MaxLocalAgentsPerSnapshot caps how many agents we ship in a snapshot
// frame. Matches the protocol's MaxAgentsPerSnapshot. When the local
// directory exceeds this, we ship the most recently-seen agents and
// drop the tail (debounced so a hot directory still converges).
const MaxLocalAgentsPerSnapshot = 256

// AgentSourceStore is the read surface the source needs: the workspace
// authorization set for a peer + the workspace-filtered agent query.
type AgentSourceStore interface {
	ListActiveMeshAgentsInWorkspaces(ctx context.Context, wsIDs []string, since time.Time) ([]store.MeshAgent, error)
	ListLocalWorkspaceIDsForPeer(ctx context.Context, peerID string) ([]string, error)
}

// agentDirectorySource implements LocalAgentSource by querying the
// mesh_agents table for origin == "local" rows in the active window,
// SCOPED to the workspaces the target peer is paired with. Conversion
// happens here so the wire layer never sees store types.
type agentDirectorySource struct {
	store AgentSourceStore
}

// NewAgentDirectorySource wraps a store as a LocalAgentSource.
func NewAgentDirectorySource(s AgentSourceStore) LocalAgentSource {
	return &agentDirectorySource{store: s}
}

// ListLocalAgents returns the active local agents the given peer is
// authorized to see — those in the workspaces this peer is paired with.
// A peer with no workspace binding gets an empty snapshot (default-deny).
// Peer-origin rows are excluded by the store query so they can never leak
// back across the wire. Capped at the snapshot limit.
func (s *agentDirectorySource) ListLocalAgents(ctx context.Context, peerID string) ([]AgentRecord, error) {
	wsIDs, err := s.store.ListLocalWorkspaceIDsForPeer(ctx, peerID)
	if err != nil {
		return nil, err
	}
	if len(wsIDs) == 0 {
		// Unbound peer: nothing crosses. This is the workspace-scoped
		// pairing guarantee — pairing alone no longer leaks the directory.
		return nil, nil
	}
	since := time.Now().UTC().Add(-agentDirectoryActiveWindow)
	rows, err := s.store.ListActiveMeshAgentsInWorkspaces(ctx, wsIDs, since)
	if err != nil {
		return nil, err
	}
	out := make([]AgentRecord, 0, len(rows))
	for _, a := range rows {
		out = append(out, agentRecordFromMeshAgent(a))
		if len(out) >= MaxLocalAgentsPerSnapshot {
			break
		}
	}
	return out, nil
}

// agentRecordFromMeshAgent projects the store row onto the protocol's
// wire shape. Status + workspace + tmux locator are all forward-compatible
// add-ons (omitempty on the wire); older receivers drop them silently.
func agentRecordFromMeshAgent(a store.MeshAgent) AgentRecord {
	return AgentRecord{
		SessionID:   a.SessionID,
		Name:        a.Name,
		Role:        a.Role,
		ClientType:  a.ClientType,
		LastSeenAt:  a.LastSeenAt,
		Status:      a.Status,
		WorkspaceID: a.WorkspaceID,
		TmuxSession: a.TmuxSession,
		TmuxWindow:  a.TmuxWindow,
		TmuxPane:    a.TmuxPane,
	}
}
