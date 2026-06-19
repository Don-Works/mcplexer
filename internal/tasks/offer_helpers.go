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
	var tags []string
	if len(payload.Tags) > 0 {
		_ = json.Unmarshal(payload.Tags, &tags)
	}
	return s.Create(ctx, CreateOptions{
		WorkspaceID: wsID,
		Title:       payload.Title,
		Description: payload.Description,
		Status:      payload.Status,
		Priority:    payload.Priority,
		DueAt:       payload.DueAt,
		Tags:        tags,
		Meta:        payload.Meta,
		SourceKind:  store.TaskSourcePeerImport,
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
	existingID, err := s.store.FindLocalTaskForRemoteOffer(ctx, offer.FromPeerID, offer.RemoteTaskID)
	if err == nil && existingID != "" {
		t, cerr := s.convergePeerTask(ctx, existingID, wsID, payload)
		if cerr == nil {
			return t, nil
		}
		if !errors.Is(cerr, store.ErrNotFound) {
			return nil, cerr
		}
		// Local row vanished between lookup and update — fall through and
		// materialize a fresh one.
	}
	return s.materializeOfferedTask(ctx, offer, payload, wsID)
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
func buildOfferEnvelope(t *store.Task, wsName, message string) p2p.TaskOfferEnvelope {
	return p2p.TaskOfferEnvelope{
		EnvelopeKind:        p2p.TaskEnvelopeKindOffer,
		EnvelopeNonce:       ulid.Make().String(),
		EnvelopeCreatedAt:   time.Now().UTC(),
		RemoteTaskID:        t.ID,
		RemoteWorkspaceID:   t.WorkspaceID,
		RemoteWorkspaceName: wsName,
		Title:               t.Title,
		DescriptionPreview:  truncate(t.Description, taskOfferPreviewCap),
		MetaPreview:         truncate(t.Meta, taskOfferPreviewCap),
		StatusPreview:       t.Status,
		PriorityPreview:     t.Priority,
		Tags:                t.TagsJSON,
		Message:             message,
	}
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
