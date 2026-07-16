package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/collaboration"
	"github.com/don-works/mcplexer/internal/p2p"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

type apiCollaborationTransport struct{ peerID string }

func (t apiCollaborationTransport) LocalPeerID() string { return t.peerID }
func (apiCollaborationTransport) LocalAddrs() []string  { return []string{"/ip4/127.0.0.1/tcp/14001"} }
func (apiCollaborationTransport) Join(context.Context, p2p.CollaborationJoinOptions) (*p2p.CollaborationJoinResult, error) {
	return nil, errors.New("not used")
}

type recordingCollaborationSyncer struct{ peers []string }

func (s *recordingCollaborationSyncer) SyncPeerNow(peerID string) error {
	s.peers = append(s.peers, peerID)
	return nil
}

func TestCollaborationSnapshotProvisionsNewWorkspaceDefaultDeny(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	manager := collaboration.NewManager(db, apiCollaborationTransport{peerID: "home-peer"})
	if _, err := manager.EnsureLocalOwner(ctx, &store.User{
		UserID: "owner", DisplayName: "Owner", IsSelf: true,
	}); err != nil {
		t.Fatal(err)
	}
	workspace := &store.Workspace{Name: "added after boot", RootPath: t.TempDir()}
	if err := db.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}

	handler := &collaborationHandler{manager: manager, store: db}
	recorder := httptest.NewRecorder()
	handler.snapshot(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/collaboration", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("snapshot status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var snapshot collaborationSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatal(err)
	}
	var found *collaborationWorkspaceView
	for i := range snapshot.Workspaces {
		if snapshot.Workspaces[i].LocalWorkspaceID == workspace.ID {
			found = &snapshot.Workspaces[i]
			break
		}
	}
	if found == nil || len(found.Grants) != 0 || found.Policy == nil ||
		found.Policy.DefaultVisibility != store.TaskVisibilityPrivate {
		t.Fatalf("snapshot workspace = %#v", found)
	}
}

func TestCollaborationMembershipManualSyncUsesRecordedHome(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspace := &store.Workspace{ID: "shared:remote", Name: "Remote", RootPath: ""}
	if err := db.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertWorkspaceMembership(ctx, &store.WorkspaceMembership{
		ShareID: "remote", HomePeerID: "verified-home", RemoteWorkspaceID: "home-workspace",
		LocalWorkspaceID: workspace.ID, WorkspaceName: workspace.Name,
		Capabilities: []string{store.CapabilityWorkspaceView, store.CapabilityTasksRead},
		AccessEpoch:  1, Status: store.WorkspaceShareStatusActive,
		JoinedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	syncer := &recordingCollaborationSyncer{}
	handler := &collaborationHandler{store: db, syncer: syncer}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/collaboration/memberships/remote/sync", nil)
	request.SetPathValue("share_id", "remote")
	handler.syncMembership(recorder, request)
	if recorder.Code != http.StatusOK || len(syncer.peers) != 1 || syncer.peers[0] != "verified-home" {
		t.Fatalf("sync status=%d peers=%v body=%s", recorder.Code, syncer.peers, recorder.Body.String())
	}
}

func TestCollaborationAdvertisesOnlyOperationalWireCapabilities(t *testing.T) {
	for _, capability := range []string{
		store.CapabilityWorkspaceView, store.CapabilityTasksRead,
		store.CapabilityTasksCreate, store.CapabilityTasksPublish,
		store.CapabilityTasksEdit,
	} {
		if err := validateOperationalCapabilities([]string{capability}); err != nil {
			t.Fatalf("operational capability %q rejected: %v", capability, err)
		}
	}
	for _, dormant := range []string{
		store.CapabilityTasksComment, store.CapabilityTasksAssign,
		store.CapabilityTasksDelete, store.CapabilityWorkspaceAdmin,
	} {
		if err := validateOperationalCapabilities([]string{dormant}); err == nil {
			t.Fatalf("dormant capability %q was advertised as operational", dormant)
		}
	}
}
