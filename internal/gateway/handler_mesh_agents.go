package gateway

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// handleMeshListAgents returns the active agent directory — local plus
// every peer-origin agent we've observed via gossip from a paired peer.
// The intent is to make agents first-class addressable entities for
// `to_agent` routing on mesh__send (mirrors mesh__list_peers's role for
// devices).
func (h *handler) handleMeshListAgents(ctx context.Context) ([]byte, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	agents, err := h.mesh.ListAgentsInWorkspaces(ctx, h.sessionMeshMeta(ctx).WorkspaceIDs)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	return marshalToolResult(formatAgentDirectory(agents)), nil
}

// formatAgentDirectory renders the active-agents listing. Splits local from
// remote peer-origin agents so the human reader can see what's directly
// connected vs. what came in via gossip from a paired peer.
func formatAgentDirectory(agents []store.MeshAgent) string {
	if len(agents) == 0 {
		return "## Mesh Agent Directory\n\nNo active agents.\n"
	}

	sort.Slice(agents, func(i, j int) bool {
		if agents[i].Origin != agents[j].Origin {
			return agents[i].Origin < agents[j].Origin
		}
		return agents[i].LastSeenAt.After(agents[j].LastSeenAt)
	})

	var local, remote []store.MeshAgent
	for _, a := range agents {
		if strings.HasPrefix(a.Origin, store.MeshAgentOriginPeerPrefix) {
			remote = append(remote, a)
		} else {
			local = append(local, a)
		}
	}

	var b strings.Builder
	b.WriteString("## Mesh Agent Directory\n\n")
	now := time.Now().UTC()

	b.WriteString(fmt.Sprintf("Local agents (%d):\n", len(local)))
	if len(local) == 0 {
		b.WriteString("- (none)\n")
	}
	for _, a := range local {
		writeAgentRow(&b, a, now)
	}

	b.WriteString(fmt.Sprintf("\nPeer agents (%d):\n", len(remote)))
	if len(remote) == 0 {
		b.WriteString("- (none — pair a device and the peer's agents will appear here as they connect)\n")
	}
	for _, a := range remote {
		writeAgentRow(&b, a, now)
	}

	b.WriteString("\nAddress an agent by passing `to_agent: \"<name>\"` to mesh__send. Names must be unique across the active set; if two agents share a name pass `audience: \"<session_id>\"` instead.\n")
	return b.String()
}

func writeAgentRow(b *strings.Builder, a store.MeshAgent, now time.Time) {
	name := a.Name
	if name == "" {
		name = "(unnamed)"
	}
	role := a.Role
	if role == "" {
		role = "—"
	}
	origin := "local"
	if strings.HasPrefix(a.Origin, store.MeshAgentOriginPeerPrefix) {
		origin = "peer:" + shortPeerSuffix(strings.TrimPrefix(a.Origin, store.MeshAgentOriginPeerPrefix))
	}
	last := formatRelativeAge(now.Sub(a.LastSeenAt))
	fmt.Fprintf(b, "- %s/%s [%s] — last seen %s (session %s)\n",
		name, role, origin, last, shortSessionSuffix(a.SessionID))
}

func shortPeerSuffix(peerID string) string {
	if len(peerID) <= 10 {
		return peerID
	}
	return peerID[len(peerID)-10:]
}

func shortSessionSuffix(sessionID string) string {
	if len(sessionID) <= 8 {
		return sessionID
	}
	return sessionID[len(sessionID)-8:]
}

func formatRelativeAge(d time.Duration) string {
	if d < time.Second {
		return "just now"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}
