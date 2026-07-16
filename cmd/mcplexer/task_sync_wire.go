// task_sync_wire.go — glue between the cross-peer /mcplexer/task-sync/
// 1.0.0 libp2p gossip protocol and the local task store + tasks service.
// Adapters live here rather than internal/p2p to avoid pulling a
// store/tasks dependency into the p2p package (which would create a
// build cycle). Mirrors task_share_wire.go / attachment_share_wire.go.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/collaboration"
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
	// Do not let a legacy/narrow store downgrade task sync to pairing-only
	// authorization. Production SQLite implements this composite capability;
	// unsupported stores leave the service present but deny every peer.
	collaborationStore, _ := s.(taskSyncStore)
	var authorizer *collaboration.Authorizer
	if collaborationStore != nil {
		authorizer = collaboration.NewAuthorizer(collaborationStore)
	}
	lookup := &taskSyncPairChecker{authorizer: authorizer, memberships: collaborationStore}
	source := &taskSyncSource{store: collaborationStore, authorizer: authorizer, selfPeerID: selfID}
	scope := &taskSyncScopeChecker{authorizer: authorizer}
	sink := &taskSyncSink{memberships: collaborationStore, tasks: taskSvc}
	aud := &taskSyncAuditor{auditor: auditor, resolver: resolver, selfUser: selfUser}
	return p2p.NewTaskSyncService(host, lookup, source, sink, scope, aud, slog.Default())
}

type taskSyncStore interface {
	store.Store
	store.CollaborationStore
	store.CollaborationMembershipStore
}

// taskSyncPairChecker implements p2p.PeerPairChecker over the peer
// store. A revoked peer is NOT paired — stricter than the discovery-path
// SQLPeerLookup.IsPaired (which tolerates revoked rows for re-dial
// bookkeeping); task state must never flow to a revoked peer.
type taskSyncPairChecker struct {
	authorizer  *collaboration.Authorizer
	memberships store.CollaborationMembershipStore
}

func (c *taskSyncPairChecker) IsPaired(ctx context.Context, peerID string) (bool, error) {
	if c == nil || peerID == "" {
		return false, nil
	}
	if c.authorizer != nil {
		_, err := c.authorizer.ResolvePeer(ctx, peerID)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, collaboration.ErrUnauthenticated) {
			return false, err
		}
	}
	if c.memberships == nil {
		return false, nil
	}
	return c.memberships.IsActiveWorkspaceHome(ctx, peerID)
}

// taskSyncSink admits only the home peer named by an active membership and
// applies its stream authoritatively to the local mirror. Legacy links and a
// proof-bound peer acting outside its home role cannot inject mirror rows.
type taskSyncSink struct {
	memberships store.CollaborationMembershipStore
	tasks       *tasks.Service
}

func (s *taskSyncSink) ApplyRemoteEvent(ctx context.Context, fromPeerID string, event p2p.TaskSyncEvent) error {
	if s == nil || s.memberships == nil || s.tasks == nil {
		return collaboration.ErrDenied
	}
	membership, err := s.memberships.GetWorkspaceMembershipByLocalWorkspaceID(ctx, event.WorkspaceID)
	if err != nil || membership.Status != store.WorkspaceShareStatusActive || membership.HomePeerID != fromPeerID {
		return collaboration.ErrDenied
	}
	if err := s.tasks.ApplyAuthoritativeRemoteEvent(ctx, fromPeerID, event); err != nil {
		return err
	}
	return s.memberships.AdvanceWorkspaceMembershipCursor(ctx, membership.ShareID, event.HLC, time.Now().UTC())
}

func (s *taskSyncSink) ApplyWorkspaceAccess(
	ctx context.Context, fromPeerID string, access p2p.TaskSyncWorkspaceAccess,
) error {
	if s == nil || s.memberships == nil || access.LocalWorkspaceID == "" {
		return collaboration.ErrDenied
	}
	membership, err := s.memberships.GetWorkspaceMembershipByLocalWorkspaceID(ctx, access.LocalWorkspaceID)
	if err != nil || membership.HomePeerID != fromPeerID ||
		membership.ShareID != access.ShareID || membership.RemoteWorkspaceID != access.WorkspaceID {
		return collaboration.ErrDenied
	}
	if access.Status == store.WorkspaceShareStatusRevoked {
		return s.memberships.RevokeWorkspaceMembership(ctx, membership.ShareID, time.Now().UTC())
	}
	if access.Status != store.WorkspaceShareStatusActive || access.AccessEpoch < membership.AccessEpoch {
		return collaboration.ErrDenied
	}
	membership.Capabilities = append([]string(nil), access.Capabilities...)
	membership.AccessEpoch = access.AccessEpoch
	membership.Status = store.WorkspaceShareStatusActive
	membership.UpdatedAt = time.Now().UTC()
	return s.memberships.UpsertWorkspaceMembership(ctx, membership)
}

