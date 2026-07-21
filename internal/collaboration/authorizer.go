// Package collaboration centralizes P2P principal and workspace authorization.
// Transport pairing is intentionally not an authorization input.
package collaboration

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

var (
	ErrUnauthenticated = errors.New("collaboration: peer identity is not active")
	ErrDenied          = errors.New("collaboration: capability denied")
)

type Store interface {
	ResolveActivePrincipalForPeer(ctx context.Context, peerID string) (*store.PrincipalDevice, *store.Principal, error)
	GetWorkspaceShare(ctx context.Context, shareID string) (*store.WorkspaceShare, error)
	GetWorkspaceShareByLocalWorkspaceID(ctx context.Context, workspaceID string) (*store.WorkspaceShare, error)
	HasWorkspaceCapability(ctx context.Context, principalID, shareID, capability string, at time.Time) (bool, error)
	CanPrincipalReadTask(ctx context.Context, principalID, taskID string, at time.Time) (bool, error)
	GetTaskAccess(ctx context.Context, taskID string) (*store.TaskAccess, error)
}

type PeerContext struct {
	PeerID      string
	Principal   *store.Principal
	Device      *store.PrincipalDevice
	Share       *store.WorkspaceShare
	AccessEpoch int64
}

type Authorizer struct {
	store Store
	now   func() time.Time
}

func NewAuthorizer(s Store) *Authorizer {
	return &Authorizer{store: s, now: func() time.Time { return time.Now().UTC() }}
}

func (a *Authorizer) ResolvePeer(ctx context.Context, peerID string) (*PeerContext, error) {
	if a == nil || a.store == nil || peerID == "" {
		return nil, ErrUnauthenticated
	}
	device, principal, err := a.store.ResolveActivePrincipalForPeer(ctx, peerID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrUnauthenticated
		}
		return nil, fmt.Errorf("resolve peer principal: %w", err)
	}
	return &PeerContext{PeerID: peerID, Principal: principal, Device: device}, nil
}

func (a *Authorizer) AuthorizeWorkspace(
	ctx context.Context,
	peerID string,
	shareID string,
	capabilities ...string,
) (*PeerContext, error) {
	peerContext, err := a.ResolvePeer(ctx, peerID)
	if err != nil {
		return nil, err
	}
	share, err := a.store.GetWorkspaceShare(ctx, shareID)
	if err != nil || share.Status != store.WorkspaceShareStatusActive {
		return nil, ErrDenied
	}
	if err := a.authorizeCapabilities(ctx, peerContext.Principal.ID, share.ShareID, capabilities); err != nil {
		return nil, err
	}
	peerContext.Share = share
	peerContext.AccessEpoch = share.AccessEpoch
	return peerContext, nil
}

func (a *Authorizer) AuthorizeLocalWorkspace(
	ctx context.Context,
	peerID string,
	workspaceID string,
	capabilities ...string,
) (*PeerContext, error) {
	if a == nil || a.store == nil || workspaceID == "" {
		return nil, ErrDenied
	}
	share, err := a.store.GetWorkspaceShareByLocalWorkspaceID(ctx, workspaceID)
	if err != nil || share.Status != store.WorkspaceShareStatusActive {
		return nil, ErrDenied
	}
	return a.AuthorizeWorkspace(ctx, peerID, share.ShareID, capabilities...)
}

func (a *Authorizer) AuthorizeTaskRead(ctx context.Context, peerID, taskID string) (*PeerContext, *store.TaskAccess, error) {
	peerContext, err := a.ResolvePeer(ctx, peerID)
	if err != nil {
		return nil, nil, err
	}
	allowed, err := a.store.CanPrincipalReadTask(ctx, peerContext.Principal.ID, taskID, a.now())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, nil, ErrDenied
		}
		return nil, nil, fmt.Errorf("task access check: %w", err)
	}
	if !allowed {
		return nil, nil, ErrDenied
	}
	access, err := a.store.GetTaskAccess(ctx, taskID)
	if err != nil || access.ShareID == "" {
		return nil, nil, ErrDenied
	}
	share, err := a.store.GetWorkspaceShare(ctx, access.ShareID)
	if err != nil || share.Status != store.WorkspaceShareStatusActive {
		return nil, nil, ErrDenied
	}
	peerContext.Share = share
	peerContext.AccessEpoch = share.AccessEpoch
	return peerContext, access, nil
}

func (a *Authorizer) authorizeCapabilities(ctx context.Context, principalID, shareID string, capabilities []string) error {
	for _, capability := range capabilities {
		if !store.ValidWorkspaceCapability(capability) {
			return ErrDenied
		}
		allowed, err := a.store.HasWorkspaceCapability(ctx, principalID, shareID, capability, a.now())
		if err != nil {
			return fmt.Errorf("authorize %s: %w", capability, err)
		}
		if !allowed {
			return ErrDenied
		}
	}
	return nil
}
