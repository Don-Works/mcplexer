package collaboration_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/collaboration"
	"github.com/don-works/mcplexer/internal/store"
)

type fakeStore struct {
	device    *store.PrincipalDevice
	principal *store.Principal
	share     *store.WorkspaceShare
	grants    map[string]bool
	task      *store.TaskAccess
	readTask  bool
}

func (f *fakeStore) ResolveActivePrincipalForPeer(context.Context, string) (*store.PrincipalDevice, *store.Principal, error) {
	if f.device == nil || f.principal == nil {
		return nil, nil, store.ErrNotFound
	}
	return f.device, f.principal, nil
}
func (f *fakeStore) GetWorkspaceShare(context.Context, string) (*store.WorkspaceShare, error) {
	if f.share == nil {
		return nil, store.ErrNotFound
	}
	return f.share, nil
}
func (f *fakeStore) GetWorkspaceShareByLocalWorkspaceID(context.Context, string) (*store.WorkspaceShare, error) {
	return f.GetWorkspaceShare(context.Background(), "")
}
func (f *fakeStore) HasWorkspaceCapability(_ context.Context, _, _ string, capability string, _ time.Time) (bool, error) {
	return f.grants[capability], nil
}
func (f *fakeStore) CanPrincipalReadTask(context.Context, string, string, time.Time) (bool, error) {
	return f.readTask, nil
}
func (f *fakeStore) GetTaskAccess(context.Context, string) (*store.TaskAccess, error) {
	if f.task == nil {
		return nil, store.ErrNotFound
	}
	return f.task, nil
}

func TestAuthorizerRequiresProofBoundPeerAndEveryExactCapability(t *testing.T) {
	base := &fakeStore{
		device:    &store.PrincipalDevice{ID: "device", PeerID: "peer"},
		principal: &store.Principal{ID: "principal", Status: store.PrincipalStatusActive},
		share:     &store.WorkspaceShare{ShareID: "share", Status: store.WorkspaceShareStatusActive, AccessEpoch: 3},
		grants:    map[string]bool{store.CapabilityWorkspaceView: true},
	}
	authorizer := collaboration.NewAuthorizer(base)
	if _, err := authorizer.AuthorizeWorkspace(context.Background(), "peer", "share", store.CapabilityWorkspaceView, store.CapabilityTasksRead); !errors.Is(err, collaboration.ErrDenied) {
		t.Fatalf("missing exact read capability error = %v", err)
	}
	base.grants[store.CapabilityTasksRead] = true
	ctx, err := authorizer.AuthorizeWorkspace(context.Background(), "peer", "share", store.CapabilityWorkspaceView, store.CapabilityTasksRead)
	if err != nil || ctx.AccessEpoch != 3 || ctx.Principal.ID != "principal" {
		t.Fatalf("authorized context = %#v, %v", ctx, err)
	}
	base.device = nil
	if _, err := authorizer.AuthorizeWorkspace(context.Background(), "peer", "share", store.CapabilityWorkspaceView); !errors.Is(err, collaboration.ErrUnauthenticated) {
		t.Fatalf("unverified device error = %v", err)
	}
}
