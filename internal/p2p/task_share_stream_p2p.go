//go:build p2p

package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// handleTaskStream is the server side of /mcplexer/task/1.0.0. Reads
// the first JSON line, peeks at envelope_kind, and dispatches.
func (s *TaskShareService) handleTaskStream(stream network.Stream) {
	defer func() { _ = stream.Close() }()

	remote := stream.Conn().RemotePeer().String()
	ctx := context.Background()

	if err := s.assertInboundPeerPaired(ctx, remote); err != nil {
		s.logger.Info("task stream rejected", "peer", remote, "error", err)
		s.recordAudit(ctx, "stream_rejected", remote, "", "denied", err.Error())
		_ = writeJSONLine(stream, taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "denied",
			Message:      ErrTaskOfferDenied.Error(),
		})
		return
	}

	_ = stream.SetReadDeadline(time.Now().Add(taskShareReadDeadline))
	line, err := readLimitedLine(stream, maxShareControlLineBytes)
	if err != nil {
		s.logger.Debug("task stream read header", "peer", remote, "error", err)
		return
	}
	s.dispatchTaskInbound(ctx, stream, remote, line)
}

// dispatchTaskInbound parses the envelope_kind discriminator and routes
// to the appropriate handler.
func (s *TaskShareService) dispatchTaskInbound(
	ctx context.Context, stream network.Stream, remote string, line []byte,
) {
	var head struct {
		Kind string `json:"envelope_kind"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		s.logger.Debug("task envelope parse", "peer", remote, "error", err)
		_ = writeJSONLine(stream, taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "bad_request",
			Message:      err.Error(),
		})
		return
	}
	switch head.Kind {
	case TaskEnvelopeKindOffer:
		s.handleTaskInboundOffer(ctx, stream, remote, line)
	case TaskEnvelopeKindRequest:
		s.handleTaskInboundRequest(ctx, stream, remote, line)
	default:
		_ = writeJSONLine(stream, taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "bad_request",
			Message:      "unknown envelope_kind: " + head.Kind,
		})
	}
}

// handleTaskInboundOffer parses the offer, hands it to the receiver
// hook (which runs scope + throttle + staleness checks + persists), then
// writes the ack/error line back.
func (s *TaskShareService) handleTaskInboundOffer(
	ctx context.Context, stream network.Stream, remote string, line []byte,
) {
	var env TaskOfferEnvelope
	if err := json.Unmarshal(line, &env); err != nil {
		_ = writeJSONLine(stream, taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "bad_request",
			Message:      err.Error(),
		})
		return
	}
	if s.receiver == nil {
		_ = writeJSONLine(stream, taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "denied",
			Message:      "no receiver configured",
		})
		return
	}
	state, offerID, err := s.receiver.HandleIncomingTaskOffer(ctx, remote, &env)
	if err != nil {
		s.recordAudit(ctx, "offer_received", remote, env.RemoteTaskID, "error", err.Error())
		_ = writeJSONLine(stream, mapHandlerError(err))
		return
	}
	s.recordAudit(ctx, "offer_received", remote, env.RemoteTaskID, state, "")
	_ = writeJSONLine(stream, TaskAckEnvelope{
		EnvelopeKind: TaskEnvelopeKindAck,
		State:        state,
		OfferID:      offerID,
	})
}

// handleTaskInboundRequest serves the full payload back to the
// requester. One JSON line containing the TaskPayloadEnvelope, or a
// taskShareError line.
func (s *TaskShareService) handleTaskInboundRequest(
	ctx context.Context, stream network.Stream, remote string, line []byte,
) {
	if s.provider == nil {
		_ = writeJSONLine(stream, taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "not_found",
			Message:      "no provider configured",
		})
		return
	}
	var req TaskRequestEnvelope
	if err := json.Unmarshal(line, &req); err != nil {
		_ = writeJSONLine(stream, taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "bad_request",
			Message:      err.Error(),
		})
		return
	}
	payload, err := s.provider.GetTaskPayload(ctx, remote, req.EnvelopeNonce, req.RemoteTaskID)
	if err != nil {
		code := "not_found"
		if !errors.Is(err, ErrTaskNotFound) {
			code = "internal"
		}
		s.recordAudit(ctx, "request_received", remote, req.RemoteTaskID, "error", err.Error())
		_ = writeJSONLine(stream, taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         code,
			Message:      err.Error(),
		})
		return
	}
	if int64(len(payload.Description)+len(payload.Meta)) > MaxTaskBytes {
		_ = writeJSONLine(stream, taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "too_large",
			Message:      ErrTaskTooLarge.Error(),
		})
		return
	}
	payload.EnvelopeKind = TaskEnvelopeKindPayload
	payload.RemoteTaskID = req.RemoteTaskID
	_ = stream.SetWriteDeadline(time.Now().Add(taskShareReadDeadline))
	if err := writeJSONLine(stream, payload); err != nil {
		s.logger.Warn("task payload write",
			"peer", remote, "remote_task_id", req.RemoteTaskID, "error", err)
		return
	}
	s.recordAudit(ctx, "request_served", remote, req.RemoteTaskID, "ok", "")
}

// assertInboundPeerPaired enforces pairing on receive. Scope checks are
// per-action and live inside the receiver hook (it needs the parsed
// envelope to know whether to look at task_offer:* or task_assign:*).
func (s *TaskShareService) assertInboundPeerPaired(ctx context.Context, peerID string) error {
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

// mapHandlerError translates a typed receiver-side error into the wire
// error envelope. The receiver MUST return one of the typed sentinels
// here for the right code to surface; everything else maps to "internal".
func mapHandlerError(err error) taskShareError {
	switch {
	case errors.Is(err, ErrTaskOfferDenied):
		return taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "denied", Message: err.Error(),
		}
	case errors.Is(err, ErrTaskExpired):
		return taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "expired", Message: err.Error(),
		}
	case errors.Is(err, ErrTaskTooLarge):
		return taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "too_large", Message: err.Error(),
		}
	case errors.Is(err, ErrTaskConflict):
		return taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "conflict", Message: err.Error(),
		}
	default:
		return taskShareError{
			EnvelopeKind: TaskEnvelopeKindError,
			Code:         "internal", Message: err.Error(),
		}
	}
}

// readTaskAck reads the one-line ack/error reply from a Phase A send.
func readTaskAck(stream network.Stream) (TaskAckEnvelope, error) {
	line, err := readOneLine(stream)
	if err != nil {
		return TaskAckEnvelope{}, err
	}
	var head struct {
		Kind string `json:"envelope_kind"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return TaskAckEnvelope{}, fmt.Errorf("decode ack head: %w", err)
	}
	switch head.Kind {
	case TaskEnvelopeKindAck:
		var ack TaskAckEnvelope
		if err := json.Unmarshal(line, &ack); err != nil {
			return TaskAckEnvelope{}, fmt.Errorf("decode ack: %w", err)
		}
		return ack, nil
	case TaskEnvelopeKindError:
		return TaskAckEnvelope{}, decodeTaskStreamError(line)
	default:
		return TaskAckEnvelope{}, fmt.Errorf("unknown ack envelope_kind %q", head.Kind)
	}
}

