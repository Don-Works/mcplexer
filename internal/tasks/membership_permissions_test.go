package tasks_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

func TestSharedWorkspaceLocalPermissionsMatchMembershipCapabilities(t *testing.T) {
	ctx := context.Background()
	db, err := sqlite.New(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workspace := &store.Workspace{Name: "remote mirror", RootPath: t.TempDir()}
	if err := db.CreateWorkspace(ctx, workspace); err != nil {
		t.Fatal(err)
	}
	membership := &store.WorkspaceMembership{
		ShareID: "remote-share", HomePeerID: "home-peer",
		RemoteWorkspaceID: "home-workspace", LocalWorkspaceID: workspace.ID,
		WorkspaceName: "Operations", Status: store.WorkspaceShareStatusActive,
		AccessEpoch: 1, JoinedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		Capabilities: []string{store.CapabilityWorkspaceView, store.CapabilityTasksRead},
	}
	if err := db.UpsertWorkspaceMembership(ctx, membership); err != nil {
		t.Fatal(err)
	}
	service := tasks.New(db)

	if _, err := service.Create(ctx, tasks.CreateOptions{
		WorkspaceID: workspace.ID, Title: "reader draft",
	}); !errors.Is(err, tasks.ErrWorkspaceMembershipDenied) {
		t.Fatalf("read-only member create error = %v", err)
	}

	mirror := &store.Task{
		ID: "01KXNC1C7XDJ2FFKMY0QQQQQQQ", WorkspaceID: workspace.ID,
		Title: "home task", SourceKind: store.TaskSourcePeerImport,
		OriginPeerID: membership.HomePeerID,
	}
	if err := db.CreateTask(ctx, mirror); err != nil {
		t.Fatal(err)
	}
	changed := "reader must not edit"
	if _, err := service.Update(ctx, workspace.ID, mirror.ID, tasks.UpdatePatch{Title: &changed}); !errors.Is(err, tasks.ErrWorkspaceMembershipDenied) {
		t.Fatalf("read-only member update error = %v", err)
	}
	if err := service.Delete(ctx, workspace.ID, mirror.ID); !errors.Is(err, tasks.ErrWorkspaceMembershipDenied) {
		t.Fatalf("read-only member delete error = %v", err)
	}
	if _, err := service.AppendNote(ctx, workspace.ID, mirror.ID, "local note", "session", "agent"); !errors.Is(err, tasks.ErrWorkspaceMembershipDenied) {
		t.Fatalf("read-only member comment error = %v", err)
	}

	membership.Capabilities = []string{
		store.CapabilityWorkspaceView, store.CapabilityTasksRead, store.CapabilityTasksEdit,
	}
	if err := db.UpsertWorkspaceMembership(ctx, membership); err != nil {
		t.Fatal(err)
	}
	changed = "editor may edit"
	if _, err := service.Update(ctx, workspace.ID, mirror.ID, tasks.UpdatePatch{Title: &changed}); err != nil {
		t.Fatalf("editor update: %v", err)
	}
	if _, err := service.Update(ctx, workspace.ID, mirror.ID, tasks.UpdatePatch{
		Assignee: &tasks.Assignee{SessionID: "session"},
	}); !errors.Is(err, tasks.ErrWorkspaceMembershipDenied) {
		t.Fatalf("editor without tasks.assign assignment error = %v", err)
	}

	membership.Capabilities = []string{store.CapabilityTasksPublish}
	if err := db.UpsertWorkspaceMembership(ctx, membership); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, tasks.CreateOptions{
		WorkspaceID: workspace.ID, Title: "machine finding",
	}); err != nil {
		t.Fatalf("publisher local draft: %v", err)
	}
}
