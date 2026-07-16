// offer_helpers.go — shared helpers used by both the outbound flow
// (offer.go) and the inbound receiver (offer_inbound.go). Split out to
// keep each file under the 300-line cap.
package tasks

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// ErrBindingRequired surfaces from AcceptOffer when no
// workspace_peer_bindings entry exists for (peer, remote_workspace)
// AND the caller didn't pass an explicit local workspace. Callers
// (handlers) translate this into a tray prompt.
var ErrBindingRequired = errors.New("first offer from this peer/workspace — pick a local workspace")

// resolveAcceptWorkspace runs the binding lookup. Explicit
// localWorkspaceID wins; otherwise read the bindings table.
func (s *Service) resolveAcceptWorkspace(
	ctx context.Context, offer *store.TaskOffer, localWorkspaceID string,
) (string, error) {
	if strings.TrimSpace(localWorkspaceID) != "" {
		return localWorkspaceID, nil
	}
	if offer.WorkspaceID != "" {
		return offer.WorkspaceID, nil
	}
	binding, err := s.store.GetWorkspacePeerBinding(ctx, offer.FromPeerID, offer.RemoteWorkspaceID)
	if err == nil && binding != nil {
		return binding.LocalWorkspaceID, nil
	}
	return "", ErrBindingRequired
}

// materializeOfferedTask creates the local task with origin_peer_id =
// sender and source_kind = peer-import. Tags ride through verbatim
// from the wire payload.
//
// Cross-peer meta normalisation: an older peer (pre-072) may send a
// payload whose Meta is in the legacy frontmatter shape. Service.Create
// always funnels meta through MetaToJSON, so the local row is stored in
// the canonical JSON shape regardless of what the wire payload carried.
// The remote envelope_preview on the offer row keeps its original shape
// so the dashboard's "what they sent" surface stays faithful.
func (s *Service) materializeOfferedTask(
	ctx context.Context, offer *store.TaskOffer, payload *p2p.TaskPayloadEnvelope, wsID string,
) (*store.Task, error) {
	sanitizeIncomingTaskPayload(payload)
	var tags []string
	if len(payload.Tags) > 0 {
		_ = json.Unmarshal(payload.Tags, &tags)
	}
	return s.Create(ctx, CreateOptions{
		WorkspaceID:      wsID,
		Title:            payload.Title,
		Description:      payload.Description,
		Status:           payload.Status,
		Priority:         payload.Priority,
		DueAt:            payload.DueAt,
		Tags:             tags,
		Meta:             payload.Meta,
		OwnerPrincipalID: offer.SenderPrincipalID,
		SourceKind:       store.TaskSourcePeerImport,
		// ActorKind=peer-import is load-bearing: it maps to source="peer"
		// in the Emitter's replication hook so a peer-imported task is
		// NEVER re-replicated back out (echo guard). Without it a linked
		// pair would loop A→B→A.
		ActorKind: store.TaskSourcePeerImport,
	})
}

// convergeOrMaterialize is the receive-side entry point for an accepted
// offer payload: converge onto an existing local task when this
// (peer, remote_task_id) was accepted before, otherwise create fresh.
// The dedup is what makes linked-workspace replication idempotent —
// re-pushing the same task N times yields one converging local row, not N.
func (s *Service) convergeOrMaterialize(
	ctx context.Context, offer *store.TaskOffer, payload *p2p.TaskPayloadEnvelope, wsID string,
) (*store.Task, error) {
	task, _, err := s.convergeOrMaterializeWithState(ctx, offer, payload, wsID)
	return task, err
}

// convergeOrMaterializeWithState is the production variant used after the
// authenticated payload gate. The created bit lets the caller apply the
// workspace publication policy without making this low-level convergence
// primitive itself an authorization boundary.
func (s *Service) convergeOrMaterializeWithState(
	ctx context.Context, offer *store.TaskOffer, payload *p2p.TaskPayloadEnvelope, wsID string,
) (*store.Task, bool, error) {
	// Collaboration mirror edits retain the home task's globally stable id.
	// The authorization gate has already required tasks.edit for this exact
	// existing row. Resolve it before the legacy offer-mapping convergence
	// path, which is for imported tasks whose local id may differ.
	if offer.IsDirectAssign {
		existing, existingErr := s.store.GetTask(ctx, payload.RemoteTaskID)
		if existingErr == nil {
			if existing.WorkspaceID != wsID {
				return nil, false, p2p.ErrTaskOfferDenied
			}
			t, updateErr := s.convergePeerTask(ctx, existing.ID, wsID, payload)
			return t, false, updateErr
		}
		if !errors.Is(existingErr, store.ErrNotFound) {
			return nil, false, existingErr
		}
	}
	existingID, err := s.store.FindLocalTaskForRemoteOffer(ctx, offer.FromPeerID, offer.RemoteTaskID)
	if err == nil && existingID != "" {
		t, cerr := s.convergePeerTask(ctx, existingID, wsID, payload)
		if cerr == nil {
			return t, false, nil
		}
		if !errors.Is(cerr, store.ErrNotFound) {
			return nil, false, cerr
		}
		// Local row vanished between lookup and update — fall through and
		// materialize a fresh one.
	}
	t, err := s.materializeOfferedTask(ctx, offer, payload, wsID)
	if err != nil {
		return nil, false, err
	}
	return t, true, nil
}

