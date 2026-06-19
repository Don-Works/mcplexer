// offer.go — Phase 3 cross-peer task transport (PLAN.md "Cross-peer
// protocol /mcplexer/task/1.0.0"). Wires the local Service into the
// libp2p p2p.TaskShareService both as sender (Offer/AssignRemote) and
// as receiver (HandleIncomingTaskOffer). Inbound envelopes pass
// through three gates here before they create a task_offers row:
// scope check (task_offer:<ws> / task_assign:<ws> / task_*:*),
// throttle (60 envelopes / 60s per (peer, workspace)) and staleness
// (envelope_created_at < 24h).
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

// Throttle + staleness constants — see PLAN.md "Cross-peer protocol
// constraints". Configurable per-deploy is a future hook; the defaults
// here are intentionally conservative.
const (
	// taskOfferStalenessWindow rejects envelopes whose envelope_created_at
	// is older than this. Defeats replays of long-stored offers across
	// a daemon restart.
	taskOfferStalenessWindow = 24 * time.Hour
	// taskOfferThrottleWindow is the rolling window for the (peer, ws)
	// budget. 60 envelopes per 60s = "burst tolerable, sustained noise
	// rejected".
	taskOfferThrottleWindow = 60 * time.Second
	// taskOfferThrottleBudget caps envelopes per window. Rejected
	// envelopes are stored as state="rejected_throttle" rather than
	// erroring at the wire — the audit trail wants to remember why.
	taskOfferThrottleBudget = 60
	// taskOfferPreviewCap truncates description / meta previews on
	// outbound envelopes. Matches the schema column comment in 061.
	taskOfferPreviewCap = 256
)

// OfferOptions is the input to Service.Offer — local task → outgoing
// envelope. WorkspaceID is required so we don't accidentally leak a
// task across workspace boundaries on the offer-side.
type OfferOptions struct {
	WorkspaceID  string
	TaskID       string
	ToPeerID     string
	Message      string
	BySessionID  string // for audit; not transmitted
	DirectAssign bool   // sets is_direct_assign=true on the envelope
}

// AssignRemoteOptions is the input to Service.AssignRemote. Sugar
// wrapper that flips DirectAssign on top of OfferOptions.
type AssignRemoteOptions = OfferOptions

// publishOfferEvent fans an offer mutation out to SSE subscribers so the
// dashboard updates without waiting on the 30s fallback poll. The local
// workspace_id may be empty for inbound-pending offers (no binding yet);
// unfiltered subscribers still receive, workspace-filtered subscribers
// drop those on the server-side filter.
func (s *Service) publishOfferEvent(o *store.TaskOffer) {
	if o == nil {
		return
	}
	s.publish(Event{
		Kind:        EventTaskOfferUpdated,
		WorkspaceID: o.WorkspaceID,
		Offer:       o,
	})
}

// SetTaskShare wires the libp2p protocol service post-construction.
// Nil-safe — callers can drop the wiring without branching at every
// Offer/Accept call site. The daemon constructs the share service
// with this Service as the receiver hook, then calls SetTaskShare so
// outbound calls can route through it.
func (s *Service) SetTaskShare(ts *p2p.TaskShareService) {
	s.taskShare = ts
}

// TaskShare returns the wired libp2p service, or nil.
func (s *Service) TaskShare() *p2p.TaskShareService {
	return s.taskShare
}

// Offer sends a Phase A task_offer envelope to a paired peer. Records
// the outgoing offer row regardless of the wire result so the
// dashboard can show retry candidates. Returns the created offer row.
func (s *Service) Offer(ctx context.Context, opts OfferOptions) (*store.TaskOffer, error) {
	if s.taskShare == nil {
		return nil, errors.New("task share service not wired (p2p not enabled?)")
	}
	t, err := s.store.GetTask(ctx, opts.TaskID)
	if err != nil {
		return nil, err
	}
	if opts.WorkspaceID != "" && t.WorkspaceID != opts.WorkspaceID {
		return nil, store.ErrNotFound
	}
	if opts.ToPeerID == "" {
		return nil, errors.New("to_peer_id required")
	}
	wsName, _ := s.resolveWorkspaceName(ctx, t.WorkspaceID)
	env := buildOfferEnvelope(t, wsName, opts.Message)
	if opts.DirectAssign {
		env.IsDirectAssign = true
	}
	row := &store.TaskOffer{
		ID:                  ulid.Make().String(),
		TaskID:              t.ID,
		RemoteTaskID:        t.ID,
		FromPeerID:          s.selfPeerID(),
		ToPeerID:            opts.ToPeerID,
		RemoteWorkspaceID:   t.WorkspaceID,
		RemoteWorkspaceName: wsName,
		WorkspaceID:         t.WorkspaceID,
		Title:               t.Title,
		DescriptionPreview:  env.DescriptionPreview,
		MetaPreview:         env.MetaPreview,
		StatusPreview:       env.StatusPreview,
		PriorityPreview:     env.PriorityPreview,
		TagsJSON:            env.Tags,
		IsDirectAssign:      opts.DirectAssign,
		EnvelopeNonce:       env.EnvelopeNonce,
		EnvelopeCreatedAt:   env.EnvelopeCreatedAt,
		Direction:           "outgoing",
		State:               store.TaskOfferPending,
		CreatedAt:           time.Now().UTC(),
	}
	if err := s.store.CreateTaskOffer(ctx, row); err != nil {
		return nil, fmt.Errorf("record outgoing offer: %w", err)
	}
	s.publishOfferEvent(row)
	var ack p2p.TaskAckEnvelope
	var sendErr error
	if opts.DirectAssign {
		ack, sendErr = s.taskShare.AssignTaskRemote(ctx, opts.ToPeerID, env)
	} else {
		ack, sendErr = s.taskShare.OfferTask(ctx, opts.ToPeerID, env)
	}
	if sendErr != nil {
		// Leave the row pending — UI can retry. The wire failure surfaces
		// to the caller so the agent gets the error string.
		return row, sendErr
	}
	// Mirror the receiver's reported state into the local row so listings
	// reflect what the peer actually did.
	if ack.State != "" && ack.State != row.State {
		now := time.Now().UTC()
		var acceptedAt *time.Time
		if ack.State == store.TaskOfferAutoAccepted || ack.State == store.TaskOfferAccepted {
			acceptedAt = &now
		}
		_ = s.store.UpdateTaskOfferState(ctx, row.ID, ack.State, acceptedAt, nil, "", "", "")
		row.State = ack.State
		row.AcceptedAt = acceptedAt
		s.publishOfferEvent(row)
	}
	return row, nil
}

