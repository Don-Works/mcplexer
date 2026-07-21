package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/collaboration"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

type collaborationHandler struct {
	manager *collaboration.Manager
	store   collaborationAdminStore
	syncer  collaborationSyncer
}

type collaborationSyncer interface {
	SyncPeerNow(peerID string) error
}

type collaborationAdminStore interface {
	store.Store
	store.CollaborationStore
	store.CollaborationMembershipStore
}

type collaborationPrincipalView struct {
	store.Principal
	Keys        []store.PrincipalIdentityKey `json:"keys"`
	Devices     []store.PrincipalDevice      `json:"devices"`
	Invitations []store.PrincipalInvitation  `json:"invitations"`
}

type collaborationWorkspaceView struct {
	store.WorkspaceShare
	Workspace *store.Workspace                  `json:"workspace,omitempty"`
	Grants    []store.WorkspaceGrant            `json:"grants"`
	Policy    *store.WorkspacePublicationPolicy `json:"policy"`
}

type collaborationSnapshot struct {
	Enabled      bool                         `json:"enabled"`
	LocalPeerID  string                       `json:"local_peer_id,omitempty"`
	Principals   []collaborationPrincipalView `json:"principals"`
	Workspaces   []collaborationWorkspaceView `json:"workspaces"`
	Memberships  []store.WorkspaceMembership  `json:"memberships"`
	Capabilities []string                     `json:"capabilities"`
	Profiles     map[string][]string          `json:"profiles"`
}

var collaborationCapabilities = []string{
	store.CapabilityWorkspaceView,
	store.CapabilityTasksRead,
	store.CapabilityTasksCreate,
	store.CapabilityTasksPublish,
	store.CapabilityTasksEdit,
}

var collaborationProfiles = map[string][]string{
	"reader": {
		store.CapabilityWorkspaceView, store.CapabilityTasksRead,
	},
	"contributor": {
		store.CapabilityWorkspaceView, store.CapabilityTasksRead,
		store.CapabilityTasksCreate,
	},
	"editor": {
		store.CapabilityWorkspaceView, store.CapabilityTasksRead,
		store.CapabilityTasksCreate, store.CapabilityTasksEdit,
	},
	"machine_publisher": {
		store.CapabilityTasksPublish,
	},
}

func validateOperationalCapabilities(capabilities []string) error {
	allowed := make(map[string]struct{}, len(collaborationCapabilities))
	for _, capability := range collaborationCapabilities {
		allowed[capability] = struct{}{}
	}
	for _, capability := range capabilities {
		if _, ok := allowed[capability]; !ok {
			return fmt.Errorf("workspace capability %q is not available on the collaboration wire", capability)
		}
	}
	return nil
}

func (h *collaborationHandler) snapshot(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.manager == nil || h.store == nil {
		writeError(w, http.StatusServiceUnavailable, "collaboration is unavailable")
		return
	}
	// Workspaces can be added after daemon boot. Keep the matrix complete by
	// idempotently provisioning default-private share metadata before reading
	// the snapshot; this creates no grant and publishes no task.
	if h.manager.LocalPeerID() != "" {
		if _, err := h.manager.EnsureWorkspaceShares(r.Context()); err != nil {
			writeCollaborationError(w, err)
			return
		}
	}
	principals, err := h.store.ListPrincipals(r.Context())
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	principalViews := make([]collaborationPrincipalView, 0, len(principals))
	for i := range principals {
		keys, err := h.store.ListPrincipalIdentityKeys(r.Context(), principals[i].ID)
		if err != nil {
			writeCollaborationError(w, err)
			return
		}
		devices, err := h.store.ListPrincipalDevices(r.Context(), principals[i].ID)
		if err != nil {
			writeCollaborationError(w, err)
			return
		}
		invitations, err := h.store.ListPrincipalInvitations(r.Context(), principals[i].ID)
		if err != nil {
			writeCollaborationError(w, err)
			return
		}
		if keys == nil {
			keys = []store.PrincipalIdentityKey{}
		}
		if devices == nil {
			devices = []store.PrincipalDevice{}
		}
		if invitations == nil {
			invitations = []store.PrincipalInvitation{}
		}
		principalViews = append(principalViews, collaborationPrincipalView{
			Principal: principals[i], Keys: keys, Devices: devices, Invitations: invitations,
		})
	}
	shares, err := h.store.ListWorkspaceShares(r.Context())
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	workspaces, err := h.store.ListWorkspaces(r.Context())
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	workspaceByID := make(map[string]*store.Workspace, len(workspaces))
	for i := range workspaces {
		workspaceByID[workspaces[i].ID] = &workspaces[i]
	}
	workspaceViews := make([]collaborationWorkspaceView, 0, len(shares))
	for i := range shares {
		grants, err := h.store.ListWorkspaceGrants(r.Context(), shares[i].ShareID, false)
		if err != nil {
			writeCollaborationError(w, err)
			return
		}
		policy, err := h.store.GetWorkspacePublicationPolicy(r.Context(), shares[i].ShareID)
		if err != nil {
			writeCollaborationError(w, err)
			return
		}
		if grants == nil {
			grants = []store.WorkspaceGrant{}
		}
		workspaceViews = append(workspaceViews, collaborationWorkspaceView{
			WorkspaceShare: shares[i], Workspace: workspaceByID[shares[i].LocalWorkspaceID],
			Grants: grants, Policy: policy,
		})
	}
	memberships, err := h.store.ListWorkspaceMemberships(r.Context())
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	if memberships == nil {
		memberships = []store.WorkspaceMembership{}
	}
	writeJSON(w, http.StatusOK, collaborationSnapshot{
		Enabled: h.manager.LocalPeerID() != "", LocalPeerID: h.manager.LocalPeerID(),
		Principals: principalViews, Workspaces: workspaceViews, Memberships: memberships,
		Capabilities: append([]string(nil), collaborationCapabilities...),
		Profiles:     cloneCollaborationProfiles(),
	})
}

