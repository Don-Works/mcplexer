//go:build !p2p

package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// MaxTaskBytes mirrors the p2p-build constant for stub mode.
const MaxTaskBytes int64 = 256 * 1024

// Task share sentinels — declared in stub mode too so gateway tools
// can use errors.Is without a build-tag fence.
var (
	ErrTaskOfferDenied = errors.New("p2p: task offer denied")
	ErrTaskNotFound    = errors.New("p2p: task not found on peer")
	ErrTaskExpired     = errors.New("p2p: task envelope too old")
	ErrTaskTooLarge    = errors.New("p2p: task payload exceeds size cap")
)

// Envelope kind discriminator constants — mirror the p2p-build values.
const (
	TaskEnvelopeKindOffer   = "task_offer"
	TaskEnvelopeKindRequest = "task_request"
	TaskEnvelopeKindPayload = "task_payload"
	TaskEnvelopeKindAck     = "task_ack"
	TaskEnvelopeKindError   = "task_error"
)

// TaskOfferEnvelope mirrors the p2p-build shape so import sites compile.
type TaskOfferEnvelope struct {
	EnvelopeKind        string          `json:"envelope_kind"`
	EnvelopeNonce       string          `json:"envelope_nonce"`
	EnvelopeCreatedAt   time.Time       `json:"envelope_created_at"`
	IsDirectAssign      bool            `json:"is_direct_assign"`
	RemoteTaskID        string          `json:"remote_task_id"`
	RemoteWorkspaceID   string          `json:"remote_workspace_id"`
	RemoteWorkspaceName string          `json:"remote_workspace_name"`
	Title               string          `json:"title"`
	DescriptionPreview  string          `json:"description_preview,omitempty"`
	MetaPreview         string          `json:"meta_preview,omitempty"`
	StatusPreview       string          `json:"status_preview,omitempty"`
	PriorityPreview     string          `json:"priority_preview,omitempty"`
	Tags                json.RawMessage `json:"tags,omitempty"`
	Message             string          `json:"message,omitempty"`
}

// TaskRequestEnvelope mirrors the p2p-build shape so import sites compile.
type TaskRequestEnvelope struct {
	EnvelopeKind  string `json:"envelope_kind"`
	EnvelopeNonce string `json:"envelope_nonce"`
	RemoteTaskID  string `json:"remote_task_id"`
}

// TaskPayloadEnvelope mirrors the p2p-build shape so import sites compile.
type TaskPayloadEnvelope struct {
	EnvelopeKind string          `json:"envelope_kind"`
	RemoteTaskID string          `json:"remote_task_id"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Status       string          `json:"status"`
	Priority     string          `json:"priority"`
	DueAt        *time.Time      `json:"due_at,omitempty"`
	Meta         string          `json:"meta,omitempty"`
	Tags         json.RawMessage `json:"tags,omitempty"`
}

// TaskAckEnvelope mirrors the p2p-build shape so import sites compile.
type TaskAckEnvelope struct {
	EnvelopeKind string `json:"envelope_kind"`
	State        string `json:"state"`
	OfferID      string `json:"offer_id,omitempty"`
}

// TaskShareProvider declared here for compile-time binary parity.
type TaskShareProvider interface {
	GetTaskPayload(ctx context.Context, remoteTaskID string) (*TaskPayloadEnvelope, error)
}

// TaskShareReceiver declared here for compile-time binary parity.
type TaskShareReceiver interface {
	HandleIncomingTaskOffer(
		ctx context.Context, fromPeerID string, env *TaskOfferEnvelope,
	) (state, offerID string, err error)
}

// TaskShareAuditor declared here for compile-time binary parity.
type TaskShareAuditor interface {
	RecordTaskShare(
		ctx context.Context, action, peerID, remoteTaskID, status, errMsg string,
	)
}

// TaskShareService is a stub when the binary is built without `-tags p2p`.
// Methods return ErrP2PNotBuiltIn so callers branch on the sentinel and
// surface "p2p not enabled" replies to the agent.
type TaskShareService struct{}

// NewTaskShareService returns a non-nil stub so wiring works in both
// build modes (mirrors NewMemoryShareService).
func NewTaskShareService(
	_ *Host, _ PairedPeerLookup, _ TaskShareProvider,
	_ TaskShareReceiver, _ TaskShareAuditor, _ *slog.Logger,
) *TaskShareService {
	return &TaskShareService{}
}

// OfferTask returns ErrP2PNotBuiltIn in stub mode.
func (s *TaskShareService) OfferTask(
	_ context.Context, _ string, _ TaskOfferEnvelope,
) (TaskAckEnvelope, error) {
	return TaskAckEnvelope{}, ErrP2PNotBuiltIn
}

// AssignTaskRemote returns ErrP2PNotBuiltIn in stub mode.
func (s *TaskShareService) AssignTaskRemote(
	_ context.Context, _ string, _ TaskOfferEnvelope,
) (TaskAckEnvelope, error) {
	return TaskAckEnvelope{}, ErrP2PNotBuiltIn
}

// RequestTaskPayload returns ErrP2PNotBuiltIn in stub mode.
func (s *TaskShareService) RequestTaskPayload(
	_ context.Context, _, _, _ string,
) (*TaskPayloadEnvelope, error) {
	return nil, ErrP2PNotBuiltIn
}
