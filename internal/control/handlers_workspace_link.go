// handlers_workspace_link.go — admin MCP tools for "linked workspaces":
// declaring, listing, and removing the explicit cross-machine workspace
// links that opt a (peer, remote_workspace) ↔ local_workspace pair into
// silent task replication. Backed by the migration-088 link columns on
// workspace_peer_bindings. See .planning/linked-workspaces/PLAN.md.
//
// These are CWD-gated like every other mcplexer__* admin tool (visible
// only when the agent's CWD is ⊆ ~/.mcplexer or a mcplexer source repo)
// and discoverable via mcpx__search_tools, not the slim top-level list.
package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// resolveLocalWorkspace accepts a workspace id OR name and returns the
// canonical (id, name). Name match is case-insensitive. Lets an operator
// type "gateway" instead of copying a UUID.
func resolveLocalWorkspace(ctx context.Context, s store.Store, idOrName string) (id, name string, err error) {
	if idOrName == "" {
		return "", "", fmt.Errorf("local_workspace is required")
	}
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return "", "", fmt.Errorf("list workspaces: %w", err)
	}
	for _, w := range workspaces {
		if w.ID == idOrName {
			return w.ID, w.Name, nil
		}
	}
	for _, w := range workspaces {
		if strings.EqualFold(w.Name, idOrName) {
			return w.ID, w.Name, nil
		}
	}
	return "", "", fmt.Errorf("no local workspace matches id or name %q", idOrName)
}