func (s *Service) validateIncomingTaskPayload(
	ctx context.Context, offer *store.TaskOffer, payload *p2p.TaskPayloadEnvelope,
) error {
	if offer == nil || payload == nil || payload.RemoteTaskID != offer.RemoteTaskID ||
		payload.ShareID == "" || payload.ShareID != offer.ShareID ||
		!store.ValidTaskVisibility(payload.Visibility) {
		return p2p.ErrTaskOfferDenied
	}
	envelope := &p2p.TaskOfferEnvelope{
		ShareID: payload.ShareID, AccessEpoch: payload.AccessEpoch,
		IsDirectAssign: offer.IsDirectAssign, RemoteTaskID: payload.RemoteTaskID,
		BaseHLC: payload.BaseHLC,
	}
	if payload.BaseHLC != offer.BaseHLC {
		return p2p.ErrTaskOfferDenied
	}
	workspaceID, principalID, _, err := s.authorizeIncomingOffer(ctx, offer.FromPeerID, envelope)
	if err != nil || workspaceID != offer.WorkspaceID ||
		(offer.SenderPrincipalID != "" && principalID != offer.SenderPrincipalID) {
		return p2p.ErrTaskOfferDenied
	}
	offer.SenderPrincipalID = principalID
	return nil
}

func (s *Service) applyIncomingTaskVisibility(
	ctx context.Context,
	offer *store.TaskOffer,
	payload *p2p.TaskPayloadEnvelope,
	task *store.Task,
	created bool,
) (*store.Task, error) {
	if s.collaborationStore == nil || s.authorizer == nil || offer == nil || payload == nil || task == nil {
		return nil, p2p.ErrTaskOfferDenied
	}
	policy, err := s.collaborationStore.GetWorkspacePublicationPolicy(ctx, offer.ShareID)
	if err != nil {
		return nil, err
	}
	current, err := s.collaborationStore.GetTaskAccess(ctx, task.ID)
	if err != nil {
		return nil, err
	}
	desired := payload.Visibility
	audience := append([]string(nil), payload.AudiencePrincipalIDs...)
	_, publisherErr := s.authorizer.AuthorizeWorkspace(ctx, offer.FromPeerID, offer.ShareID, store.CapabilityTasksPublish)
	if !created {
		// A content editor cannot change sharing as a side-effect of a safe
		// task projection. The remote mirror deliberately carries no audience
		// metadata, so retain the authoritative home visibility verbatim.
		desired = current.Visibility
		audience = append([]string(nil), current.AudiencePrincipalIDs...)
	} else if publisherErr == nil {
		// Publisher-only machines cannot choose a wider audience. The home
		// workspace's policy is authoritative for every published task.
		desired = policy.DefaultVisibility
		audience = nil
	}
	if !store.ValidTaskVisibility(desired) ||
		(created && visibilityRank(desired) > visibilityRank(policy.AgentVisibilityCeiling)) {
		return nil, p2p.ErrTaskOfferDenied
	}
	if created && publisherErr != nil && visibilityRank(desired) > visibilityRank(policy.DefaultVisibility) {
		if _, err := s.authorizer.AuthorizeWorkspace(ctx, offer.FromPeerID, offer.ShareID, store.CapabilityTasksShare); err != nil {
			return nil, p2p.ErrTaskOfferDenied
		}
	}
	if !created && policy.WideningRequiresApproval && visibilityRank(desired) > visibilityRank(current.Visibility) {
		return nil, p2p.ErrTaskOfferDenied
	}
	if current.Visibility != desired || !equalStringSets(current.AudiencePrincipalIDs, audience) {
		if _, err := s.collaborationStore.SetTaskVisibility(ctx, store.TaskVisibilityChange{
			TaskID: task.ID, Visibility: desired,
			AudiencePrincipalIDs: audience,
			ActorPrincipalID:     offer.SenderPrincipalID,
			At:                   time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
	}
	return s.store.GetTask(ctx, task.ID)
}

func visibilityRank(visibility string) int {
	switch visibility {
	case store.TaskVisibilityPrivate:
		return 0
	case store.TaskVisibilityRestricted:
		return 1
	case store.TaskVisibilityWorkspace:
		return 2
	default:
		return 99
	}
}

func equalStringSets(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[string]int, len(left))
	for _, value := range left {
		seen[value]++
	}
	for _, value := range right {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}

// convergePeerTask applies a re-pushed (linked-workspace replication)
// task onto an EXISTING local row instead of creating a duplicate.
// Last-write-wins on the content fields; the peer is authoritative for a
// linked mirror. Lease safety: if the local row is actively claimed
// (unexpired lease on a local session), the peer's status/assignee are
// NOT stomped — local in-progress work wins — but the descriptive fields
// still converge. Returns the updated task, or (nil, ErrNotFound) when
// the local row vanished (deleted) so the caller falls back to create.
//
// ActorKind=peer-import flows through Update → the Emitter's replication
// hook maps it to source="peer", so applying this convergence does not
// echo back to the sender.
func (s *Service) convergePeerTask(
	ctx context.Context, localTaskID, wsID string, payload *p2p.TaskPayloadEnvelope,
) (*store.Task, error) {
	existing, err := s.store.GetTask(ctx, localTaskID)
	if err != nil {
		return nil, err // ErrNotFound → caller materializes fresh
	}
	var tags []string
	if len(payload.Tags) > 0 {
		_ = json.Unmarshal(payload.Tags, &tags)
	}
	patch := UpdatePatch{
		Title:       strPtr(payload.Title),
		Description: strPtr(payload.Description),
		Priority:    strPtr(payload.Priority),
		Meta:        strPtr(payload.Meta),
		Tags:        &tags,
		ActorKind:   store.TaskSourcePeerImport,
	}
	// Lease guard — don't let a remote status flip stomp active local work.
	if !s.hasActiveLocalLease(existing) {
		patch.Status = strPtr(payload.Status)
	}
	return s.Update(ctx, wsID, localTaskID, patch)
}

// hasActiveLocalLease reports whether the task is currently claimed by a
// LOCAL session with an unexpired lease. Used to protect in-progress
// local work from a converging peer status change.
func (s *Service) hasActiveLocalLease(t *store.Task) bool {
	if t == nil {
		return false
	}
	if t.AssigneeOriginKind == store.TaskAssigneePeer || t.AssigneeSessionID == "" {
		return false
	}
	if t.LeaseExpiresAt == nil {
		return false
	}
	return t.LeaseExpiresAt.After(time.Now().UTC())
}

// strPtr is a tiny helper for building UpdatePatch pointer fields.
func strPtr(s string) *string { return &s }

// resolveWorkspaceName looks up the workspace's Name field so the
// outgoing envelope can carry the human-readable label alongside the
// id. Soft-fails to empty string — name is display-only.
func (s *Service) resolveWorkspaceName(ctx context.Context, wsID string) (string, error) {
	if s.workspaces == nil {
		return "", nil
	}
	ws, err := s.workspaces.GetWorkspace(ctx, wsID)
	if err != nil {
		return "", err
	}
	return ws.Name, nil
}

// selfPeerID returns the local libp2p peer id (when known). The store
// fan-out doesn't need this to function — outgoing offers can leave it
// blank — but populating it makes the dashboard's "sent from X" column
// stable across mirror visits.
func (s *Service) selfPeerID() string {
	return s.localPeerID
}

// buildOfferEnvelope renders the Phase A envelope from a local task.
// Previews are truncated to taskOfferPreviewCap; tags are passed verbatim
// (already JSON in the row).
func buildOfferEnvelope(t *store.Task, wsName, message, profile string) (p2p.TaskOfferEnvelope, error) {
	patch, err := safeRemoteTaskPatch(t, profile)
	if err != nil {
		return p2p.TaskOfferEnvelope{}, err
	}
	return p2p.TaskOfferEnvelope{
		EnvelopeKind:        p2p.TaskEnvelopeKindOffer,
		EnvelopeNonce:       ulid.Make().String(),
		EnvelopeCreatedAt:   time.Now().UTC(),
		RemoteTaskID:        t.ID,
		BaseHLC:             t.RemoteBaseHLC,
		RemoteWorkspaceID:   t.WorkspaceID,
		RemoteWorkspaceName: wsName,
		Title:               patch.Title,
		DescriptionPreview:  truncate(patch.Description, taskOfferPreviewCap),
		MetaPreview:         "",
		StatusPreview:       patch.Status,
		PriorityPreview:     patch.Priority,
		Tags:                patch.TagsJSON,
		Message:             safeProjectedText(message, taskOfferPreviewCap),
	}, nil
}

// truncate trims s to at most n bytes (rune-safe — falls back to the
// raw byte slice for short ASCII previews).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	// rune-safe truncation: find the last full rune within the cap.
	for i := n; i > 0; i-- {
		if i < len(s) && (s[i]&0xc0) != 0x80 {
			return s[:i]
		}
	}
	return s[:n]
}
