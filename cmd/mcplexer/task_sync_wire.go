// task_sync_wire.go — glue between the cross-peer /mcplexer/task-sync/
// 1.0.0 libp2p gossip protocol and the local task store + tasks service.
// Adapters live here rather than internal/p2p to avoid pulling a
// store/tasks dependency into the p2p package (which would create a
// build cycle). Mirrors task_share_wire.go / attachment_share_wire.go.
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

// buildTaskSyncService wires the libp2p stream handler onto host.
// Returns a non-nil service in both build modes — the slim-build stub
// short-circuits all operations to ErrP2PNotBuiltIn, so callers can use
// the service without branching on the build tag.
//
// The sink is the tasks Service itself (ApplyRemoteEvent — LWW + HLC
// receive rule); the source streams store rows via
// tasks.BuildLocalEventForGossip so the producer and the e2e tests share
// one conversion path.
func buildTaskSyncService(
	host *p2p.Host,
	s store.Store,
	taskSvc *tasks.Service,
	auditor *audit.Logger,
	resolver consent.Resolver,
	selfUser *store.User,
) *p2p.TaskSyncService {
	if resolver == nil {
		resolver = consent.NopResolver{}
	}
	selfID := ""
	if host != nil {
		selfID = host.PeerID()
	}
	lookup := &taskSyncPairChecker{db: s}
	source := &taskSyncSource{store: s, selfPeerID: selfID}
	scope := &taskSyncScopeChecker{store: s}
	aud := &taskSyncAuditor{auditor: auditor, resolver: resolver, selfUser: selfUser}
	return p2p.NewTaskSyncService(host, lookup, source, taskSvc, scope, aud, slog.Default())
}

// taskSyncPairChecker implements p2p.PeerPairChecker over the peer
// store. A revoked peer is NOT paired — stricter than the discovery-path
// SQLPeerLookup.IsPaired (which tolerates revoked rows for re-dial
// bookkeeping); task state must never flow to a revoked peer.
type taskSyncPairChecker struct{ db store.P2PPeerStore }

func (c *taskSyncPairChecker) IsPaired(ctx context.Context, peerID string) (bool, error) {
	if c == nil || c.db == nil || peerID == "" {
		return false, nil
	}
	row, err := c.db.GetPeer(ctx, peerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return row.RevokedAt == nil, nil
}

// taskSyncSource serves the server side of the gossip stream: pages
// task rows out of the local store and converts each to the wire event
// shape. Thin pass-through to store.ListTasksSinceHLC.
type taskSyncSource struct {
	store      store.TaskStore
	selfPeerID string
}

func (s *taskSyncSource) ListTasksSinceHLC(
	ctx context.Context, workspaceID, sinceHLC string, limit int,
) ([]p2p.TaskSyncEvent, error) {
	rows, err := s.store.ListTasksSinceHLC(ctx, workspaceID, sinceHLC, limit)
	if err != nil {
		return nil, err
	}
	out := make([]p2p.TaskSyncEvent, 0, len(rows))
	for i := range rows {
		out = append(out, tasks.BuildLocalEventForGossip(&rows[i], s.selfPeerID))
	}
	return out, nil
}

func (s *taskSyncSource) MaxHLCForWorkspace(
	ctx context.Context, workspaceID string,
) (string, error) {
	return s.store.MaxHLCForWorkspace(ctx, workspaceID)
}

// taskSyncScopeChecker gates the server-side stream on the
// task_sync:<workspace> peer scope (see internal/peerscope). Accepted
// grant shapes, checked in order: the literal workspace id (what the
// link flow grants), the workspace NAME (what a human picks in the
// dashboard's grant picker), and the wildcard. Fail closed on lookup
// errors.
type taskSyncScopeChecker struct{ store store.Store }

func (c *taskSyncScopeChecker) HasTaskSyncScope(
	ctx context.Context, peerID, workspaceID string,
) (bool, error) {
	if c == nil || c.store == nil || peerID == "" || workspaceID == "" {
		return false, nil
	}
	checks := []string{"task_sync:" + workspaceID, "task_sync:*"}
	if ws, err := c.store.GetWorkspace(ctx, workspaceID); err == nil && ws != nil && ws.Name != "" {
		checks = append(checks, "task_sync:"+ws.Name)
	}
	for _, scope := range checks {
		ok, err := c.store.HasPeerScope(ctx, peerID, scope)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// taskSyncAuditor writes a tool-name=mesh__task_sync audit row for every
// protocol transition so the dashboard audit page can surface cross-peer
// task replication activity.
type taskSyncAuditor struct {
	auditor  *audit.Logger
	resolver consent.Resolver
	selfUser *store.User
}

// RecordTaskSync emits one audit row per transition. Best-effort —
// failures are logged inside auditor.Record.
func (a *taskSyncAuditor) RecordTaskSync(
	ctx context.Context, action, peerID, workspaceID, status, errMsg string,
) {
	if a == nil || a.auditor == nil {
		return
	}
	params, _ := json.Marshal(map[string]any{
		"action":       action,
		"peer_id":      peerID,
		"workspace_id": workspaceID,
		"status":       status,
		"error":        errMsg,
	})
	env := shareEnvelope(ctx, a.resolver, a.selfUser, peerID,
		"task_sync:"+workspaceID, status, errMsg)
	now := time.Now().UTC()
	_ = a.auditor.Record(ctx, &store.AuditRecord{
		ID:             uuid.NewString(),
		Timestamp:      now,
		ToolName:       "mesh__task_sync",
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
