package main

import (
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

func TestHumanTaskNotificationCreatedOnly(t *testing.T) {
	at := time.Date(2026, 6, 27, 12, 30, 0, 0, time.UTC)
	task := &store.Task{
		ID:                 "task-1",
		WorkspaceID:        "ws-1",
		Title:              "Approve memory promotion",
		AssigneeOriginKind: store.TaskAssigneeHuman,
		Priority:           "urgent",
	}

	got, ok := humanTaskNotification(tasks.Event{
		Kind: tasks.EventTaskCreated,
		Task: task,
		At:   at,
	})
	if !ok {
		t.Fatal("expected created human task to produce notification")
	}
	if got.Title != "Human task created" {
		t.Fatalf("title = %q, want created title", got.Title)
	}
	if got.Kind != tasks.EventTaskCreated {
		t.Fatalf("kind = %q, want %q", got.Kind, tasks.EventTaskCreated)
	}
	if got.Priority != "urgent" {
		t.Fatalf("priority = %q, want urgent", got.Priority)
	}
	if got.Link != "/app?task=task-1" {
		t.Fatalf("link = %q, want task deep-link", got.Link)
	}
}

func TestHumanTaskNotificationIgnoresUpdates(t *testing.T) {
	task := &store.Task{
		ID:                 "task-1",
		WorkspaceID:        "ws-1",
		Title:              "Approve memory promotion",
		AssigneeOriginKind: store.TaskAssigneeHuman,
	}
	if _, ok := humanTaskNotification(tasks.Event{Kind: tasks.EventTaskUpdated, Task: task}); ok {
		t.Fatal("human task updates should stay on SSE only, not top-banner notifications")
	}
	if _, ok := humanTaskNotification(tasks.Event{Kind: tasks.EventTaskClaimed, Task: task}); ok {
		t.Fatal("human task claims should stay on SSE only, not top-banner notifications")
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
}
