// offer_inbound.go — Phase A receiver path. Implements
// p2p.TaskShareReceiver on *Service via HandleIncomingTaskOffer, runs
// the three gates (authorization / throttle / staleness) before persisting,
// and provides the helper functions used by the outbound flow in
// offer.go (resolveAcceptWorkspace, materializeOfferedTask).
package tasks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// HandleIncomingTaskOffer implements p2p.TaskShareReceiver. Runs the
// three gates (authorization, throttle, staleness) before deciding whether to
// persist the offer row as pending OR (for direct-assign with
// an exact collaboration grant) auto-accept and trigger Phase B.
//
// Return contract:
//
//	state    — task_offers.state to record; mirrors the schema vocab
//	           (pending|auto_accepted|rejected_throttle|...). Wire ack
//	           carries this string so the sender knows what happened.
//	offerID  — local task_offers.id on success (empty on rejection).
//	err      — only typed sentinels (ErrTaskOfferDenied / ErrTaskExpired)
//	           propagate as wire errors. Anything else surfaces as
//	           "internal" + the offer is NOT persisted.
func (s *Service) HandleIncomingTaskOffer(
	ctx context.Context, fromPeerID string, env *p2p.TaskOfferEnvelope,
) (string, string, error) {
	if err := checkOfferStaleness(env, time.Now().UTC()); err != nil {
		s.recordRejected(ctx, fromPeerID, env, store.TaskOfferExpired)
		return store.TaskOfferExpired, "", err
	}
	sanitizeIncomingTaskOffer(env)
	workspaceID, principalID, autoAccept, err := s.authorizeIncomingOffer(ctx, fromPeerID, env)
	if err != nil {
		if errors.Is(err, p2p.ErrTaskConflict) {
			s.recordRejected(ctx, fromPeerID, env, store.TaskOfferConflict)
			return store.TaskOfferConflict, "", err
		}
		s.recordRejected(ctx, fromPeerID, env, store.TaskOfferRejectedUnscoped)
		return store.TaskOfferRejectedUnscoped, "",
			fmt.Errorf("%w: collaboration grant required", p2p.ErrTaskOfferDenied)
	}
	throttled, terr := s.checkAndStampThrottle(ctx, fromPeerID, env.ShareID, time.Now().UTC())
	if terr != nil {
		return "", "", terr
	}
	if throttled {
		s.recordRejected(ctx, fromPeerID, env, store.TaskOfferRejectedThrottle)
		return store.TaskOfferRejectedThrottle, "",
			fmt.Errorf("%w: throttle exceeded", p2p.ErrTaskOfferDenied)
	}
	offerID, state, err := s.persistIncoming(ctx, fromPeerID, env, workspaceID, principalID, autoAccept)
	if err != nil {
		return "", "", err
	}
	// Direct-assign auto-accept: once exact collaboration authorization has
	// succeeded, eagerly fetch the payload and materialize the local task.
	if env.IsDirectAssign && state == store.TaskOfferAutoAccepted {
		if _, err := s.autoAcceptOffer(ctx, offerID); err != nil {
			// The offer row remains as an auditable half-finished transition,
			// but the wire caller must not receive a success ack for a task
			// that was never materialized or updated.
			return state, offerID, fmt.Errorf("auto-accept task: %w", err)
		}
	}
	return state, offerID, nil
}