// taskSyncSource serves the server side of the gossip stream: pages
// task rows out of the local store and converts each to the wire event
// shape. Thin pass-through to store.ListTasksSinceHLC.
type taskSyncSource struct {
	store      taskSyncStore
	authorizer *collaboration.Authorizer
	selfPeerID string
}

// WorkspaceAccess returns the current exact grants and epoch to a principal
// that has ever been a member of this workspace. This receipt is sent even
// when tasks.read has just been removed, allowing the remote mirror to become
// accurately read-only instead of retaining stale capabilities. A peer with
// no current or historical grant learns nothing about the workspace.
func (s *taskSyncSource) WorkspaceAccess(
	ctx context.Context, recipientPeerID, workspaceID string,
) (*p2p.TaskSyncWorkspaceAccess, error) {
	if s == nil || s.store == nil || s.authorizer == nil {
		return nil, collaboration.ErrDenied
	}
	peerContext, err := s.authorizer.ResolvePeer(ctx, recipientPeerID)
	if err != nil {
		return nil, collaboration.ErrDenied
	}
	share, err := s.store.GetWorkspaceShareByLocalWorkspaceID(ctx, workspaceID)
	if err != nil {
		return nil, collaboration.ErrDenied
	}
	grants, err := s.store.ListWorkspaceGrants(ctx, share.ShareID, true)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	hadGrant := false
	capabilities := make([]string, 0, len(grants))
	for _, grant := range grants {
		if grant.PrincipalID != peerContext.Principal.ID {
			continue
		}
		hadGrant = true
		if grant.RevokedAt == nil && (grant.ExpiresAt == nil || grant.ExpiresAt.After(now)) {
			capabilities = append(capabilities, grant.Capability)
		}
	}
	if !hadGrant {
		return nil, collaboration.ErrDenied
	}
	sort.Strings(capabilities)
	return &p2p.TaskSyncWorkspaceAccess{
		ShareID: share.ShareID, WorkspaceID: workspaceID,
		AccessEpoch: share.AccessEpoch, Capabilities: capabilities,
		Status: share.Status,
	}, nil
}

func (s *taskSyncSource) ListTasksSinceHLC(
	ctx context.Context, recipientPeerID, workspaceID, sinceHLC string, limit int,
) ([]p2p.TaskSyncEvent, error) {
	if s == nil || s.store == nil || s.authorizer == nil {
		return nil, collaboration.ErrDenied
	}
	profile := tasks.EgressProfileTaskSafeV1
	peerContext, err := s.authorizer.AuthorizeLocalWorkspace(ctx, recipientPeerID,
		workspaceID, store.CapabilityWorkspaceView, store.CapabilityTasksRead)
	if err != nil {
		return nil, collaboration.ErrDenied
	}
	if policy, err := s.store.GetWorkspacePublicationPolicy(ctx, peerContext.Share.ShareID); err == nil {
		profile = policy.EgressProfile
	}
	rows, err := s.store.ListTasksSinceHLC(ctx, workspaceID, sinceHLC, limit)
	if err != nil {
		return nil, err
	}
	out := make([]p2p.TaskSyncEvent, 0, len(rows))
	for i := range rows {
		var access *store.TaskAccess
		peerContext, access, err = s.authorizer.AuthorizeTaskRead(ctx, recipientPeerID, rows[i].ID)
		if errors.Is(err, collaboration.ErrDenied) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if access.WorkspaceID != workspaceID {
			continue
		}
		event, err := tasks.BuildSafeLocalEventForGossip(&rows[i], s.selfPeerID, profile)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(event.FieldPatchesJSON)
		err = s.store.RecordTaskDisclosure(ctx, &store.TaskDisclosure{
			TaskID: rows[i].ID, RecipientPrincipalID: peerContext.Principal.ID,
			RecipientDeviceID: peerContext.Device.ID, RecipientPeerID: recipientPeerID,
			ProjectionSHA256: hex.EncodeToString(sum[:]),
			ProjectionBytes:  int64(len(event.FieldPatchesJSON)),
			EgressProfile:    profile, DisclosedAt: time.Now().UTC(),
		})
		if err != nil {
			return nil, err
		}
		out = append(out, event)
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
type taskSyncScopeChecker struct{ authorizer *collaboration.Authorizer }

func (c *taskSyncScopeChecker) HasTaskSyncScope(
	ctx context.Context, peerID, workspaceID string,
) (bool, error) {
	if c == nil || c.authorizer == nil || peerID == "" || workspaceID == "" {
		return false, nil
	}
	_, err := c.authorizer.AuthorizeLocalWorkspace(ctx, peerID, workspaceID,
		store.CapabilityWorkspaceView, store.CapabilityTasksRead)
	if errors.Is(err, collaboration.ErrDenied) || errors.Is(err, collaboration.ErrUnauthenticated) {
		return false, nil
	}
	return err == nil, err
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
