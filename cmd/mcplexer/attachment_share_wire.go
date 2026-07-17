// attachment_share_wire.go — glue between the cross-peer
// /mcplexer/attachment/1.0.0 libp2p protocol and the local task
// attachments store + on-disk blob directory. Adapters live here rather
// than internal/p2p to avoid pulling a store dependency into the p2p
// package (which would create a build cycle).
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
)

// buildAttachmentShareService wires the libp2p stream handler onto host.
// Returns a non-nil service in both build modes — the slim-build stub
// short-circuits all operations to ErrP2PNotBuiltIn, so callers can use
// the service without branching on the build tag.
//
// resolver is the consent.Resolver injected so every audit row carries
// the tier + accepted_by envelope per epic 01KSK91Q4W8TNED9MAF0CTRVKC.
// nil is tolerated (treated as NopResolver → cross_org default) so
// callers can wire without branching.
func buildAttachmentShareService(
	host *p2p.Host,
	s store.Store,
	auditor *audit.Logger,
	resolver consent.Resolver,
	selfUser *store.User,
) *p2p.AttachmentShareService {
	if resolver == nil {
		resolver = consent.NopResolver{}
	}
	// Reuse storePairedLookup defined in skill_share.go — same shape.
	lookup := &storePairedLookup{db: s}
	provider := &attachmentShareProvider{store: s}
	aud := &attachmentShareAuditor{
		auditor:  auditor,
		resolver: resolver,
		selfUser: selfUser,
	}
	return p2p.NewAttachmentShareService(host, lookup, provider, aud, slog.Default())
}

// attachmentShareProvider serves outgoing attachment payloads. Looks up
// the row by id, resolves the on-disk blob, base64-encodes it for wire
// transit, and applies the same containment guard as the local handlers
// to keep a poisoned storage_path from escaping the data dir.
type attachmentShareProvider struct {
	store store.Store
}

// GetAttachmentPayload fetches the attachment row + its on-disk body.
// Returns ErrAttachmentNotFound when missing or soft-deleted. The
// filename rides through redacted (defence-in-depth — should already
// have been redacted at upload time but the audit hop benefits from
// reapplying the pass).
func (p *attachmentShareProvider) GetAttachmentPayload(
	ctx context.Context, id, peerID string,
) (*p2p.AttachmentPayload, error) {
	row, err := p.store.GetTaskAttachment(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, p2p.ErrAttachmentNotFound
		}
		return nil, err
	}
	if row.DeletedAt != nil {
		return nil, p2p.ErrAttachmentNotFound
	}
	// Per-workspace authorization: the coarse mesh.attachment_request scope
	// gates the protocol, but an attachment id is not itself an authorization.
	// The peer may only fetch attachments in a workspace it is bound to (the
	// same workspace_peer_bindings the mesh bridge and task-sync authorize
	// against). Denied access collapses to ErrAttachmentNotFound so the wire
	// reply can't distinguish "no access" from "no such id".
	if !p.peerBoundToWorkspace(ctx, peerID, row.WorkspaceID) {
		return nil, p2p.ErrAttachmentNotFound
	}
	if row.SizeBytes > p2p.MaxAttachmentBytes {
		return nil, p2p.ErrAttachmentTooLarge
	}
	full, err := attachmentShareSafePath(row.StoragePath)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(full)
	if err != nil {
		return nil, fmt.Errorf("read attachment blob: %w", err)
	}
	return &p2p.AttachmentPayload{
		Type:          "attachment",
		ID:            row.ID,
		TaskID:        row.TaskID,
		WorkspaceID:   row.WorkspaceID,
		Filename:      audit.RedactString(row.Filename, nil),
		MimeType:      row.MimeType,
		SizeBytes:     row.SizeBytes,
		Sha256:        row.Sha256,
		ContentBase64: base64.StdEncoding.EncodeToString(body),
		CreatedAt:     row.CreatedAt,
	}, nil
}

// peerBoundToWorkspace reports whether peerID holds a workspace_peer_binding
// whose local workspace is workspaceID. An empty workspace never matches (an
// attachment must belong to a real workspace to be shareable). Fail-closed:
// on any lookup error the peer is treated as unauthorized.
func (p *attachmentShareProvider) peerBoundToWorkspace(ctx context.Context, peerID, workspaceID string) bool {
	if peerID == "" || workspaceID == "" {
		return false
	}
	ids, err := p.store.ListLocalWorkspaceIDsForPeer(ctx, peerID)
	if err != nil {
		return false
	}
	return slices.Contains(ids, workspaceID)
}

// attachmentShareSafePath resolves a storage_path under the daemon's
// data dir + enforces containment under <data_dir>/attachments. Mirror
// of safeAttachmentPath in api/task_attachments_handler.go — duplicated
// here so this file has no api/ dependency.
func attachmentShareSafePath(storageRel string) (string, error) {
	dataDir := strings.TrimSpace(os.Getenv("MCPLEXER_DATA_DIR"))
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("user home: %w", err)
		}
		dataDir = filepath.Join(home, ".mcplexer")
	}
	cleanRel := filepath.Clean(storageRel)
	if strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
		return "", fmt.Errorf("storage_path escapes data dir: %q", storageRel)
	}
	full := filepath.Join(dataDir, cleanRel)
	absRoot, _ := filepath.Abs(filepath.Join(dataDir, "attachments"))
	absFull, _ := filepath.Abs(full)
	// Containment check with a path-separator boundary: a bare HasPrefix would
	// accept a sibling like "<data_dir>/attachments-evil" that shares the
	// prefix but is not under the attachments dir.
	if absFull != absRoot && !strings.HasPrefix(absFull, absRoot+string(os.PathSeparator)) {
		return "", fmt.Errorf("storage_path resolves outside attachments dir: %q", storageRel)
	}
	return full, nil
}

// attachmentShareAuditor writes a tool-name=mesh__attachment_share audit
// row for every request/served transition so the dashboard audit page
// can surface cross-peer attachment activity.
type attachmentShareAuditor struct {
	auditor  *audit.Logger
	resolver consent.Resolver
	selfUser *store.User
}

// RecordAttachmentShare emits one audit row per transition. Best-effort
// — failures are logged inside auditor.Record. Tier + consent envelope
// land on the row; DenialReason is populated on rejection rows
// (status="denied"/"error").
func (a *attachmentShareAuditor) RecordAttachmentShare(
	ctx context.Context, action, peerID, attachmentID, status, errMsg string,
) {
	if a == nil || a.auditor == nil {
		return
	}
	params, _ := json.Marshal(map[string]any{
		"action":        action,
		"peer_id":       peerID,
		"attachment_id": attachmentID,
		"status":        status,
		"error":         errMsg,
	})
	env := shareEnvelope(ctx, a.resolver, a.selfUser, peerID,
		"mesh.attachment_request", status, errMsg)
	now := time.Now().UTC()
	_ = a.auditor.Record(ctx, &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      now,
		ToolName:       "mesh__attachment_share",
		ParamsRedacted: params,
		Status:         status,
		ErrorMessage:   errMsg,
		ActorKind:      "mesh",
		ActorID:        peerID,
		CreatedAt:      now,
		Tier:           string(env.Tier),
		AcceptedBy:     env.MarshalAcceptedBy(),
		GrantOrigin:    env.MarshalGrantOrigin(),
		DenialReason:   env.DenialReason,
	})
}
