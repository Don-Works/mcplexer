//go:build !p2p

package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// MaxAttachmentBytes mirrors the p2p-build constant for stub mode. Kept
// in lock-step so callers can size-check before they know whether p2p
// is active.
const MaxAttachmentBytes int64 = 25 * 1024 * 1024

// Attachment share sentinels — declared in stub mode too so gateway tools
// can use errors.Is without a build-tag fence.
var (
	ErrAttachmentNotFound    = errors.New("p2p: attachment not found on peer")
	ErrAttachmentTooLarge    = errors.New("p2p: attachment exceeds size cap")
	ErrAttachmentShareDenied = errors.New("p2p: attachment share denied")
)

// AttachmentRequest mirrors the p2p-build shape so import sites compile.
type AttachmentRequest struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

// AttachmentPayload mirrors the p2p-build shape so import sites compile.
type AttachmentPayload struct {
	Type          string    `json:"type"`
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	WorkspaceID   string    `json:"workspace_id"`
	Filename      string    `json:"filename,omitempty"`
	MimeType      string    `json:"mime_type,omitempty"`
	SizeBytes     int64     `json:"size_bytes"`
	Sha256        string    `json:"sha256"`
	ContentBase64 string    `json:"content_base64"`
	CreatedAt     time.Time `json:"created_at"`
}

// AttachmentProvider declared in stub mode for compile-time binary parity.
type AttachmentProvider interface {
	GetAttachmentPayload(ctx context.Context, id string) (*AttachmentPayload, error)
}

// AttachmentShareAuditor declared in stub mode for compile-time binary parity.
type AttachmentShareAuditor interface {
	RecordAttachmentShare(
		ctx context.Context, action, peerID, attachmentID, status, errMsg string,
	)
}

// AttachmentShareService is a stub when the binary is built without
// `-tags p2p`. Methods return ErrP2PNotBuiltIn so callers branch on the
// sentinel and surface "p2p not enabled" replies to the agent.
type AttachmentShareService struct{}

// NewAttachmentShareService returns a non-nil stub so route registration
// in the gateway works in both build modes (mirrors NewMemoryShareService).
func NewAttachmentShareService(
	_ *Host, _ PairedPeerLookup, _ AttachmentProvider,
	_ AttachmentShareAuditor, _ *slog.Logger,
) *AttachmentShareService {
	return &AttachmentShareService{}
}

// RequestAttachment returns ErrP2PNotBuiltIn in stub mode — the p2p
// host isn't wired so there's no transport to open a stream on.
func (s *AttachmentShareService) RequestAttachment(
	_ context.Context, _, _ string,
) (*AttachmentPayload, error) {
	return nil, ErrP2PNotBuiltIn
}

// MarshalAttachmentRequest mirrors the p2p-build helper so callers that
// inspect wire shape (tests, REST proxies) compile in both modes.
func MarshalAttachmentRequest(req AttachmentRequest) ([]byte, error) {
	return json.Marshal(req)
}