// AssignRemote is sugar for Offer with DirectAssign=true. The caller's
// "I want to fast-path this" intent is more discoverable as a separate
// entrypoint than a bool buried in OfferOptions.
func (s *Service) AssignRemote(ctx context.Context, opts AssignRemoteOptions) (*store.TaskOffer, error) {
	opts.DirectAssign = true
	return s.Offer(ctx, opts)
}

// AcceptOffer pulls the full payload from the offering peer, creates
// the local task, and stamps the offer row as accepted. localWorkspace
// is optional — when set, this is the user's explicit choice from the
// tray. When empty, we look up workspace_peer_bindings to find the
// memoized local workspace, falling back to ErrBindingRequired so the
// UI can prompt for the binding.
func (s *Service) AcceptOffer(ctx context.Context, offerID, localWorkspaceID string) (*store.Task, error) {
	if s.taskShare == nil {
		return nil, errors.New("task share service not wired (p2p not enabled?)")
	}
	offer, err := s.store.GetTaskOffer(ctx, offerID)
	if err != nil {
		return nil, err
	}
	if offer.Direction != "incoming" {
		return nil, errors.New("can only accept incoming offers")
	}
	if offer.State != store.TaskOfferPending {
		return nil, fmt.Errorf("offer state is %q; only pending offers can be accepted", offer.State)
	}
	wsID, err := s.resolveAcceptWorkspace(ctx, offer, localWorkspaceID)
	if err != nil {
		return nil, err
	}
	payload, err := s.taskShare.RequestTaskPayload(ctx, offer.FromPeerID, offer.EnvelopeNonce, offer.RemoteTaskID)
	if err != nil {
		// Leave offer pending so the user can retry — partial-state
		// "accepted but no task" would be worse.
		return nil, fmt.Errorf("fetch payload: %w", err)
	}
	t, err := s.materializeOfferedTask(ctx, offer, payload, wsID)
	if err != nil {
		return nil, fmt.Errorf("materialize task: %w", err)
	}
	now := time.Now().UTC()
	if err := s.store.UpdateTaskOfferState(ctx, offer.ID, store.TaskOfferAccepted, &now, nil, "", t.ID, wsID); err != nil {
		return t, fmt.Errorf("update offer state: %w", err)
	}
	offer.State = store.TaskOfferAccepted
	offer.AcceptedAt = &now
	offer.TaskID = t.ID
	offer.WorkspaceID = wsID
	s.publishOfferEvent(offer)
	// Memoize the workspace binding so subsequent offers from the same
	// peer / remote workspace land deterministically without prompting.
	_ = s.store.UpsertWorkspacePeerBinding(ctx, &store.WorkspacePeerBinding{
		PeerID:              offer.FromPeerID,
		RemoteWorkspaceID:   offer.RemoteWorkspaceID,
		LocalWorkspaceID:    wsID,
		RemoteWorkspaceName: offer.RemoteWorkspaceName,
		EstablishedAt:       now,
	})
	return t, nil
}

// DeclineOffer marks the offer declined with an optional reason. No
// wire round-trip — declines are private to the receiving daemon.
func (s *Service) DeclineOffer(ctx context.Context, offerID, reason string) error {
	offer, err := s.store.GetTaskOffer(ctx, offerID)
	if err != nil {
		return err
	}
	if offer.State != store.TaskOfferPending {
		return fmt.Errorf("offer state is %q; only pending offers can be declined", offer.State)
	}
	now := time.Now().UTC()
	if err := s.store.UpdateTaskOfferState(ctx, offerID, store.TaskOfferDeclined, nil, &now, reason, "", ""); err != nil {
		return err
	}
	offer.State = store.TaskOfferDeclined
	offer.DeclinedAt = &now
	offer.DeclinedReason = reason
	s.publishOfferEvent(offer)
	return nil
}

// ListOffers wraps the store filter so callers can query without
// pulling internal/store directly. Mirrors the existing List + Get
// pattern on the service.
func (s *Service) ListOffers(ctx context.Context, f store.TaskOfferFilter) ([]store.TaskOffer, error) {
	return s.store.ListTaskOffers(ctx, f)
}

// GetOffer returns one offer by ID. Convenience wrapper.
func (s *Service) GetOffer(ctx context.Context, offerID string) (*store.TaskOffer, error) {
	return s.store.GetTaskOffer(ctx, offerID)
}