func (h *collaborationHandler) syncMembership(w http.ResponseWriter, r *http.Request) {
	if h == nil || h.store == nil || h.syncer == nil {
		writeError(w, http.StatusServiceUnavailable, "collaboration sync is unavailable")
		return
	}
	membership, err := h.store.GetWorkspaceMembership(r.Context(), r.PathValue("share_id"))
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	if membership.Status != store.WorkspaceShareStatusActive || membership.HomePeerID == "" {
		writeError(w, http.StatusConflict, "workspace membership is revoked")
		return
	}
	if err := h.syncer.SyncPeerNow(membership.HomePeerID); err != nil {
		writeError(w, http.StatusBadGateway, "workspace home sync failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"share_id":       membership.ShareID,
		"home_peer_id":   membership.HomePeerID,
		"sync_requested": true,
	})
}

func cloneCollaborationProfiles() map[string][]string {
	profiles := make(map[string][]string, len(collaborationProfiles))
	for name, capabilities := range collaborationProfiles {
		profiles[name] = append([]string(nil), capabilities...)
	}
	return profiles
}

type collaborationCreateInvitationRequest struct {
	Purpose                string                               `json:"purpose,omitempty"`
	PrincipalID            string                               `json:"principal_id,omitempty"`
	Kind                   string                               `json:"kind,omitempty"`
	DisplayName            string                               `json:"display_name,omitempty"`
	ControllingPrincipalID string                               `json:"controlling_principal_id,omitempty"`
	PublicKey              string                               `json:"public_key"`
	ReplacesKeyID          string                               `json:"replaces_key_id,omitempty"`
	WorkspaceGrants        []collaboration.InvitationGrantInput `json:"workspace_grants,omitempty"`
	ExpiresInHours         int                                  `json:"expires_in_hours,omitempty"`
}

