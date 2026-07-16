//go:build p2p

package p2p

import (
	"encoding/json"
	"time"

	"github.com/don-works/mcplexer/internal/scopes"
)

// Envelope kinds — every envelope carries this discriminator on the wire
// so the receiver can dispatch without separate streams per phase.
const (
	TaskEnvelopeKindOffer   = "task_offer"
	TaskEnvelopeKindRequest = "task_request"
	TaskEnvelopeKindPayload = "task_payload"
	TaskEnvelopeKindAck     = "task_ack"
	TaskEnvelopeKindError   = "task_error"
)

// TaskOfferEnvelope is Phase A — a thin descriptor sent BEFORE the
// receiver pulls the full content. Mirrors the schema described in
// PLAN.md "Phase A — Offer".
type TaskOfferEnvelope struct {
	EnvelopeKind         string          `json:"envelope_kind"` // "task_offer"
	EnvelopeNonce        string          `json:"envelope_nonce"`
	EnvelopeCreatedAt    time.Time       `json:"envelope_created_at"`
	IsDirectAssign       bool            `json:"is_direct_assign"`
	RemoteTaskID         string          `json:"remote_task_id"`
	ShareID              string          `json:"share_id"`
	AccessEpoch          int64           `json:"access_epoch"`
	Visibility           string          `json:"visibility"`
	VisibilityEpoch      int64           `json:"visibility_epoch"`
	BaseHLC              string          `json:"base_hlc,omitempty"`
	AudiencePrincipalIDs []string        `json:"audience_principal_ids,omitempty"`
	RemoteWorkspaceID    string          `json:"remote_workspace_id"`
	RemoteWorkspaceName  string          `json:"remote_workspace_name"`
	Title                string          `json:"title"`
	DescriptionPreview   string          `json:"description_preview,omitempty"`
	MetaPreview          string          `json:"meta_preview,omitempty"`
	StatusPreview        string          `json:"status_preview,omitempty"`
	PriorityPreview      string          `json:"priority_preview,omitempty"`
	Tags                 json.RawMessage `json:"tags,omitempty"`
	Message              string          `json:"message,omitempty"`
}

// TaskRequestEnvelope is Phase B's request shape — sent by the receiver
// once the operator/agent decides to accept an offer.
type TaskRequestEnvelope struct {
	EnvelopeKind  string `json:"envelope_kind"` // "task_request"
	EnvelopeNonce string `json:"envelope_nonce"`
	RemoteTaskID  string `json:"remote_task_id"`
}

// TaskPayloadEnvelope is Phase B's reply — the full task body, meta,
// status, priority, due_at, and tags. The Phase A preview fields stay
// authoritative for the offer row's preview columns; this carries the
// full content for the newly-created local task.
type TaskPayloadEnvelope struct {
	EnvelopeKind         string          `json:"envelope_kind"` // "task_payload"
	RemoteTaskID         string          `json:"remote_task_id"`
	ShareID              string          `json:"share_id"`
	AccessEpoch          int64           `json:"access_epoch"`
	Visibility           string          `json:"visibility"`
	VisibilityEpoch      int64           `json:"visibility_epoch"`
	BaseHLC              string          `json:"base_hlc,omitempty"`
	AudiencePrincipalIDs []string        `json:"audience_principal_ids,omitempty"`
	Title                string          `json:"title"`
	Description          string          `json:"description"`
	Status               string          `json:"status"`
	Priority             string          `json:"priority"`
	DueAt                *time.Time      `json:"due_at,omitempty"`
	Meta                 string          `json:"meta,omitempty"`
	Tags                 json.RawMessage `json:"tags,omitempty"`
}

// TaskAckEnvelope is the success reply to a Phase A offer — gives the
// sender a state-string back so it can mirror its outgoing offer row.
type TaskAckEnvelope struct {
	EnvelopeKind string `json:"envelope_kind"` // "task_ack"
	State        string `json:"state"`         // pending|auto_accepted|rejected_*
	OfferID      string `json:"offer_id,omitempty"`
}

// taskShareError is the on-the-wire failure shape. Stream closes after.
//
// The optional `Denial` field carries the typed scope-rejection
// vocabulary (JTAC65); senders that recognize one of the canonical
// scopes.DenialCode values populate it so the receiving daemon can
// surface a structured reason instead of a generic "denied". Older
// peers that don't set the field still get the same Code/Message
// pair — the field is additive.
type taskShareError struct {
	EnvelopeKind string         `json:"envelope_kind"` // "task_error"
	Code         string         `json:"code"`
	Message      string         `json:"message"`
	Denial       *scopes.Denial `json:"denial,omitempty"`
}