// authorizeIncomingOffer binds the claimed share to this node's local
// workspace and checks exact principal capabilities. A publisher may push
// directly without read access; an ordinary direct assignment needs create +
// assign, while a reviewable offer needs view + create.
func (s *Service) authorizeIncomingOffer(
	ctx context.Context, peerID string, env *p2p.TaskOfferEnvelope,
) (workspaceID, principalID string, autoAccept bool, err error) {
	if s.authorizer == nil || env == nil || env.ShareID == "" {
		return "", "", false, p2p.ErrTaskOfferDenied
	}
	base, baseErr := s.authorizer.AuthorizeWorkspace(ctx, peerID, env.ShareID)
	if baseErr != nil || env.AccessEpoch != base.AccessEpoch {
		return "", "", false, p2p.ErrTaskOfferDenied
	}
	// A direct publish carrying the id of a live home task is an update,
	// never a create. This makes local mirrors useful for contribution while
	// preventing publisher-only machines from overwriting existing work.
	existing, taskErr := s.store.GetTask(ctx, env.RemoteTaskID)
	if taskErr == nil {
		if !env.IsDirectAssign || existing.WorkspaceID != base.Share.LocalWorkspaceID {
			return "", "", false, p2p.ErrTaskOfferDenied
		}
		editor, editErr := s.authorizer.AuthorizeWorkspace(ctx, peerID, env.ShareID, store.CapabilityTasksEdit)
		if editErr != nil || env.AccessEpoch != editor.AccessEpoch {
			return "", "", false, p2p.ErrTaskOfferDenied
		}
		if env.BaseHLC == "" || env.BaseHLC != existing.HlcAt {
			return "", "", false, fmt.Errorf("%w: base=%q current=%q",
				p2p.ErrTaskConflict, env.BaseHLC, existing.HlcAt)
		}
		return editor.Share.LocalWorkspaceID, editor.Principal.ID, true, nil
	}
	if !errors.Is(taskErr, store.ErrNotFound) {
		return "", "", false, p2p.ErrTaskOfferDenied
	}
	if env.IsDirectAssign {
		publisher, publishErr := s.authorizer.AuthorizeWorkspace(ctx, peerID, env.ShareID, store.CapabilityTasksPublish)
		if publishErr == nil {
			if env.AccessEpoch != publisher.AccessEpoch {
				return "", "", false, p2p.ErrTaskOfferDenied
			}
			return publisher.Share.LocalWorkspaceID, publisher.Principal.ID, true, nil
		}
		contributor, contributorErr := s.authorizer.AuthorizeWorkspace(ctx, peerID, env.ShareID,
			store.CapabilityWorkspaceView, store.CapabilityTasksCreate)
		if contributorErr != nil || env.AccessEpoch != contributor.AccessEpoch {
			return "", "", false, p2p.ErrTaskOfferDenied
		}
		return contributor.Share.LocalWorkspaceID, contributor.Principal.ID, true, nil
	}
	contributor, err := s.authorizer.AuthorizeWorkspace(ctx, peerID, env.ShareID,
		store.CapabilityWorkspaceView, store.CapabilityTasksCreate)
	if err != nil || env.AccessEpoch != contributor.AccessEpoch {
		return "", "", false, p2p.ErrTaskOfferDenied
	}
	return contributor.Share.LocalWorkspaceID, contributor.Principal.ID, false, nil
}

// checkOfferStaleness rejects envelopes whose envelope_created_at is
// older than the staleness window. Defeats replays of long-stored
// offers across daemon restarts.
func checkOfferStaleness(env *p2p.TaskOfferEnvelope, now time.Time) error {
	if env.EnvelopeCreatedAt.IsZero() {
		return nil // unset = assume now
	}
	age := now.Sub(env.EnvelopeCreatedAt)
	if age < 0 {
		// Future-dated envelope — accept (clock skew); only past-too-old
		// is the staleness signal.
		return nil
	}
	if age > taskOfferStalenessWindow {
		return fmt.Errorf("%w: envelope %s older than %s",
			p2p.ErrTaskExpired, env.EnvelopeCreatedAt.Format(time.RFC3339), taskOfferStalenessWindow)
	}
	return nil
}

// checkAndStampThrottle is the rolling-window budget check. Returns
// (true, nil) when the (peer, workspace) pair has exceeded the
// budget — caller stamps state="rejected_throttle". Stamps the
// throttle row on every accepted envelope so the counter survives a
// daemon restart.
func (s *Service) checkAndStampThrottle(
	ctx context.Context, peerID, workspaceID string, now time.Time,
) (bool, error) {
	if peerID == "" || workspaceID == "" {
		return false, nil
	}
	current, err := s.store.GetTaskAssignThrottle(ctx, peerID, workspaceID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return false, err
	}
	windowStart := now
	count := 1
	if current != nil && now.Sub(current.WindowStartedAt) < taskOfferThrottleWindow {
		windowStart = current.WindowStartedAt
		count = current.CountInWindow + 1
		if count > taskOfferThrottleBudget {
			// Don't stamp — leave the counter where it is so the
			// throttle naturally resets after the window. (Stamping
			// would just push the window forward.)
			return true, nil
		}
	}
	err = s.store.UpsertTaskAssignThrottle(ctx, &store.TaskAssignThrottle{
		PeerID:          peerID,
		WorkspaceID:     workspaceID,
		LastAssignAt:    now,
		CountInWindow:   count,
		WindowStartedAt: windowStart,
	})
	if err != nil {
		return false, err
	}
	return false, nil
}

// persistIncoming inserts the offer row with the right state. For
// plain offers the state is always "pending". An authorized direct-assign
// envelope is "auto_accepted" and the caller triggers Phase B in-process.
func (s *Service) persistIncoming(
	ctx context.Context, fromPeerID string, env *p2p.TaskOfferEnvelope,
	workspaceID, principalID string, autoAccept bool,
) (offerID, state string, err error) {
	state = store.TaskOfferPending
	if env.IsDirectAssign && autoAccept {
		state = store.TaskOfferAutoAccepted
	}
	row := &store.TaskOffer{
		ID:                  ulid.Make().String(),
		RemoteTaskID:        env.RemoteTaskID,
		ShareID:             env.ShareID,
		SenderPrincipalID:   principalID,
		AccessEpoch:         env.AccessEpoch,
		VisibilityEpoch:     env.VisibilityEpoch,
		BaseHLC:             env.BaseHLC,
		FromPeerID:          fromPeerID,
		ToPeerID:            s.selfPeerID(),
		RemoteWorkspaceID:   env.RemoteWorkspaceID,
		RemoteWorkspaceName: env.RemoteWorkspaceName,
		WorkspaceID:         workspaceID,
		Title:               env.Title,
		DescriptionPreview:  env.DescriptionPreview,
		MetaPreview:         env.MetaPreview,
		StatusPreview:       env.StatusPreview,
		PriorityPreview:     env.PriorityPreview,
		TagsJSON:            env.Tags,
		IsDirectAssign:      env.IsDirectAssign,
		EnvelopeNonce:       env.EnvelopeNonce,
		EnvelopeCreatedAt:   env.EnvelopeCreatedAt,
		Direction:           "incoming",
		State:               state,
		CreatedAt:           time.Now().UTC(),
	}
	if err := s.store.CreateTaskOffer(ctx, row); err != nil {
		return "", "", err
	}
	s.publishOfferEvent(row)
	return row.ID, state, nil
}

