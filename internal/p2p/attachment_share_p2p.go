//go:build p2p

package p2p

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// AttachmentShareProtocol is the libp2p protocol id for cross-peer task
// attachment transfer. Mirror of MemoryShareProtocol: one stream per
// request, JSON envelopes only (no binary frames — attachments transferred
// here are bounded at MaxAttachmentBytes and content_base64-encoded inside
// the JSON payload for symmetry with the MCP-side task__get_attachment).
const AttachmentShareProtocol protocol.ID = "/mcplexer/attachment/1.0.0"

// MaxAttachmentBytes caps a single attachment's body length on the wire.
// 25 MiB matches the REST upload cap (taskAttachmentMaxBytes) so the
// cross-peer surface can ferry the same payloads that landed via the
// dashboard. Higher than memory/task payload caps; a misbehaving peer
// can still OOM the receiver with one mismatched frame, so the receiver
// pre-checks the announced length before allocating.
const MaxAttachmentBytes int64 = 25 * 1024 * 1024

// attachmentShareReadDeadline caps how long any single read on an
// attachment stream can block. Large payloads + slow links can outrun
// this, so the reader re-arms per chunk inside readAttachmentPayload.
const attachmentShareReadDeadline = 60 * time.Second

// attachmentShareScopeName is the boolean scope on a paired peer that
// must be granted before attachment fetch requests are honored. Mirrors
// the skill/memory scope-grant model. Tier 1 (auto-paired same-user)
// peers inherit this by default; Tier 2/3 require an explicit grant.
const attachmentShareScopeName = "mesh.attachment_request"

// Attachment share sentinel errors. The wire never reveals the precise
// cause (pairing vs scope vs not-found could leak peer state) — callers
// map these to user-friendly messages in the gateway handler.
var (
	// ErrAttachmentNotFound is returned when the requested attachment id
	// is unknown on the offering peer.
	ErrAttachmentNotFound = errors.New("p2p: attachment not found on peer")
	// ErrAttachmentTooLarge wraps a per-payload size cap violation seen
	// on the wire (either announced length > cap or actual body > cap).
	ErrAttachmentTooLarge = errors.New("p2p: attachment exceeds size cap")
	// ErrAttachmentShareDenied is the generic "request refused" code —
	// no information leakage about pairing vs scope state.
	ErrAttachmentShareDenied = errors.New("p2p: attachment share denied")
)

// AttachmentRequest is the wire shape sent by the requesting peer.
// id is the attachment row id on the offering peer (the local id from
// the offerer's task_attachments table); the receiver looks this up
// scoped by the workspace it lives in.
type AttachmentRequest struct {
	Type string `json:"type"` // always "request"
	ID   string `json:"id"`
}

// AttachmentPayload is the success reply. Content is base64-encoded so
// the envelope stays a single JSON line (matching the
// /mcplexer/memory/1.0.0 framing convention). Filename rides through
// pre-redacted by the offering peer so audit-shaped tokens never leave
// the originator.
type AttachmentPayload struct {
	Type          string    `json:"type"` // always "attachment"
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

// attachmentShareError is the on-the-wire failure shape. Stream closes
// immediately after the writer flushes.
type attachmentShareError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// AttachmentProvider is the offering-side hook: given an attachment id,
// returns the full AttachmentPayload (metadata + body bytes). Returns
// ErrAttachmentNotFound when the id is unknown or soft-deleted.
type AttachmentProvider interface {
	GetAttachmentPayload(ctx context.Context, id string) (*AttachmentPayload, error)
}

// AttachmentShareAuditor is the audit hook for every request/served
// transition so the dashboard's audit page can surface cross-peer
// attachment activity. Optional; nil = no audit.
type AttachmentShareAuditor interface {
	RecordAttachmentShare(
		ctx context.Context, action, peerID, attachmentID, status, errMsg string,
	)
}

// AttachmentShareService is the libp2p stream handler + request client.
// One instance per Host. Provider may be nil on a receive-only peer.
type AttachmentShareService struct {
	host     *Host
	lookup   PairedPeerLookup
	provider AttachmentProvider
	auditor  AttachmentShareAuditor
	logger   *slog.Logger
}

// NewAttachmentShareService wires the libp2p stream handler onto host.
// lookup may be nil only when provider is nil (the service then becomes
// inert — fine for an outbound-only peer that hasn't enabled receive).
// auditor + logger may be nil.
func NewAttachmentShareService(
	host *Host,
	lookup PairedPeerLookup,
	provider AttachmentProvider,
	auditor AttachmentShareAuditor,
	logger *slog.Logger,
) *AttachmentShareService {
	if logger == nil {
		logger = slog.Default()
	}
	s := &AttachmentShareService{
		host:     host,
		lookup:   lookup,
		provider: provider,
		auditor:  auditor,
		logger:   logger,
	}
	if host != nil {
		host.Inner().SetStreamHandler(AttachmentShareProtocol, s.handleAttachmentStream)
	}
	return s
}

// RequestAttachment dials peerID and asks for the full payload of the
// named attachment. On success the payload is returned for the caller
// (gateway tool / REST proxy handler) to relay back to the agent.
func (s *AttachmentShareService) RequestAttachment(
	ctx context.Context, peerID, attachmentID string,
) (*AttachmentPayload, error) {
	if s == nil {
		return nil, errors.New("RequestAttachment: nil service")
	}
	if err := s.assertPeerPaired(ctx, peerID); err != nil {
		return nil, err
	}
	pid, err := peer.Decode(peerID)
	if err != nil {
		return nil, fmt.Errorf("decode peer id: %w", err)
	}
	stream, err := s.host.Inner().NewStream(ctx, pid, AttachmentShareProtocol)
	if err != nil {
		return nil, fmt.Errorf("open attachment stream: %w", err)
	}
	defer func() { _ = stream.Close() }()

	_ = stream.SetDeadline(time.Now().Add(attachmentShareReadDeadline))
	req := AttachmentRequest{Type: "request", ID: attachmentID}
	if err := writeJSONLine(stream, req); err != nil {
		s.recordAudit(ctx, "request", peerID, attachmentID, "error", err.Error())
		return nil, fmt.Errorf("send request: %w", err)
	}
	payload, err := readAttachmentPayload(stream)
	if err != nil {
		s.recordAudit(ctx, "request", peerID, attachmentID, "error", err.Error())
		return nil, err
	}
	s.recordAudit(ctx, "request", peerID, attachmentID, "ok", "")
	return payload, nil
}

// assertPeerPaired returns nil iff peerID is in the paired list + active.
// Mirror of memory/skill share's assertPeerPaired — kept service-local
// rather than shared because the error wrapping differs per surface
// (e.g. denial wording).
func (s *AttachmentShareService) assertPeerPaired(ctx context.Context, peerID string) error {
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
func (s *AttachmentShareService) recordAudit(
	ctx context.Context, action, peerID, attachmentID, status, errMsg string,
) {
	if s.auditor == nil {
		return
	}
	s.auditor.RecordAttachmentShare(ctx, action, peerID, attachmentID, status, errMsg)
}

// MarshalAttachmentRequest is a helper for tests + non-p2p callers that
// need to inspect the wire shape. Keeps the json import local.
func MarshalAttachmentRequest(req AttachmentRequest) ([]byte, error) {
	return json.Marshal(req)
}
