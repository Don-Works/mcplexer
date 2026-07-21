package tasks_test

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
	"github.com/don-works/mcplexer/internal/tasks"
)

// shareWorkspace turns wsID into a collaboration-shared workspace with a local
// owner principal, the minimum for GetTaskAccess to report an active share.
func shareWorkspace(t *testing.T, db *sqlite.DB, wsID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	owner := &store.Principal{ID: "local-owner", Kind: store.PrincipalKindPerson,
		DisplayName: "Owner", Status: store.PrincipalStatusActive, IsLocalOwner: true, CreatedAt: now}
	if err := db.CreatePrincipal(ctx, owner); err != nil {
		t.Fatalf("create owner principal: %v", err)
	}
	share := &store.WorkspaceShare{ShareID: "share-1", LocalWorkspaceID: wsID,
		HomePeerID: "self-peer", OwnerPrincipalID: owner.ID, CreatedAt: now}
	if err := db.CreateWorkspaceShare(ctx, share); err != nil {
		t.Fatalf("create workspace share: %v", err)
	}
}

func mintTask(t *testing.T, svc *tasks.Service, wsID string) *store.Task {
	t.Helper()
	task, err := svc.Create(context.Background(), tasks.CreateOptions{
		WorkspaceID: wsID, Title: "incident", Status: "open",
		SourceKind: store.TaskSourceAgent, ActorKind: "system",
	})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	if task.Visibility != store.TaskVisibilityPrivate {
		t.Fatalf("newly created task should be private, got %q", task.Visibility)
	}
	return task
}

// TestPublishSystemTask_WidensInSharedWorkspace: a shared workspace has a peer,
// so a system task is widened to workspace visibility (replicable), bypassing
// the agent ceiling/approval gate.
func TestPublishSystemTask_WidensInSharedWorkspace(t *testing.T) {
	ctx := context.Background()
	svc, db, wsID := newSvc(t)
	shareWorkspace(t, db, wsID)
	task := mintTask(t, svc, wsID)

	if err := svc.PublishSystemTask(ctx, task.ID); err != nil {
		t.Fatalf("publish: %v", err)
	}
	got, err := svc.Get(ctx, wsID, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Visibility != store.TaskVisibilityWorkspace {
		t.Fatalf("shared workspace task must widen to workspace, got %q", got.Visibility)
	}

	// Idempotent: a second call (the ensurer's convergence path) leaves it
	// widened and does not error.
	if err := svc.PublishSystemTask(ctx, task.ID); err != nil {
		t.Fatalf("second publish: %v", err)
	}
	got, _ = svc.Get(ctx, wsID, task.ID)
	if got.Visibility != store.TaskVisibilityWorkspace {
		t.Fatalf("task should stay widened, got %q", got.Visibility)
	}
}

// TestPublishSystemTask_NonSharedStaysPrivate: no active share means no peer to
// replicate to; widening there would change semantics for no benefit, so it is a
// no-op and the task stays private.
func TestPublishSystemTask_NonSharedStaysPrivate(t *testing.T) {
	ctx := context.Background()
	svc, _, wsID := newSvc(t)
	task := mintTask(t, svc, wsID)

	if err := svc.PublishSystemTask(ctx, task.ID); err != nil {
		t.Fatalf("publish (non-shared must no-op): %v", err)
	}
	got, err := svc.Get(ctx, wsID, task.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Visibility != store.TaskVisibilityPrivate {
		t.Fatalf("non-shared task must stay private, got %q", got.Visibility)
	}
}