// recordRejected inserts a rejection row so the UI can show "X tried
// to send a task you didn't allow". Idempotent on the uniq_task_offers
// index — re-tries from the same peer with the same nonce silently
// no-op (the store's CreateTaskOffer maps duplicate to nil).
func (s *Service) recordRejected(
	ctx context.Context, fromPeerID string, env *p2p.TaskOfferEnvelope, state string,
) {
	if env == nil {
		return
	}
	now := time.Now().UTC()
	declined := now
	row := &store.TaskOffer{
		ID:                  ulid.Make().String(),
		RemoteTaskID:        env.RemoteTaskID,
		ShareID:             env.ShareID,
		AccessEpoch:         env.AccessEpoch,
		VisibilityEpoch:     env.VisibilityEpoch,
		BaseHLC:             env.BaseHLC,
		FromPeerID:          fromPeerID,
		ToPeerID:            s.selfPeerID(),
		RemoteWorkspaceID:   env.RemoteWorkspaceID,
		RemoteWorkspaceName: env.RemoteWorkspaceName,
		Title:               env.Title,
		DescriptionPreview:  env.DescriptionPreview,
		MetaPreview:         env.MetaPreview,
		StatusPreview:       env.StatusPreview,
		PriorityPreview:     env.PriorityPreview,
		TagsJSON:            env.Tags,
		IsDirectAssign:      env.IsDirectAssign,
		EnvelopeNonce:       env.EnvelopeNonce,
		EnvelopeCreatedAt:   env.EnvelopeCreatedAt,
		Direction:           "incoming",
		State:               state,
		DeclinedAt:          &declined,
		CreatedAt:           now,
	}
	if err := s.store.CreateTaskOffer(ctx, row); err == nil {
		s.publishOfferEvent(row)
	}
}

// autoAcceptOffer runs the Phase B fetch + create flow on a freshly-
// landed direct-assign envelope. On failure the offer stays in the
// auto_accepted state with task_id null — the user can retry via
// AcceptOffer to complete it.
func (s *Service) autoAcceptOffer(ctx context.Context, offerID string) (*store.Task, error) {
	offer, err := s.store.GetTaskOffer(ctx, offerID)
	if err != nil {
		return nil, err
	}
	// Look up the workspace binding; if missing, fall back to creating
	// in the local workspace whose name matches the remote workspace
	// name (best-effort). When even that misses, we surface
	// ErrBindingRequired and let the user resolve from the tray.
	wsID, err := s.resolveAcceptWorkspace(ctx, offer, "")
	if err != nil && errors.Is(err, ErrBindingRequired) {
		if s.workspaces != nil && offer.RemoteWorkspaceName != "" {
			// no auto-binding fallback yet — let the user resolve.
			return nil, err
		}
		return nil, err
	}
	if err != nil {
		return nil, err
	}
	if s.taskShare == nil {
		return nil, errors.New("task share service not wired (p2p not enabled?)")
	}
	payload, err := s.taskShare.RequestTaskPayload(ctx, offer.FromPeerID, offer.EnvelopeNonce, offer.RemoteTaskID)
	if err != nil {
		return nil, err
	}
	if err := s.validateIncomingTaskPayload(ctx, offer, payload); err != nil {
		return nil, err
	}
	// Linked-workspace convergence: if a prior accepted offer from this
	// (peer, remote_task_id) already produced a live local task, UPDATE it
	// in place rather than creating a duplicate. This is what turns
	// repeated AssignRemote pushes (one per task mutation on the sender)
	// into status/note SYNC instead of a pile of clones.
	t, created, err := s.convergeOrMaterializeWithState(ctx, offer, payload, wsID)
	if err != nil {
		return nil, err
	}
	t, err = s.applyIncomingTaskVisibility(ctx, offer, payload, t, created)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	_ = s.store.UpdateTaskOfferState(ctx, offer.ID, store.TaskOfferAutoAccepted, &now, nil, "", t.ID, wsID)
	offer.State = store.TaskOfferAutoAccepted
	offer.AcceptedAt = &now
	offer.TaskID = t.ID
	offer.WorkspaceID = wsID
	s.publishOfferEvent(offer)
	return t, nil
}
