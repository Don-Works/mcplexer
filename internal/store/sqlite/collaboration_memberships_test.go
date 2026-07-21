package sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestWorkspaceMembershipKeepsTaskVisibilityHomeAuthoritative(t *testing.T) {
	ctx := context.Background()
	db, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	now := time.Now().UTC()
	workspace := &store.Workspace{ID: "shared:visibility", Name: "Shared visibility", Source: "p2p"}
	if err := db.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	localOwner := &store.Principal{
		ID: "local-owner", Kind: store.PrincipalKindPerson, DisplayName: "Local owner",
		Status: store.PrincipalStatusActive, IsLocalOwner: true, CreatedAt: now,
	}
	remoteOwner := &store.Principal{
		ID: "remote-owner", Kind: store.PrincipalKindPerson, DisplayName: "Remote owner",
		Status: store.PrincipalStatusActive, CreatedAt: now,
	}
	for _, principal := range []*store.Principal{localOwner, remoteOwner} {
		if err := db.CreatePrincipal(ctx, principal); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.UpsertWorkspaceMembership(ctx, &store.WorkspaceMembership{
		ShareID: "visibility-share", HomePeerID: "home-peer",
		RemoteWorkspaceID: "home-workspace", LocalWorkspaceID: workspace.ID,
		WorkspaceName: workspace.Name,
		Capabilities:  []string{store.CapabilityWorkspaceView, store.CapabilityTasksRead},
		AccessEpoch:   1, Status: store.WorkspaceShareStatusActive,
		JoinedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	task := &store.Task{
		ID: "mirrored-task", WorkspaceID: workspace.ID, Title: "Home task",
		OwnerPrincipalID: remoteOwner.ID, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	access, err := db.GetTaskAccess(ctx, task.ID)
	if err != nil || access.VisibilityEditable {
		t.Fatalf("mirror visibility access = %#v, %v", access, err)
	}
	_, err = db.SetTaskVisibility(ctx, store.TaskVisibilityChange{
		TaskID: task.ID, Visibility: store.TaskVisibilityWorkspace,
		ActorPrincipalID: localOwner.ID, At: now.Add(time.Minute),
	})
	if !errors.Is(err, store.ErrConflict) {
		t.Fatalf("mirror visibility update error = %v", err)
	}
}

func TestWorkspaceMembershipLifecycleAndMonotonicCursor(t *testing.T) {
	ctx := context.Background()
	db, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspace := &store.Workspace{
		ID: "shared:share-01", Name: "Shared incidents", RootPath: "",
		Tags: json.RawMessage(`["p2p-shared"]`), Source: "p2p",
	}
	if err := db.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	membership := &store.WorkspaceMembership{
		ShareID: "share-01", HomePeerID: "peer-home",
		RemoteWorkspaceID: "workspace-home", LocalWorkspaceID: workspace.ID,
		WorkspaceName: "Incidents",
		Capabilities:  []string{store.CapabilityTasksRead, store.CapabilityWorkspaceView},
		AccessEpoch:   3, UpdatedAt: time.Now().UTC(),
	}
	if err := db.UpsertWorkspaceMembership(ctx, membership); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetWorkspaceMembershipByLocalWorkspaceID(ctx, workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ShareID != membership.ShareID || len(got.Capabilities) != 2 || got.CursorHLC != "" {
		t.Fatalf("membership = %#v", got)
	}
	if active, err := db.IsActiveWorkspaceHome(ctx, "peer-home"); err != nil || !active {
		t.Fatalf("active home = %v, %v", active, err)
	}
	if err := db.AdvanceWorkspaceMembershipCursor(ctx, membership.ShareID, "0002", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := db.AdvanceWorkspaceMembershipCursor(ctx, membership.ShareID, "0001", time.Now()); err != nil {
		t.Fatal(err)
	}
	got, err = db.GetWorkspaceMembership(ctx, membership.ShareID)
	if err != nil || got.CursorHLC != "0002" {
		t.Fatalf("monotonic cursor = %#v, %v", got, err)
	}
	if err := db.RevokeWorkspaceMembership(ctx, membership.ShareID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if active, err := db.IsActiveWorkspaceHome(ctx, "peer-home"); err != nil || active {
		t.Fatalf("revoked home = %v, %v", active, err)
	}
}

func TestWorkspaceMembershipRejectsUnknownCapability(t *testing.T) {
	ctx := context.Background()
	db, err := New(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspace := &store.Workspace{ID: "shared:bad", Name: "Bad", Tags: json.RawMessage(`[]`)}
	if err := db.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	err = db.UpsertWorkspaceMembership(ctx, &store.WorkspaceMembership{
		ShareID: "bad", HomePeerID: "home", RemoteWorkspaceID: "remote",
		LocalWorkspaceID: workspace.ID, WorkspaceName: "Bad",
		Capabilities: []string{"tasks.*"}, AccessEpoch: 1,
	})
	if err == nil {
		t.Fatal("wildcard membership capability was accepted")
	}
}
