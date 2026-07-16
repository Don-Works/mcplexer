// task_share_wire.go — glue between the cross-peer /mcplexer/task/
// 1.0.0 libp2p protocol and the local tasks service. Adapters live
// here rather than internal/p2p to avoid pulling a store/tasks
// dependency into the p2p package (which would create a build cycle).
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/don-works/mcplexer/internal/collaboration"
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
	// Collaboration is an explicit optional store capability. A legacy or
	// narrow test store must never silently fall back to transport pairing as
	// authorization, so an unsupported store leaves these adapters fail-closed.
	collaborationStore, _ := s.(taskShareStore)
	var authorizer *collaboration.Authorizer
	if collaborationStore != nil {
		authorizer = collaboration.NewAuthorizer(collaborationStore)
	}
	lookup := &principalPairedLookup{authorizer: authorizer, memberships: collaborationStore}
	provider := &taskShareProvider{store: collaborationStore, authorizer: authorizer}
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

type taskShareStore interface {
	store.Store
	store.CollaborationStore
	store.CollaborationMembershipStore
}

// taskShareProvider serves outgoing task payloads (responding to a
// peer's accept-offer Phase B request). Looks up by local task id.
type taskShareProvider struct {
	store      taskShareStore
	authorizer *collaboration.Authorizer
}

// GetTaskPayload fetches the task and converts it to the wire shape.
// Returns ErrTaskNotFound when the id is unknown or soft-deleted.
func (p *taskShareProvider) GetTaskPayload(
	ctx context.Context, requesterPeerID, requestNonce, remoteID string,
) (*p2p.TaskPayloadEnvelope, error) {
	if p == nil || p.store == nil || p.authorizer == nil || requestNonce == "" {
		return nil, p2p.ErrTaskNotFound
	}
	offers, err := p.store.ListTaskOffers(ctx, store.TaskOfferFilter{
		Direction: "outgoing", PeerID: requesterPeerID, Limit: 500,
	})
	if err != nil {
		return nil, err
	}
	var authorizedOffer *store.TaskOffer
	for i := range offers {
		offer := &offers[i]
		if offer.TaskID == remoteID && offer.RemoteTaskID == remoteID &&
			offer.ToPeerID == requesterPeerID && offer.EnvelopeNonce == requestNonce &&
			offer.ShareID != "" && offer.State != store.TaskOfferDeclined &&
			offer.State != store.TaskOfferExpired {
			authorizedOffer = offer
			break
		}
	}
	if authorizedOffer == nil {
		return nil, p2p.ErrTaskNotFound
	}
	t, err := p.store.GetTask(ctx, remoteID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, p2p.ErrTaskNotFound
		}
		return nil, err
	}
	profile := tasks.EgressProfileTaskSafeV1
	var recipient *collaboration.PeerContext
	var access *store.TaskAccess
	membership, membershipErr := p.store.GetWorkspaceMembership(ctx, authorizedOffer.ShareID)
	isHomeDelivery := membershipErr == nil && membership.Status == store.WorkspaceShareStatusActive &&
		membership.HomePeerID == requesterPeerID && membership.LocalWorkspaceID == t.WorkspaceID &&
		(hasCapability(membership.Capabilities, store.CapabilityTasksPublish) ||
			(hasCapability(membership.Capabilities, store.CapabilityWorkspaceView) &&
				hasCapability(membership.Capabilities, store.CapabilityTasksCreate)) ||
			(authorizedOffer.IsDirectAssign && t.OriginPeerID == membership.HomePeerID &&
				hasCapability(membership.Capabilities, store.CapabilityWorkspaceView) &&
				hasCapability(membership.Capabilities, store.CapabilityTasksEdit)))
	if !isHomeDelivery {
		var err error
		recipient, access, err = p.authorizer.AuthorizeTaskRead(ctx, requesterPeerID, t.ID)
		if err != nil || access.ShareID != authorizedOffer.ShareID {
			return nil, p2p.ErrTaskNotFound
		}
		if policy, policyErr := p.store.GetWorkspacePublicationPolicy(ctx, access.ShareID); policyErr == nil {
			profile = policy.EgressProfile
		}
	}
	payload, err := tasks.BuildSafeTaskPayload(t, profile)
	if err != nil {
		return nil, err
	}
	if isHomeDelivery {
		payload.ShareID = membership.ShareID
		payload.AccessEpoch = membership.AccessEpoch
		payload.Visibility = store.TaskVisibilityPrivate
		payload.VisibilityEpoch = 1
	} else {
		payload.ShareID = access.ShareID
		payload.AccessEpoch = recipient.AccessEpoch
		payload.Visibility = access.Visibility
		payload.VisibilityEpoch = access.VisibilityEpoch
		payload.AudiencePrincipalIDs = append([]string(nil), access.AudiencePrincipalIDs...)
	}
	projection, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(projection)
	if !isHomeDelivery {
		if err := p.store.RecordTaskDisclosure(ctx, &store.TaskDisclosure{
			TaskID: t.ID, RecipientPrincipalID: recipient.Principal.ID,
			RecipientDeviceID: recipient.Device.ID, RecipientPeerID: requesterPeerID,
			ProjectionSHA256: hex.EncodeToString(sum[:]), ProjectionBytes: int64(len(projection)),
			EgressProfile: profile, DisclosedAt: time.Now().UTC(),
		}); err != nil {
			return nil, err
		}
	}
	return payload, nil
}

type principalPairedLookup struct {
	authorizer  *collaboration.Authorizer
	memberships store.CollaborationMembershipStore
}

func (l *principalPairedLookup) GetPairedPeer(ctx context.Context, peerID string) (p2p.PairedPeer, error) {
	if l == nil {
		return p2p.PairedPeer{}, collaboration.ErrUnauthenticated
	}
	if l.authorizer != nil {
		if _, err := l.authorizer.ResolvePeer(ctx, peerID); err == nil {
			return p2p.PairedPeer{PeerID: peerID}, nil
		} else if !errors.Is(err, collaboration.ErrUnauthenticated) {
			return p2p.PairedPeer{}, err
		}
	}
	if l.memberships != nil {
		if allowed, err := l.memberships.IsActiveWorkspaceHome(ctx, peerID); err != nil {
			return p2p.PairedPeer{}, err
		} else if allowed {
			return p2p.PairedPeer{PeerID: peerID}, nil
		}
	}
	return p2p.PairedPeer{}, collaboration.ErrUnauthenticated
}

func hasCapability(capabilities []string, wanted string) bool {
	for _, capability := range capabilities {
		if capability == wanted {
			return true
		}
	}
	return false
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
