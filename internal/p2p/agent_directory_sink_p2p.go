//go:build p2p

package p2p

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// agentDirectorySink implements RemoteAgentSink by writing peer-origin
// rows into mesh_agents with the spec-mandated namespacing:
//
//	origin     = "peer:<fromPeerID>"
//	session_id = "peer:<fromPeerID>:<original_session_id>"
//
// Namespacing avoids collision with local sessions and lets the same
// remote peer send the same per-session id without overwriting other
// peers' rows. Conversion from the wire AgentRecord happens here.
// AgentSinkStore is the write surface the sink needs: the mesh-agent
// mutations plus the workspace binding lookup used to (a) confirm the
// remote workspace is one we actually paired and (b) map it onto the
// local workspace so peer agents render under the shared workspace.
type AgentSinkStore interface {
	store.MeshStore
	GetWorkspacePeerBinding(ctx context.Context, peerID, remoteWorkspaceID string) (*store.WorkspacePeerBinding, error)
}

type agentDirectorySink struct {
	store AgentSinkStore
}

// NewAgentDirectorySink wraps a store as a RemoteAgentSink.
func NewAgentDirectorySink(s AgentSinkStore) RemoteAgentSink {
	return &agentDirectorySink{store: s}
}

// ApplyRemoteSnapshot replaces every row tagged origin = "peer:<fromPeerID>"
// with the snapshot's contents. Per spec, snapshots are authoritative
// for the sender's namespace — anything not in the snapshot is dropped.
func (s *agentDirectorySink) ApplyRemoteSnapshot(
	ctx context.Context, fromPeerID string, agents []AgentRecord,
) error {
	origin := store.MeshAgentOriginPeerPrefix + fromPeerID
	if _, err := s.store.DeleteMeshAgentsByOrigin(ctx, origin); err != nil {
		return fmt.Errorf("delete by origin: %w", err)
	}
	for _, a := range agents {
		if err := s.upsert(ctx, fromPeerID, a); err != nil {
			return fmt.Errorf("upsert remote agent %s: %w", a.SessionID, err)
		}
	}
	return nil
}

// ApplyRemoteDelta upserts added agents and deletes removed session IDs
// (after namespacing). Both operations are scoped to the sender's
// origin tag so a delta can never affect another peer's rows.
func (s *agentDirectorySink) ApplyRemoteDelta(
	ctx context.Context, fromPeerID string, added []AgentRecord, removed []string,
) error {
	for _, a := range added {
		if err := s.upsert(ctx, fromPeerID, a); err != nil {
			return fmt.Errorf("upsert remote agent %s: %w", a.SessionID, err)
		}
	}
	for _, sid := range removed {
		namespaced := namespacedSessionID(fromPeerID, sid)
		if err := s.store.DeleteMeshAgent(ctx, namespaced); err != nil {
			// Best-effort: a delta to remove a row we never had isn't
			// fatal. Log via the audit path on the protocol side.
			continue
		}
	}
	return nil
}

// HandleRemoteBye drops every row tagged origin = "peer:<fromPeerID>".
// Same effect as an empty snapshot but cheaper.
func (s *agentDirectorySink) HandleRemoteBye(ctx context.Context, fromPeerID string) error {
	origin := store.MeshAgentOriginPeerPrefix + fromPeerID
	if _, err := s.store.DeleteMeshAgentsByOrigin(ctx, origin); err != nil {
		return fmt.Errorf("delete by origin on bye: %w", err)
	}
	return nil
}

// upsert applies the namespacing + origin tagging and writes the row.
// The remote workspace_id is mapped through the workspace_peer_binding to
// the LOCAL workspace it was paired with; if no binding exists the row is
// dropped (a peer can only populate agents for a workspace we paired with
// it — defense-in-depth on top of the sender's outbound scoping).
func (s *agentDirectorySink) upsert(
	ctx context.Context, fromPeerID string, a AgentRecord,
) error {
	binding, err := s.store.GetWorkspacePeerBinding(ctx, fromPeerID, a.WorkspaceID)
	if err != nil || binding == nil {
		// Unbound workspace → drop silently. Not an error: a peer running
		// an older (unscoped) build may send agents for workspaces we
		// never paired; we simply refuse to store them.
		return nil
	}
	row := &store.MeshAgent{
		SessionID:   namespacedSessionID(fromPeerID, a.SessionID),
		WorkspaceID: binding.LocalWorkspaceID,
		Name:        a.Name,
		Role:        a.Role,
		ClientType:  a.ClientType,
		Origin:      store.MeshAgentOriginPeerPrefix + fromPeerID,
		Status:      a.Status,
		TmuxSession: a.TmuxSession,
		TmuxWindow:  a.TmuxWindow,
		TmuxPane:    a.TmuxPane,
		LastSeenAt:  a.LastSeenAt,
		CreatedAt:   time.Now().UTC(),
	}
	return s.store.UpsertMeshAgent(ctx, row)
}

// namespacedSessionID prefixes the remote session_id with the peer-origin
// tag so it can never collide with a local session_id and so cleanup is
// scoped per-peer.
func namespacedSessionID(fromPeerID, remoteSessionID string) string {
	// Sanity: never accept a foreign-namespaced id (defends against a
	// malicious peer trying to nuke another peer's namespace).
	clean := remoteSessionID
	if idx := strings.LastIndex(clean, ":"); idx >= 0 && strings.HasPrefix(clean, "peer:") {
		clean = clean[idx+1:]
	}
	return fmt.Sprintf("peer:%s:%s", fromPeerID, clean)
}
