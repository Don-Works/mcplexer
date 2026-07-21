package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// meshAgentDirectory is the structured mesh__list_agents result. Agents index
// `local[]`/`peer[]` for to_agent routing; `text` carries the same directory
// as a human render for callers that want prose.
type meshAgentDirectory struct {
	Local []meshAgentEntry `json:"local"`
	Peer  []meshAgentEntry `json:"peer"`
	Count int              `json:"count"`
	Hint  string           `json:"hint"`
	Text  string           `json:"text"`
}

type meshAgentEntry struct {
	Name        string `json:"name"`
	Role        string `json:"role,omitempty"`
	Status      string `json:"status,omitempty"`
	Origin      string `json:"origin"`
	SessionID   string `json:"session_id"`
	LastSeen    string `json:"last_seen"`
	LastSeenAge string `json:"last_seen_age"`
}

// handleMeshListAgents returns the active agent directory — local plus
// every peer-origin agent we've observed via gossip from a paired peer.
// The intent is to make agents first-class addressable entities for
// `to_agent` routing on mesh__send (mirrors mesh__list_peers's role for
// devices). The wire shape is a JSON object with structured local[]/peer[]
// arrays plus a `text` render — a bare markdown string used to make the
// arrays silently undefined for a code-mode consumer.
//
// Peer-origin rows carry Name/Role/Status strings authored on another
// machine. Those structured fields are scanned and wrapped in the
// <untrusted-content> trust marker on a denylist hit (clean identifiers stay
// clean, mirroring mesh__receive), while `text` keeps the always-wrapped
// render. Builtin results never pass through sanitizeToolResult, so the
// handler wraps itself.
func (h *handler) handleMeshListAgents(ctx context.Context) (json.RawMessage, *RPCError) {
	if h.mesh == nil {
		return marshalErrorResult("Agent mesh is not enabled."), nil
	}
	agents, err := h.mesh.ListAgentsInWorkspaces(ctx, h.sessionMeshMeta(ctx).WorkspaceIDs)
	if err != nil {
		return marshalErrorResult(err.Error()), nil
	}
	scan := h.meshFieldSanitizer(ctx, MeshPrefix+"list_agents", false)
	wrap := h.meshFieldSanitizer(ctx, MeshPrefix+"list_agents", true)
	dir := buildAgentDirectory(agents, scan)
	dir.Text = wrap(formatAgentDirectory(agents))
	return marshalJSONResult(dir)
}

// buildAgentDirectory splits the agent set into local vs peer-origin rows and
// renders each as a structured entry. Peer-authored identity fields are run
// through scan so a hostile name/role/status arrives wrapped.
func buildAgentDirectory(agents []store.MeshAgent, scan func(string) string) meshAgentDirectory {
	dir := meshAgentDirectory{
		Local: []meshAgentEntry{},
		Peer:  []meshAgentEntry{},
		Count: len(agents),
		Hint: "Address an agent with to_agent:\"<name>\" (local[]/peer[] .name) on mesh__send. " +
			"Names must be unique across the active set; if two agents share a name pass audience:\"<session_id>\" instead. " +
			"origin is \"local\" or \"peer:<short>\".",
	}
	now := time.Now().UTC()
	for _, a := range agents {
		entry := meshAgentEntry{
			Name:        scan(a.Name),
			Role:        scan(a.Role),
			Status:      scan(a.Status),
			Origin:      meshAgentOrigin(a.Origin),
			SessionID:   a.SessionID,
			LastSeen:    a.LastSeenAt.UTC().Format(time.RFC3339),
			LastSeenAge: formatRelativeAge(now.Sub(a.LastSeenAt)),
		}
		if strings.HasPrefix(a.Origin, store.MeshAgentOriginPeerPrefix) {
			dir.Peer = append(dir.Peer, entry)
		} else {
			dir.Local = append(dir.Local, entry)
		}
	}
	return dir
}

// meshAgentOrigin renders an agent Origin as "local" or "peer:<short>".
func meshAgentOrigin(origin string) string {
	if strings.HasPrefix(origin, store.MeshAgentOriginPeerPrefix) {
		return "peer:" + shortPeerSuffix(strings.TrimPrefix(origin, store.MeshAgentOriginPeerPrefix))
	}
	return "local"
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
	origin := meshAgentOrigin(a.Origin)
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