func handleLinkWorkspace(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	var p struct {
		PeerID              string `json:"peer_id"`
		LocalWorkspace      string `json:"local_workspace"`
		RemoteWorkspaceID   string `json:"remote_workspace_id"`
		RemoteWorkspaceName string `json:"remote_workspace_name"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.PeerID == "" {
		return nil, fmt.Errorf("peer_id is required")
	}
	if p.RemoteWorkspaceID == "" {
		return nil, fmt.Errorf("remote_workspace_id is required")
	}
	localID, localName, err := resolveLocalWorkspace(ctx, s, p.LocalWorkspace)
	if err != nil {
		return nil, err
	}
	binding := &store.WorkspacePeerBinding{
		PeerID:              p.PeerID,
		RemoteWorkspaceID:   p.RemoteWorkspaceID,
		LocalWorkspaceID:    localID,
		RemoteWorkspaceName: p.RemoteWorkspaceName,
	}
	if err := s.SetWorkspaceLink(ctx, binding, "local"); err != nil {
		return nil, fmt.Errorf("set workspace link: %w", err)
	}
	// The link IS the authorization: grant the peer the task_assign scope
	// for its workspace so its replicated task pushes are accepted inbound
	// here (the receiver checks task_assign:<remote_workspace_name>, or the
	// wildcard when the sender didn't name its workspace). Without this the
	// receiver would reject every replicated task as unscoped. Best-effort:
	// the link itself is already persisted, so a grant failure (e.g. the
	// peer isn't paired yet) surfaces as a warning rather than unwinding —
	// the operator can pair + re-link, or grant manually.
	grantScope := taskAssignScopeForLink(p.RemoteWorkspaceName)
	// The link also authorizes the peer to PULL this local workspace's
	// task state over /mcplexer/task-sync/1.0.0 (read-only gossip). Same
	// best-effort posture as the task_assign grant below.
	syncScope := taskSyncScopeForLink(localID)
	out := map[string]any{
		"linked":                true,
		"peer_id":               binding.PeerID,
		"remote_workspace_id":   binding.RemoteWorkspaceID,
		"remote_workspace_name": binding.RemoteWorkspaceName,
		"local_workspace_id":    localID,
		"local_workspace_name":  localName,
		"granted_scope":         grantScope,
		"granted_sync_scope":    syncScope,
		"note": "Tasks created or updated in this local workspace now replicate to the linked peer, and the peer is " +
			"authorized to land its replicated tasks here. Declare the matching link on the peer for two-way sync.",
	}
	if err := s.GrantPeerScope(ctx, p.PeerID, grantScope); err != nil {
		out["granted_scope"] = ""
		out["scope_grant_warning"] = fmt.Sprintf("could not grant %s (%v) — pair the peer then re-link, or grant manually, "+
			"or the peer will reject replicated tasks as unscoped", grantScope, err)
	}
	if err := s.GrantPeerScope(ctx, p.PeerID, syncScope); err != nil {
		out["granted_sync_scope"] = ""
		out["sync_scope_grant_warning"] = fmt.Sprintf("could not grant %s (%v) — pair the peer then re-link, or grant manually, "+
			"or the peer's task-sync catch-up pulls will be denied for this workspace", syncScope, err)
	}
	return jsonResult(out)
}

// taskAssignScopeForLink returns the scope a linked peer must hold to
// land replicated tasks here. Keyed by the peer's workspace name when
// known (the receiver checks task_assign:<remote_workspace_name>); falls
// back to the wildcard when the operator didn't supply the name.
func taskAssignScopeForLink(remoteWorkspaceName string) string {
	if remoteWorkspaceName == "" {
		return "task_assign:*"
	}
	return "task_assign:" + remoteWorkspaceName
}

// taskSyncScopeForLink returns the scope a linked peer must hold to pull
// this LOCAL workspace's task state over /mcplexer/task-sync/1.0.0. The
// gossip server gates on the local workspace id (that's what rides in
// the peer's Hello frame), so the grant is keyed by id — unlike
// task_assign, which the inbound-push receiver checks by the sender's
// workspace name.
func taskSyncScopeForLink(localWorkspaceID string) string {
	return "task_sync:" + localWorkspaceID
}

func handleListWorkspaceLinks(
	ctx context.Context, s store.Store, _ json.RawMessage,
) (json.RawMessage, error) {
	links, err := s.ListWorkspaceLinks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspace links: %w", err)
	}
	// Enrich each row with the local workspace name for legibility.
	workspaces, _ := s.ListWorkspaces(ctx)
	nameByID := make(map[string]string, len(workspaces))
	for _, w := range workspaces {
		nameByID[w.ID] = w.Name
	}
	type linkView struct {
		PeerID              string `json:"peer_id"`
		LocalWorkspaceID    string `json:"local_workspace_id"`
		LocalWorkspaceName  string `json:"local_workspace_name,omitempty"`
		RemoteWorkspaceID   string `json:"remote_workspace_id"`
		RemoteWorkspaceName string `json:"remote_workspace_name,omitempty"`
		LinkEstablishedBy   string `json:"link_established_by,omitempty"`
	}
	out := make([]linkView, 0, len(links))
	for _, l := range links {
		out = append(out, linkView{
			PeerID:              l.PeerID,
			LocalWorkspaceID:    l.LocalWorkspaceID,
			LocalWorkspaceName:  nameByID[l.LocalWorkspaceID],
			RemoteWorkspaceID:   l.RemoteWorkspaceID,
			RemoteWorkspaceName: l.RemoteWorkspaceName,
			LinkEstablishedBy:   l.LinkEstablishedBy,
		})
	}
	return jsonResult(out)
}

func handleUnlinkWorkspace(
	ctx context.Context, s store.Store, args json.RawMessage,
) (json.RawMessage, error) {
	var p struct {
		PeerID            string `json:"peer_id"`
		RemoteWorkspaceID string `json:"remote_workspace_id"`
	}
	if err := json.Unmarshal(args, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.PeerID == "" || p.RemoteWorkspaceID == "" {
		return nil, fmt.Errorf("peer_id and remote_workspace_id are required")
	}
	// Look up the binding before clearing so we can revoke the matching
	// task_assign + task_sync scopes this link granted (best-effort — a
	// missing binding just means there's nothing to revoke).
	revokeScopes := []string{}
	if b, err := s.GetWorkspacePeerBinding(ctx, p.PeerID, p.RemoteWorkspaceID); err == nil {
		revokeScopes = append(revokeScopes, taskAssignScopeForLink(b.RemoteWorkspaceName))
		if b.LocalWorkspaceID != "" {
			revokeScopes = append(revokeScopes, taskSyncScopeForLink(b.LocalWorkspaceID))
		}
	}
	if err := s.ClearWorkspaceLink(ctx, p.PeerID, p.RemoteWorkspaceID); err != nil {
		return nil, fmt.Errorf("clear workspace link: %w", err)
	}
	for _, scope := range revokeScopes {
		// Revoke is idempotent; ignore "scope wasn't granted" races.
		_ = s.RevokePeerScope(ctx, p.PeerID, scope)
	}
	return textResult("unlinked"), nil
}

// handleSuggestWorkspaceLinks surfaces likely same-name links the
// operator hasn't declared yet, derived from the workspace identities
// the daemon already knows about each peer (from prior task offers /
// bindings). Discovery only — it never declares a link. Suggestions get
// richer after the first cross-peer task interaction with a peer; before
// that, declare links explicitly with mcplexer__link_workspace.
func handleSuggestWorkspaceLinks(
	ctx context.Context, s store.Store, _ json.RawMessage,
) (json.RawMessage, error) {
	workspaces, err := s.ListWorkspaces(ctx)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	localByName := make(map[string]store.Workspace, len(workspaces))
	for _, w := range workspaces {
		localByName[strings.ToLower(w.Name)] = w
	}

	peers, err := s.ListPeers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list peers: %w", err)
	}

	type suggestion struct {
		PeerID              string `json:"peer_id"`
		RemoteWorkspaceID   string `json:"remote_workspace_id"`
		RemoteWorkspaceName string `json:"remote_workspace_name"`
		LocalWorkspaceID    string `json:"local_workspace_id"`
		LocalWorkspaceName  string `json:"local_workspace_name"`
	}
	var out []suggestion
	seen := make(map[string]struct{})
	for _, peer := range peers {
		if peer.RevokedAt != nil {
			continue
		}
		bindings, err := s.ListWorkspacePeerBindingsForPeer(ctx, peer.PeerID)
		if err != nil {
			continue // best-effort per peer
		}
		for _, b := range bindings {
			if b.Linked || b.RemoteWorkspaceName == "" {
				continue // already linked, or no name to match on
			}
			local, ok := localByName[strings.ToLower(b.RemoteWorkspaceName)]
			if !ok {
				continue
			}
			key := peer.PeerID + ":" + b.RemoteWorkspaceID
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, suggestion{
				PeerID:              peer.PeerID,
				RemoteWorkspaceID:   b.RemoteWorkspaceID,
				RemoteWorkspaceName: b.RemoteWorkspaceName,
				LocalWorkspaceID:    local.ID,
				LocalWorkspaceName:  local.Name,
			})
		}
	}
	return jsonResult(map[string]any{
		"suggestions": out,
		"note": "Same-name workspaces known across paired peers that are not yet linked. " +
			"Confirm with mcplexer__link_workspace. Empty until a peer's workspace identity is known " +
			"(after the first cross-peer task offer/assign).",
	})
}
