package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
)

type fakeHumanTaskLister struct {
	rows []store.Task
}

func (f *fakeHumanTaskLister) List(_ context.Context, filter store.TaskFilter) ([]store.Task, error) {
	if filter.Offset >= len(f.rows) {
		return nil, nil
	}
	end := filter.Offset + filter.Limit
	if end > len(f.rows) {
		end = len(f.rows)
	}
	return append([]store.Task(nil), f.rows[filter.Offset:end]...), nil
}

type fakeTaskDueReceipts struct {
	seen map[string]bool
}

func dueReceiptKey(taskID string, dueAt time.Time) string {
	return fmt.Sprintf("%s:%d", taskID, dueAt.UTC().Unix())
}

func (f *fakeTaskDueReceipts) HasTaskDueNotification(
	_ context.Context, taskID string, dueAt time.Time,
) (bool, error) {
	return f.seen[dueReceiptKey(taskID, dueAt)], nil
}

func (f *fakeTaskDueReceipts) RecordTaskDueNotification(
	_ context.Context, taskID string, dueAt time.Time, _ string, _ time.Time,
) error {
	f.seen[dueReceiptKey(taskID, dueAt)] = true
	return nil
}

func TestPublishDueHumanTaskNotificationsBatchesAndDedupes(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	overdue := now.Add(-10 * time.Minute)
	dueNow := now.Add(-30 * time.Second)
	future := now.Add(time.Hour)
	closedAt := now.Add(-time.Minute)
	lister := &fakeHumanTaskLister{rows: []store.Task{
		{
			ID: "critical", WorkspaceID: "ws-1", Title: "Approve production deploy",
			Priority: "critical", DueAt: &overdue, AssigneeOriginKind: store.TaskAssigneeHuman,
			AssigneeUserID: "user-1",
		},
		{
			ID: "normal", WorkspaceID: "ws-1", Title: "Review launch copy",
			Priority: "normal", DueAt: &dueNow, AssigneeOriginKind: store.TaskAssigneeHuman,
			AssigneeUserID: "user-1",
		},
		{
			ID: "future", DueAt: &future, AssigneeOriginKind: store.TaskAssigneeHuman,
			AssigneeUserID: "user-1",
		},
		{
			ID: "closed", DueAt: &overdue, ClosedAt: &closedAt,
			AssigneeOriginKind: store.TaskAssigneeHuman, AssigneeUserID: "user-1",
		},
	}}
	receipts := &fakeTaskDueReceipts{seen: map[string]bool{}}
	bus := notify.NewBus()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	count, err := publishDueHumanTaskNotifications(
		context.Background(), lister, receipts, bus, now,
	)
	if err != nil {
		t.Fatalf("publish due notifications: %v", err)
	}
	if count != 2 {
		t.Fatalf("count = %d, want 2", count)
	}
	select {
	case evt := <-ch:
		if evt.Title != "2 human tasks are overdue" {
			t.Fatalf("title = %q", evt.Title)
		}
		if evt.Priority != "critical" || evt.Kind != "task_due" {
			t.Fatalf("unexpected event semantics: %+v", evt)
		}
		if evt.Body != "Approve production deploy; Review launch copy" {
			t.Fatalf("body = %q", evt.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for due notification")
	}

	count, err = publishDueHumanTaskNotifications(
		context.Background(), lister, receipts, bus, now.Add(time.Minute),
	)
	if err != nil || count != 0 {
		t.Fatalf("second scan = (%d, %v), want deduped", count, err)
	}
	select {
	case evt := <-ch:
		t.Fatalf("received duplicate due notification: %+v", evt)
	default:
	}
}

func TestSingleDueTaskNotificationDeepLinks(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	due := now.Add(-30 * time.Second)
	task := store.Task{
		ID: "task-1", WorkspaceID: "ws-1", Title: "Review launch copy",
		DueAt: &due, AssigneeOriginKind: store.TaskAssigneeHuman, AssigneeUserID: "user-1",
	}
	evt := dueTaskNotification([]store.Task{task}, now)
	if evt.Title != "Task due now" || evt.Link != "/tasks/task-1?workspace=ws-1" {
		t.Fatalf("unexpected single-task notification: %+v", evt)
	}
	if evt.MessageID != "task_due:task-1:1783771170" {
		t.Fatalf("message id = %q", evt.MessageID)
	}
}

func TestOverdueAssignmentCoversImmediateDueNotification(t *testing.T) {
	now := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	due := now.Add(-time.Hour)
	task := &store.Task{ID: "task-1", DueAt: &due}
	receipts := &fakeTaskDueReceipts{seen: map[string]bool{}}
	recordAssignmentDueReceipt(context.Background(), task, notify.Event{
		MessageID: "task_assigned:task-1:1",
		CreatedAt: now,
	}, receipts)
	seen, err := receipts.HasTaskDueNotification(context.Background(), task.ID, due)
	if err != nil || !seen {
		t.Fatalf("overdue assignment receipt = (%v, %v), want true", seen, err)
	}
}