// readTaskPayload reads the one-line payload/error reply from a Phase B
// request.
func readTaskPayload(stream network.Stream) (*TaskPayloadEnvelope, error) {
	line, err := readOneLine(stream)
	if err != nil {
		return nil, err
	}
	var head struct {
		Kind string `json:"envelope_kind"`
	}
	if err := json.Unmarshal(line, &head); err != nil {
		return nil, fmt.Errorf("decode payload head: %w", err)
	}
	switch head.Kind {
	case TaskEnvelopeKindPayload:
		var payload TaskPayloadEnvelope
		if err := json.Unmarshal(line, &payload); err != nil {
			return nil, fmt.Errorf("decode payload: %w", err)
		}
		return &payload, nil
	case TaskEnvelopeKindError:
		return nil, decodeTaskStreamError(line)
	default:
		return nil, fmt.Errorf("unknown reply envelope_kind %q", head.Kind)
	}
}

// readOneLine reads exactly one '\n'-terminated line from the stream
// (re-arming the deadline first).
func readOneLine(stream network.Stream) ([]byte, error) {
	_ = stream.SetReadDeadline(time.Now().Add(taskShareReadDeadline))
	line, err := readLimitedLine(stream, shareLineCap(MaxTaskBytes))
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("read reply: empty response")
		}
		return nil, fmt.Errorf("read reply: %w", err)
	}
	if len(line) == 0 {
		return nil, errors.New("read reply: empty response")
	}
	return line, nil
}

// decodeTaskStreamError unmarshals an on-the-wire error envelope and
// maps the code field back into the typed sentinel where possible.
func decodeTaskStreamError(line []byte) error {
	var e taskShareError
	if err := json.Unmarshal(line, &e); err != nil {
		return fmt.Errorf("decode error reply: %w", err)
	}
	switch e.Code {
	case "denied":
		return fmt.Errorf("%w: %s", ErrTaskOfferDenied, e.Message)
	case "not_found":
		return fmt.Errorf("%w: %s", ErrTaskNotFound, e.Message)
	case "expired":
		return fmt.Errorf("%w: %s", ErrTaskExpired, e.Message)
	case "too_large":
		return fmt.Errorf("%w: %s", ErrTaskTooLarge, e.Message)
	case "conflict":
		return fmt.Errorf("%w: %s", ErrTaskConflict, e.Message)
	default:
		return fmt.Errorf("remote error: %s: %s", e.Code, e.Message)
	}
}
