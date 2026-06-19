// offer_inbound.go — Phase A receiver path. Implements
// p2p.TaskShareReceiver on *Service via HandleIncomingTaskOffer, runs
// the three gates (scope / throttle / staleness) before persisting,
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
// three gates (scope, throttle, staleness) before deciding whether to
// persist the offer row as pending OR (for direct-assign with
// task_assign scope) auto-accept and trigger Phase B.
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
	scope := s.requiredScope(env)
	allowed, err := s.checkPeerScope(ctx, fromPeerID, env.RemoteWorkspaceName, env.IsDirectAssign)
	if err != nil {
		return "", "", err
	}
	if !allowed {
		// Audit + persist a rejection row so the user can see "X tried
		// to send N tasks without permission". State is one of the
		// rejected_* vocabulary values.
		s.recordRejected(ctx, fromPeerID, env, store.TaskOfferRejectedUnscoped)
		return store.TaskOfferRejectedUnscoped, "",
			fmt.Errorf("%w: %s scope required", p2p.ErrTaskOfferDenied, scope)
	}
	throttled, terr := s.checkAndStampThrottle(ctx, fromPeerID, env.RemoteWorkspaceID, time.Now().UTC())
	if terr != nil {
		return "", "", terr
	}
	if throttled {
		s.recordRejected(ctx, fromPeerID, env, store.TaskOfferRejectedThrottle)
		return store.TaskOfferRejectedThrottle, "",
			fmt.Errorf("%w: throttle exceeded", p2p.ErrTaskOfferDenied)
	}
	offerID, state, err := s.persistIncoming(ctx, fromPeerID, env)
	if err != nil {
		return "", "", err
	}
	// Direct-assign auto-accept: when the sender carries the task_assign
	// scope, eagerly fetch the payload + materialize the local task.
	if env.IsDirectAssign && state == store.TaskOfferAutoAccepted {
		if _, err := s.autoAcceptOffer(ctx, offerID); err != nil {
			// Auto-accept failed mid-way — leave the offer in
			// auto_accepted state so the dashboard can show the
			// half-finished transition. The sender already got an ack.
			return state, offerID, nil
		}
	}
	return state, offerID, nil
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

// requiredScope returns the scope string a peer must hold for the
// given envelope. Direct-assign requires task_assign:<ws>; plain
// offers require task_offer:<ws>. The workspace name comes off the
// envelope so the receiving daemon doesn't need to look it up.
func (s *Service) requiredScope(env *p2p.TaskOfferEnvelope) string {
	prefix := "task_offer:"
	if env.IsDirectAssign {
		prefix = "task_assign:"
	}
	ws := env.RemoteWorkspaceName
	if ws == "" {
		ws = "*" // wildcard fallback — sender didn't name the workspace
	}
	return prefix + ws
}

// checkPeerScope returns true iff the peer holds either the exact
// scope OR the wildcard form (task_offer:* / task_assign:*). For
// direct-assign envelopes we additionally accept task_offer:<ws> +
// task_assign:<ws> permissions both being granted is implicit (a peer
// authorized to assign is authorized to offer).
func (s *Service) checkPeerScope(
	ctx context.Context, peerID, workspaceName string, directAssign bool,
) (bool, error) {
	if s.peerScopes == nil {
		return false, nil
	}
	checks := []string{}
	prefix := "task_offer:"
	if directAssign {
		prefix = "task_assign:"
	}
	if workspaceName != "" {
		checks = append(checks, prefix+workspaceName)
	}
	checks = append(checks, prefix+"*")
	for _, scope := range checks {
		ok, err := s.peerScopes.HasPeerScope(ctx, peerID, scope)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// hasDirectAssignScope is a convenience used by persistIncoming to
// decide between pending vs auto_accepted on direct-assign envelopes.
// Returns false on lookup errors — fail closed.
func (s *Service) hasDirectAssignScope(ctx context.Context, peerID, workspaceName string) bool {
	ok, err := s.checkPeerScope(ctx, peerID, workspaceName, true)
	if err != nil {
		return false
	}
	return ok
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
// plain offers the state is always "pending". For direct-assign
// envelopes where the peer has task_assign scope, the state is
// "auto_accepted" and the caller triggers Phase B in-process.
func (s *Service) persistIncoming(
	ctx context.Context, fromPeerID string, env *p2p.TaskOfferEnvelope,
) (offerID, state string, err error) {
	state = store.TaskOfferPending
	if env.IsDirectAssign && s.hasDirectAssignScope(ctx, fromPeerID, env.RemoteWorkspaceName) {
		state = store.TaskOfferAutoAccepted
	}
	row := &store.TaskOffer{
		ID:                  ulid.Make().String(),
		RemoteTaskID:        env.RemoteTaskID,
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
	payload, err := s.taskShare.RequestTaskPayload(ctx, offer.FromPeerID, offer.EnvelopeNonce, offer.RemoteTaskID)
	if err != nil {
		return nil, err
	}
	// Linked-workspace convergence: if a prior accepted offer from this
	// (peer, remote_task_id) already produced a live local task, UPDATE it
	// in place rather than creating a duplicate. This is what turns
	// repeated AssignRemote pushes (one per task mutation on the sender)
	// into status/note SYNC instead of a pile of clones.
	t, err := s.convergeOrMaterialize(ctx, offer, payload, wsID)
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
