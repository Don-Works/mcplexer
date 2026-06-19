package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// workspaceLinkHandler is the REST surface for "linked workspaces" —
// cross-machine task replication links (migration 088). It mirrors the
// CWD-gated MCP admin tools (mcplexer__link_workspace etc.) but runs
// in-process so the dashboard + the integration harness can drive it
// without the control-server stdio. The grant-task_assign-on-link policy
// is kept in lock-step with internal/control/handlers_workspace_link.go.
type workspaceLinkHandler struct {
	store store.Store
}

type linkView struct {
	PeerID              string `json:"peer_id"`
	LocalWorkspaceID    string `json:"local_workspace_id"`
	LocalWorkspaceName  string `json:"local_workspace_name,omitempty"`
	RemoteWorkspaceID   string `json:"remote_workspace_id"`
	RemoteWorkspaceName string `json:"remote_workspace_name,omitempty"`
	LinkEstablishedBy   string `json:"link_established_by,omitempty"`
}

func (h *workspaceLinkHandler) list(w http.ResponseWriter, r *http.Request) {
	links, err := h.store.ListWorkspaceLinks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspace links")
		return
	}
	names := h.workspaceNames(r)
	out := make([]linkView, 0, len(links))
	for _, l := range links {
		out = append(out, linkView{
			PeerID:              l.PeerID,
			LocalWorkspaceID:    l.LocalWorkspaceID,
			LocalWorkspaceName:  names[l.LocalWorkspaceID],
			RemoteWorkspaceID:   l.RemoteWorkspaceID,
			RemoteWorkspaceName: l.RemoteWorkspaceName,
			LinkEstablishedBy:   l.LinkEstablishedBy,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

type createLinkRequest struct {
	PeerID              string `json:"peer_id"`
	LocalWorkspace      string `json:"local_workspace"` // id OR name
	RemoteWorkspaceID   string `json:"remote_workspace_id"`
	RemoteWorkspaceName string `json:"remote_workspace_name"`
}

func (h *workspaceLinkHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createLinkRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.PeerID == "" || req.RemoteWorkspaceID == "" {
		writeError(w, http.StatusBadRequest, "peer_id and remote_workspace_id are required")
		return
	}
	localID, localName, err := h.resolveLocalWorkspace(r, req.LocalWorkspace)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	binding := &store.WorkspacePeerBinding{
		PeerID:              req.PeerID,
		RemoteWorkspaceID:   req.RemoteWorkspaceID,
		LocalWorkspaceID:    localID,
		RemoteWorkspaceName: req.RemoteWorkspaceName,
	}
	if err := h.store.SetWorkspaceLink(r.Context(), binding, "local"); err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "failed to set workspace link", err.Error())
		return
	}
	// The link IS the authorization: grant the peer task_assign for its
	// workspace so its replicated tasks land here. Best-effort — a grant
	// failure (peer not paired yet) is a warning, not a failure.
	grantScope := taskAssignScopeForLink(req.RemoteWorkspaceName)
	resp := map[string]any{
		"linked":                true,
		"peer_id":               binding.PeerID,
		"remote_workspace_id":   binding.RemoteWorkspaceID,
		"remote_workspace_name": binding.RemoteWorkspaceName,
		"local_workspace_id":    localID,
		"local_workspace_name":  localName,
		"granted_scope":         grantScope,
	}
	if err := h.store.GrantPeerScope(r.Context(), req.PeerID, grantScope); err != nil {
		resp["granted_scope"] = ""
		resp["scope_grant_warning"] = fmt.Sprintf("could not grant %s (%v) — pair the peer then re-link", grantScope, err)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *workspaceLinkHandler) delete(w http.ResponseWriter, r *http.Request) {
	peerID := r.URL.Query().Get("peer_id")
	remoteWsID := r.URL.Query().Get("remote_workspace_id")
	if peerID == "" || remoteWsID == "" {
		writeError(w, http.StatusBadRequest, "peer_id and remote_workspace_id query params are required")
		return
	}
	revokeScope := ""
	if b, err := h.store.GetWorkspacePeerBinding(r.Context(), peerID, remoteWsID); err == nil {
		revokeScope = taskAssignScopeForLink(b.RemoteWorkspaceName)
	}
	if err := h.store.ClearWorkspaceLink(r.Context(), peerID, remoteWsID); err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "failed to clear workspace link", err.Error())
		return
	}
	if revokeScope != "" {
		_ = h.store.RevokePeerScope(r.Context(), peerID, revokeScope)
	}
	writeJSON(w, http.StatusOK, map[string]any{"unlinked": true})
}

func (h *workspaceLinkHandler) suggest(w http.ResponseWriter, r *http.Request) {
	workspaces, err := h.store.ListWorkspaces(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list workspaces")
		return
	}
	localByName := make(map[string]store.Workspace, len(workspaces))
	for _, ws := range workspaces {
		localByName[strings.ToLower(ws.Name)] = ws
	}
	peers, err := h.store.ListPeers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list peers")
		return
	}
	type suggestion struct {
		PeerID              string `json:"peer_id"`
		RemoteWorkspaceID   string `json:"remote_workspace_id"`
		RemoteWorkspaceName string `json:"remote_workspace_name"`
		LocalWorkspaceID    string `json:"local_workspace_id"`
		LocalWorkspaceName  string `json:"local_workspace_name"`
	}
	out := []suggestion{}
	seen := make(map[string]struct{})
	for _, peer := range peers {
		if peer.RevokedAt != nil {
			continue
		}
		bindings, err := h.store.ListWorkspacePeerBindingsForPeer(r.Context(), peer.PeerID)
		if err != nil {
			continue
		}
		for _, b := range bindings {
			if b.Linked || b.RemoteWorkspaceName == "" {
				continue
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
	writeJSON(w, http.StatusOK, map[string]any{"suggestions": out})
}

// resolveLocalWorkspace accepts a workspace id OR name.
func (h *workspaceLinkHandler) resolveLocalWorkspace(r *http.Request, idOrName string) (id, name string, err error) {
	if idOrName == "" {
		return "", "", fmt.Errorf("local_workspace is required")
	}
	workspaces, err := h.store.ListWorkspaces(r.Context())
	if err != nil {
		return "", "", fmt.Errorf("list workspaces: %w", err)
	}
	for _, ws := range workspaces {
		if ws.ID == idOrName {
			return ws.ID, ws.Name, nil
		}
	}
	for _, ws := range workspaces {
		if strings.EqualFold(ws.Name, idOrName) {
			return ws.ID, ws.Name, nil
		}
	}
	return "", "", fmt.Errorf("no local workspace matches id or name %q", idOrName)
}

func (h *workspaceLinkHandler) workspaceNames(r *http.Request) map[string]string {
	workspaces, _ := h.store.ListWorkspaces(r.Context())
	names := make(map[string]string, len(workspaces))
	for _, ws := range workspaces {
		names[ws.ID] = ws.Name
	}
	return names
}

// taskAssignScopeForLink mirrors the control-server helper: the scope a
// linked peer must hold to land replicated tasks here.
func taskAssignScopeForLink(remoteWorkspaceName string) string {
	if remoteWorkspaceName == "" {
		return "task_assign:*"
	}
	return "task_assign:" + remoteWorkspaceName
}
