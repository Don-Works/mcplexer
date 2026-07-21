//go:build p2p

// Package p2p — /mcplexer/task/1.0.0 protocol (Phase 3 of the per-
// workspace tasks initiative).
//
// Two-phase cross-peer task exchange:
//
//	Phase A — Offer / Direct-assign
//	  Sender:   TaskOfferEnvelope (preview only)
//	  Receiver: TaskAckEnvelope ("ok" or "error code")
//
//	Phase B — Request (only on accept)
//	  Receiver: TaskRequestEnvelope (nonce + remote_task_id)
//	  Sender:   TaskPayloadEnvelope (full body, meta, status, etc.)
//
// All envelopes are JSON, newline-delimited. The receiver authenticates
// the libp2p peer, runs scope + throttle + staleness checks, dedupes by
// (direction, from_peer_id, to_peer_id, remote_task_id, envelope_nonce),
// then either stores the offer pending OR (for direct-assign with
// task_assign scope) auto-accepts and triggers the Phase B fetch
// in-process.
package p2p

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// TaskShareProtocol is the libp2p protocol ID for the cross-peer task
// surface. JSON over a single libp2p stream, mirroring the memory share
// transport.
const TaskShareProtocol protocol.ID = "/mcplexer/task/1.0.0"

// MaxTaskBytes caps a single task's description + meta length on the
// wire. Tasks are operational records (kB at most); bounded so a
// misbehaving peer can't OOM the receiver with one payload.
const MaxTaskBytes int64 = 256 * 1024

// taskShareReadDeadline caps how long a single stream read can block.
const taskShareReadDeadline = 30 * time.Second

// TaskShareErrors surfaced over the wire are mapped by code by the
// gateway handlers; the codes themselves are stable for tests.
var (
	// ErrTaskOfferDenied is the generic "we won't accept this offer"
	// code — the wire never reveals whether the cause is pairing, scope
	// missing, or throttle so a peer can't probe state.
	ErrTaskOfferDenied = errors.New("p2p: task offer denied")
	// ErrTaskNotFound is returned when a TaskRequest references an
	// unknown / soft-deleted task id on the offering peer.
	ErrTaskNotFound = errors.New("p2p: task not found on peer")
	// ErrTaskExpired wraps a staleness window violation.
	ErrTaskExpired = errors.New("p2p: task envelope too old")
	// ErrTaskTooLarge wraps the per-payload size cap.
	ErrTaskTooLarge = errors.New("p2p: task payload exceeds size cap")
	// ErrTaskConflict means an editor published against an older home
	// revision. The home kept its canonical row unchanged.
	ErrTaskConflict = errors.New("p2p: task edit conflicts with current home revision")
)

// TaskShareProvider is the offering-side hook: given a remote_task_id,
// returns the full TaskPayloadEnvelope.
type TaskShareProvider interface {
	GetTaskPayload(ctx context.Context, requesterPeerID, requestNonce, remoteTaskID string) (*TaskPayloadEnvelope, error)
}

// TaskShareReceiver is the receiving-side hook: invoked when a Phase A
// envelope passes scope + throttle gates AND either lands as a pending
// offer or auto-accepts (per direct-assign rules). The implementation
// persists the offer row + (on auto-accept) triggers Phase B.
type TaskShareReceiver interface {
	HandleIncomingTaskOffer(
		ctx context.Context, fromPeerID string, env *TaskOfferEnvelope,
	) (state, offerID string, err error)
}

// TaskShareAuditor is the audit hook for every offer/request/install
// transition. Optional; nil = no audit.
type TaskShareAuditor interface {
	RecordTaskShare(
		ctx context.Context, action, peerID, remoteTaskID, status, errMsg string,
	)
}

// TaskShareService is the libp2p stream handler + offer/request client.
// One instance per Host. Set provider/receiver to nil to disable that
// direction.
type TaskShareService struct {
	host     *Host
	lookup   PairedPeerLookup
	provider TaskShareProvider
	receiver TaskShareReceiver
	auditor  TaskShareAuditor
	logger   *slog.Logger
}

// NewTaskShareService wires the libp2p stream handler onto host.
// lookup may be nil only when both provider + receiver are nil (the
// service then becomes inert). Auditor + logger may be nil.
func NewTaskShareService(
	host *Host,
	lookup PairedPeerLookup,
	provider TaskShareProvider,
	receiver TaskShareReceiver,
	auditor TaskShareAuditor,
	logger *slog.Logger,
) *TaskShareService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &TaskShareService{
		host:     host,
		lookup:   lookup,
		provider: provider,
		receiver: receiver,
		auditor:  auditor,
		logger:   logger,
	}
	if host != nil {
		host.Inner().SetStreamHandler(TaskShareProtocol, s.handleTaskStream)
	}
	return s
}

