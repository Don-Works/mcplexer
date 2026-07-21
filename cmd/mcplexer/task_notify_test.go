package main

import (
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

func TestHumanTaskNotificationCreatedAssignment(t *testing.T) {
	at := time.Date(2026, 6, 27, 12, 30, 0, 0, time.UTC)
	task := &store.Task{
		ID:                 "task-1",
		WorkspaceID:        "ws-1",
		Title:              "Approve memory promotion",
		AssigneeOriginKind: store.TaskAssigneeHuman,
		AssigneeUserID:     "user-1",
		Priority:           "urgent",
		AssignedAt:         &at,
	}

	got, ok := humanTaskNotification(tasks.Event{
		Kind: tasks.EventTaskCreated,
		Task: task,
		At:   at,
	})
	if !ok {
		t.Fatal("expected created human task to produce notification")
	}
	if got.Title != "Task assigned" {
		t.Fatalf("title = %q, want assignment title", got.Title)
	}
	if got.Kind != "task_assigned" {
		t.Fatalf("kind = %q, want task_assigned", got.Kind)
	}
	if got.Priority != "high" {
		t.Fatalf("priority = %q, want high", got.Priority)
	}
	if got.Link != "/tasks/task-1?workspace=ws-1" {
		t.Fatalf("link = %q, want task deep-link", got.Link)
	}
}

func TestHumanTaskNotificationOnlyUsesMeaningfulUpdates(t *testing.T) {
	task := &store.Task{
		ID:                 "task-1",
		WorkspaceID:        "ws-1",
		Title:              "Approve memory promotion",
		AssigneeOriginKind: store.TaskAssigneeHuman,
		AssigneeUserID:     "user-1",
	}
	if _, ok := humanTaskNotification(tasks.Event{Kind: tasks.EventTaskUpdated, Task: task}); ok {
		t.Fatal("generic human task updates should stay in the Signal tray")
	}
	if _, ok := humanTaskNotification(tasks.Event{Kind: tasks.EventTaskClaimed, Task: task}); ok {
		t.Fatal("human task claims should stay in the Signal tray")
	}
	got, ok := humanTaskNotification(tasks.Event{
		Kind: tasks.EventTaskUpdated, Task: task, AssigneeChanged: true,
	})
	if !ok || got.Kind != "task_assigned" {
		t.Fatalf("human reassignment should notify, got (%+v, %v)", got, ok)
	}
}

func TestHumanTaskNotificationCallsOutOverdueAssignment(t *testing.T) {
	at := time.Date(2026, 7, 11, 12, 0, 0, 0, time.UTC)
	due := at.Add(-time.Hour)
	task := &store.Task{
		ID: "task-1", Title: "Review launch", DueAt: &due,
		AssigneeOriginKind: store.TaskAssigneeHuman, AssigneeUserID: "user-1",
	}
	got, ok := humanTaskNotification(tasks.Event{
		Kind: tasks.EventTaskUpdated, Task: task, AssigneeChanged: true, At: at,
	})
	if !ok || got.Title != "Overdue task assigned" {
		t.Fatalf("overdue assignment = (%+v, %v)", got, ok)
	}
}

func TestHumanTaskNotificationIgnoresNonHumanTasks(t *testing.T) {
	task := &store.Task{
		ID:                 "task-1",
		WorkspaceID:        "ws-1",
		Title:              "Run worker",
		AssigneeOriginKind: store.TaskAssigneeLocal,
	}
	if _, ok := humanTaskNotification(tasks.Event{Kind: tasks.EventTaskCreated, Task: task}); ok {
		t.Fatal("non-human tasks should not produce human task notifications")
	}
	task.AssigneeOriginKind = store.TaskAssigneeHuman
	if _, ok := humanTaskNotification(tasks.Event{Kind: tasks.EventTaskCreated, Task: task}); ok {
		t.Fatal("human marker without an assigned user should not notify")
	}
}