func (h *collaborationHandler) createInvitation(w http.ResponseWriter, r *http.Request) {
	var req collaborationCreateInvitationRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	for _, grant := range req.WorkspaceGrants {
		if err := validateOperationalCapabilities(grant.Capabilities); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	var expiresIn time.Duration
	if req.ExpiresInHours > 0 {
		expiresIn = time.Duration(req.ExpiresInHours) * time.Hour
	}
	result, err := h.manager.CreateInvitation(r.Context(), collaboration.CreateInvitationInput{
		Purpose: req.Purpose, PrincipalID: req.PrincipalID, Kind: req.Kind,
		DisplayName: req.DisplayName, ControllingPrincipalID: req.ControllingPrincipalID,
		PublicKey: req.PublicKey, ReplacesKeyID: req.ReplacesKeyID,
		WorkspaceGrants: req.WorkspaceGrants, ExpiresIn: expiresIn,
	})
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *collaborationHandler) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	if err := h.store.RevokePrincipalInvitation(r.Context(), r.PathValue("id"), time.Now().UTC()); err != nil {
		writeCollaborationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *collaborationHandler) joinInvitation(w http.ResponseWriter, r *http.Request) {
	var req p2p.CollaborationJoinOptions
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	result, err := h.manager.Join(r.Context(), req)
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type collaborationEnrollRequest struct {
	PublicKey  string `json:"public_key"`
	DeviceName string `json:"device_name"`
	DeviceKind string `json:"device_kind,omitempty"`
}

func (h *collaborationHandler) enrollIdentity(w http.ResponseWriter, r *http.Request) {
	var req collaborationEnrollRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	result, err := h.manager.EnrollLocalIdentity(r.Context(), req.PublicKey, req.DeviceName, req.DeviceKind)
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type collaborationGrantRequest struct {
	Capabilities []string   `json:"capabilities"`
	ExpiresAt    *time.Time `json:"expires_at,omitempty"`
}

func (h *collaborationHandler) setGrants(w http.ResponseWriter, r *http.Request) {
	var req collaborationGrantRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if err := validateOperationalCapabilities(req.Capabilities); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	owner, err := h.manager.LocalOwner(r.Context())
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	epoch, grants, err := h.store.SetWorkspaceGrants(r.Context(), store.WorkspaceGrantSet{
		ShareID: r.PathValue("share_id"), PrincipalID: r.PathValue("principal_id"),
		Capabilities: req.Capabilities, ConstraintsJSON: []byte(`{}`),
		CreatedByPrincipalID: owner.ID, ExpiresAt: req.ExpiresAt, At: time.Now().UTC(),
	})
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	if grants == nil {
		grants = []store.WorkspaceGrant{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_epoch": epoch, "grants": grants})
}

func (h *collaborationHandler) putPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DefaultVisibility        string `json:"default_visibility"`
		AgentVisibilityCeiling   string `json:"agent_visibility_ceiling"`
		WideningRequiresApproval bool   `json:"widening_requires_approval"`
		EgressProfile            string `json:"egress_profile"`
		AllowRemoteEvidence      bool   `json:"allow_remote_evidence"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if req.AllowRemoteEvidence {
		writeError(w, http.StatusBadRequest, "remote evidence is not available on the collaboration wire; task projections remain summary-only")
		return
	}
	owner, err := h.manager.LocalOwner(r.Context())
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	policy := &store.WorkspacePublicationPolicy{
		ShareID: r.PathValue("share_id"), DefaultVisibility: req.DefaultVisibility,
		AgentVisibilityCeiling:   req.AgentVisibilityCeiling,
		WideningRequiresApproval: req.WideningRequiresApproval,
		EgressProfile:            req.EgressProfile, AllowRemoteEvidence: req.AllowRemoteEvidence,
		UpdatedByPrincipalID: owner.ID, UpdatedAt: time.Now().UTC(),
	}
	if err := h.store.PutWorkspacePublicationPolicy(r.Context(), policy); err != nil {
		writeCollaborationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, policy)
}

func (h *collaborationHandler) revokePrincipal(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if err := h.store.RevokePrincipal(r.Context(), r.PathValue("id"), req.Reason, time.Now().UTC()); err != nil {
		writeCollaborationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *collaborationHandler) revokeDevice(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Reason string `json:"reason"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	if err := h.store.RevokePrincipalDevice(r.Context(), r.PathValue("peer_id"), req.Reason, time.Now().UTC()); err != nil {
		writeCollaborationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *collaborationHandler) revokeKey(w http.ResponseWriter, r *http.Request) {
	if err := h.store.RevokePrincipalIdentityKey(r.Context(), r.PathValue("key_id"), time.Now().UTC()); err != nil {
		writeCollaborationError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type taskVisibilityRequest struct {
	Visibility           string   `json:"visibility"`
	AudiencePrincipalIDs []string `json:"audience_principal_ids,omitempty"`
}

func (h *collaborationHandler) setTaskVisibility(w http.ResponseWriter, r *http.Request) {
	var req taskVisibilityRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "decode: "+err.Error())
		return
	}
	owner, err := h.manager.LocalOwner(r.Context())
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	access, err := h.store.SetTaskVisibility(r.Context(), store.TaskVisibilityChange{
		TaskID: r.PathValue("task_id"), Visibility: req.Visibility,
		AudiencePrincipalIDs: req.AudiencePrincipalIDs,
		ActorPrincipalID:     owner.ID, At: time.Now().UTC(),
	})
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, access)
}

func (h *collaborationHandler) getTaskVisibility(w http.ResponseWriter, r *http.Request) {
	access, err := h.store.GetTaskAccess(r.Context(), r.PathValue("task_id"))
	if err != nil {
		writeCollaborationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, access)
}

func writeCollaborationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "collaboration resource not found")
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrAlreadyExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, p2p.ErrP2PNotBuiltIn):
		writeError(w, http.StatusNotImplemented, "p2p not built in (rebuild with -tags p2p)")
	case errors.Is(err, p2p.ErrInvalidIdentityPublicKey), errors.Is(err, p2p.ErrUnsupportedIdentityKey),
		errors.Is(err, p2p.ErrInvalidCollaborationInvite), errors.Is(err, p2p.ErrInvalidBindingChallenge),
		errors.Is(err, p2p.ErrInvalidBindingSignature):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, p2p.ErrSSHAgentUnavailable):
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	default:
		if strings.Contains(strings.ToLower(err.Error()), "required") || strings.Contains(strings.ToLower(err.Error()), "invalid") ||
			strings.Contains(strings.ToLower(err.Error()), "unknown workspace capability") {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, "collaboration operation failed")
	}
}
