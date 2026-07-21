package mesh

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// agentDirectoryActiveWindow caps how recently an agent must have touched
// the mesh to be considered routable via to_agent. Mirrors the window used
// by ListActiveMeshAgents callers; tight enough that stale rows don't
// silently swallow messages, loose enough to survive normal idle gaps.
const agentDirectoryActiveWindow = 30 * time.Minute

// ResolveAgentName looks up an active agent by friendly Name. Returns
// store.ErrNotFound semantics via a wrapped error when the name doesn't
// match any active agent on this mesh. Ambiguous names (multiple active
// agents share a Name) also error so the caller picks via session_id.
func (m *Manager) ResolveAgentName(ctx context.Context, name string) (*store.MeshAgent, error) {
	return m.ResolveAgentNameInWorkspaces(ctx, name, nil)
}

// ResolveAgentNameInWorkspaces is the scoped form used by gateway callers.
// It only considers agents active in the caller's readable workspace set.
func (m *Manager) ResolveAgentNameInWorkspaces(ctx context.Context, name string, workspaceIDs []string) (*store.MeshAgent, error) {
	want := strings.TrimSpace(name)
	if want == "" {
		return nil, fmt.Errorf("to_agent: name is required")
	}
	since := time.Now().UTC().Add(-agentDirectoryActiveWindow)
	agents, err := m.activeAgentsForWorkspaces(ctx, workspaceIDs, since)
	if err != nil {
		return nil, fmt.Errorf("list active agents: %w", err)
	}
	var matches []store.MeshAgent
	for _, a := range agents {
		if strings.EqualFold(strings.TrimSpace(a.Name), want) {
			matches = append(matches, a)
		}
	}
	if len(matches) == 0 {
		return nil, fmt.Errorf("to_agent %q does not match any active agent — call mesh__list_agents to see known names", name)
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("to_agent %q is ambiguous (%d active agents share that name) — pass audience=<session_id> or rename one", name, len(matches))
	}
	out := matches[0]
	return &out, nil
}

// agentRemotePeerID returns the libp2p peer ID embedded in a remote-origin
// agent row, or empty if the agent is local. Format mirrors
// store.MeshAgentOriginPeerPrefix ("peer:<peer_id>").
func agentRemotePeerID(agent *store.MeshAgent) string {
	if agent == nil {
		return ""
	}
	if !strings.HasPrefix(agent.Origin, store.MeshAgentOriginPeerPrefix) {
		return ""
	}
	return strings.TrimPrefix(agent.Origin, store.MeshAgentOriginPeerPrefix)
}

// ListAgents returns every active agent (local + peer-origin) in the
// directory. Used by the mesh__list_agents MCP tool. Filters by the same
// active window as ResolveAgentName so the listing matches what to_agent
// can route to.
func (m *Manager) ListAgents(ctx context.Context) ([]store.MeshAgent, error) {
	return m.ListAgentsInWorkspaces(ctx, nil)
}

// ListAgentsInWorkspaces returns active agents visible to a session bound to
// the supplied workspace IDs. Empty workspaceIDs preserves the historical
// all-workspaces manager behavior for internal callers.
func (m *Manager) ListAgentsInWorkspaces(ctx context.Context, workspaceIDs []string) ([]store.MeshAgent, error) {
	since := time.Now().UTC().Add(-agentDirectoryActiveWindow)
	return m.activeAgentsForWorkspaces(ctx, workspaceIDs, since)
}

func (m *Manager) activeAgentsForWorkspaces(ctx context.Context, workspaceIDs []string, since time.Time) ([]store.MeshAgent, error) {
	if len(workspaceIDs) == 0 {
		return m.store.ListActiveMeshAgents(ctx, "", since)
	}
	seen := make(map[string]store.MeshAgent)
	for _, wsID := range workspaceIDs {
		wsID = strings.TrimSpace(wsID)
		if wsID == "" {
			continue
		}
		rows, err := m.store.ListActiveMeshAgents(ctx, wsID, since)
		if err != nil {
			return nil, err
		}
		for _, a := range rows {
			key := a.SessionID
			if key == "" {
				key = a.Origin + ":" + a.Name + ":" + a.WorkspaceID
			}
			prev, ok := seen[key]
			if !ok || a.LastSeenAt.After(prev.LastSeenAt) {
				seen[key] = a
			}
		}
	}
	out := make([]store.MeshAgent, 0, len(seen))
	for _, a := range seen {
		out = append(out, a)
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].LastSeenAt.After(out[j].LastSeenAt)
	})
	return out, nil
}