// OfferTask dials peerID and sends a Phase A offer (is_direct_assign=
// false). Returns the state string the receiver reported via ack (or
// the typed wire error). The caller is responsible for recording the
// outgoing offer row in its own store.
func (s *TaskShareService) OfferTask(
	ctx context.Context, peerID string, env TaskOfferEnvelope,
) (TaskAckEnvelope, error) {
	env.EnvelopeKind = TaskEnvelopeKindOffer
	env.IsDirectAssign = false
	return s.sendOffer(ctx, peerID, env)
}

// AssignTaskRemote dials peerID and sends a Phase A direct-assign
// envelope (is_direct_assign=true). The receiver may auto-accept this
// when the sender's peer record carries the task_assign:<workspace>
// scope.
func (s *TaskShareService) AssignTaskRemote(
	ctx context.Context, peerID string, env TaskOfferEnvelope,
) (TaskAckEnvelope, error) {
	env.EnvelopeKind = TaskEnvelopeKindOffer
	env.IsDirectAssign = true
	return s.sendOffer(ctx, peerID, env)
}

// sendOffer is the common Phase A client path — open stream, write
// offer line, read one ack/error line.
func (s *TaskShareService) sendOffer(
	ctx context.Context, peerID string, env TaskOfferEnvelope,
) (TaskAckEnvelope, error) {
	if env.RemoteTaskID == "" || env.RemoteWorkspaceID == "" {
		return TaskAckEnvelope{}, errors.New("OfferTask: remote_task_id + remote_workspace_id required")
	}
	if env.EnvelopeNonce == "" {
		return TaskAckEnvelope{}, errors.New("OfferTask: envelope_nonce required")
	}
	if env.EnvelopeCreatedAt.IsZero() {
		env.EnvelopeCreatedAt = time.Now().UTC()
	}
	if err := s.assertPeerPaired(ctx, peerID); err != nil {
		return TaskAckEnvelope{}, err
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return TaskAckEnvelope{}, fmt.Errorf("decode peer id: %w", err)
	}
	stream, err := s.host.Inner().NewStream(ctx, pid, TaskShareProtocol)
	if err != nil {
		return TaskAckEnvelope{}, fmt.Errorf("open task stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	_ = stream.SetDeadline(time.Now().Add(taskShareReadDeadline))
	if err := writeJSONLine(stream, env); err != nil {
		s.recordAudit(ctx, "offer", peerID, env.RemoteTaskID, "error", err.Error())
		return TaskAckEnvelope{}, err
	}
	ack, err := readTaskAck(stream)
	if err != nil {
		s.recordAudit(ctx, "offer", peerID, env.RemoteTaskID, "error", err.Error())
		return TaskAckEnvelope{}, err
	}
	s.recordAudit(ctx, "offer", peerID, env.RemoteTaskID, "ok", ack.State)
	return ack, nil
}

// RequestTaskPayload dials peerID and pulls the full payload for a
// previously-offered task. Used by the accept-offer flow.
func (s *TaskShareService) RequestTaskPayload(
	ctx context.Context, peerID, nonce, remoteTaskID string,
) (*TaskPayloadEnvelope, error) {
	if err := s.assertPeerPaired(ctx, peerID); err != nil {
		return nil, err
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return nil, fmt.Errorf("decode peer id: %w", err)
	}
	stream, err := s.host.Inner().NewStream(ctx, pid, TaskShareProtocol)
	if err != nil {
		return nil, fmt.Errorf("open task stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	_ = stream.SetDeadline(time.Now().Add(taskShareReadDeadline))
	req := TaskRequestEnvelope{
		EnvelopeKind:  TaskEnvelopeKindRequest,
		EnvelopeNonce: nonce,
		RemoteTaskID:  remoteTaskID,
	}
	if err := writeJSONLine(stream, req); err != nil {
		return nil, fmt.Errorf("send task request: %w", err)
	}
	payload, err := readTaskPayload(stream)
	if err != nil {
		s.recordAudit(ctx, "request", peerID, remoteTaskID, "error", err.Error())
		return nil, err
	}
	s.recordAudit(ctx, "request", peerID, remoteTaskID, "ok", "")
	return payload, nil
}

// assertPeerPaired returns nil iff peerID is in the paired list +
// active. Mirror of skill-share's assertPeerPaired.
func (s *TaskShareService) assertPeerPaired(ctx context.Context, peerID string) error {
	if s.lookup == nil {
		return fmt.Errorf("%w: no lookup configured", ErrPeerNotPaired)
	}
	p, err := s.lookup.GetPairedPeer(ctx, peerID)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrPeerNotPaired, peerID)
	}
	if p.Revoked {
		return fmt.Errorf("%w: %s revoked", ErrPeerNotPaired, peerID)
	}
	return nil
}

// recordAudit is a nil-safe wrapper around the auditor hook.
func (s *TaskShareService) recordAudit(
	ctx context.Context, action, peerID, remoteID, status, errMsg string,
) {
	if s.auditor == nil {
		return
	}
	s.auditor.RecordTaskShare(ctx, action, peerID, remoteID, status, errMsg)
}
