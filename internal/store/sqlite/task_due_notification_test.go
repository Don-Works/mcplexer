package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

func TestTaskDueNotificationReceiptRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := newMemDB(t)
	workspaceID := seedWorkspace(t, db, "due-receipt")
	dueAt := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	task := &store.Task{WorkspaceID: workspaceID, Title: "Review launch", DueAt: &dueAt}
	if err := db.CreateTask(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}

	seen, err := db.HasTaskDueNotification(ctx, task.ID, dueAt)
	if err != nil || seen {
		t.Fatalf("initial receipt = (%v, %v), want false", seen, err)
	}
	if err := db.RecordTaskDueNotification(ctx, task.ID, dueAt, "task_due:1", dueAt); err != nil {
		t.Fatalf("record receipt: %v", err)
	}
	seen, err = db.HasTaskDueNotification(ctx, task.ID, dueAt)
	if err != nil || !seen {
		t.Fatalf("stored receipt = (%v, %v), want true", seen, err)
	}
	if err := db.RecordTaskDueNotification(ctx, task.ID, dueAt, "task_due:duplicate", dueAt); err != nil {
		t.Fatalf("duplicate receipt should be idempotent: %v", err)
	}
	seen, err = db.HasTaskDueNotification(ctx, task.ID, dueAt.Add(time.Hour))
	if err != nil || seen {
		t.Fatalf("changed due time receipt = (%v, %v), want false", seen, err)
	}
}
