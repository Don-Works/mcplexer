// task_share_wire.go — glue between the cross-peer /mcplexer/task/
// 1.0.0 libp2p protocol and the local tasks service. Adapters live
// here rather than internal/p2p to avoid pulling a store/tasks
// dependency into the p2p package (which would create a build cycle).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// buildTaskShareService wires the libp2p stream handler onto host.
// Returns a non-nil service in both build modes — the slim-build stub
// short-circuits all operations to ErrP2PNotBuiltIn, so callers can use
// the service without branching on the build tag. The receiver hook is
// the tasks Service itself, so the protocol layer uses the same scope
// + throttle + staleness checks as a direct REST/MCP call would.
func buildTaskShareService(
	host *p2p.Host,
	s store.Store,
	taskSvc *tasks.Service,
	auditor *audit.Logger,
	resolver consent.Resolver,
	selfUser *store.User,
) *p2p.TaskShareService {
	if resolver == nil {
		resolver = consent.NopResolver{}
	}
	lookup := &storePairedLookup{db: s}
	provider := &taskShareProvider{store: s}
	// receiver is the tasks Service itself — it implements
	// p2p.TaskShareReceiver via HandleIncomingTaskOffer.
	aud := &taskShareAuditor{
		auditor:  auditor,
		resolver: resolver,
		selfUser: selfUser,
	}
	return p2p.NewTaskShareService(
		host, lookup, provider, taskSvc, aud, slog.Default(),
	)
}

// taskShareProvider serves outgoing task payloads (responding to a
// peer's accept-offer Phase B request). Looks up by local task id.
type taskShareProvider struct {
	store store.Store
}

// GetTaskPayload fetches the task and converts it to the wire shape.
// Returns ErrTaskNotFound when the id is unknown or soft-deleted.
func (p *taskShareProvider) GetTaskPayload(
	ctx context.Context, remoteID string,
) (*p2p.TaskPayloadEnvelope, error) {
	t, err := p.store.GetTask(ctx, remoteID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, p2p.ErrTaskNotFound
		}
		return nil, err
	}
	return &p2p.TaskPayloadEnvelope{
		EnvelopeKind: p2p.TaskEnvelopeKindPayload,
		RemoteTaskID: t.ID,
		Title:        t.Title,
		Description:  t.Description,
		Status:       t.Status,
		Priority:     t.Priority,
		DueAt:        t.DueAt,
		Meta:         t.Meta,
		Tags:         json.RawMessage(t.TagsJSON),
	}, nil
}

// taskShareAuditor writes a tool-name=mesh__task_share audit row for
// every offer/request transition so the dashboard audit page can
// surface peer task activity.
type taskShareAuditor struct {
	auditor  *audit.Logger
	resolver consent.Resolver
	selfUser *store.User
}

// RecordTaskShare emits one audit row per transition. Best-effort —
// failures are logged inside auditor.Record. Tier + consent envelope
// land per epic 01KSK91Q4W8TNED9MAF0CTRVKC.
func (a *taskShareAuditor) RecordTaskShare(
	ctx context.Context, action, peerID, remoteID, status, errMsg string,
) {
	if a == nil || a.auditor == nil {
		return
	}
	params, _ := json.Marshal(map[string]any{
		"action":         action,
		"peer_id":        peerID,
		"remote_task_id": remoteID,
		"status":         status,
		"error":          errMsg,
	})
	env := shareEnvelope(ctx, a.resolver, a.selfUser, peerID,
		"mesh.task_offer", status, errMsg)
	now := time.Now().UTC()
	_ = a.auditor.Record(ctx, &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      now,
		ToolName:       "mesh__task_share",
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
