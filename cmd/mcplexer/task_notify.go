package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

func bridgeHumanTaskNotifications(
	ctx context.Context,
	bus *tasks.Bus,
	notifyBus *notify.Bus,
	dueReceipts taskDueReceiptStore,
) {
	if bus == nil || notifyBus == nil {
		return
	}
	ch, unsub := bus.Subscribe()
	go func() {
		defer unsub()
		for {
			select {
			case <-ctx.Done():
				return
			case evt, ok := <-ch:
				if !ok {
					return
				}
				if n, ok := humanTaskNotification(evt); ok {
					notifyBus.Publish(n)
					recordAssignmentDueReceipt(ctx, evt.Task, n, dueReceipts)
				}
			}
		}
	}()
}

func humanTaskNotification(evt tasks.Event) (notify.Event, bool) {
	if evt.Task == nil || evt.Task.DeletedAt != nil {
		return notify.Event{}, false
	}
	t := evt.Task
	if t.AssigneeOriginKind != store.TaskAssigneeHuman || strings.TrimSpace(t.AssigneeUserID) == "" {
		return notify.Event{}, false
	}
	if evt.Kind != tasks.EventTaskCreated && (evt.Kind != tasks.EventTaskUpdated || !evt.AssigneeChanged) {
		return notify.Event{}, false
	}
	at := evt.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	assignmentAt := at
	if t.AssignedAt != nil && !t.AssignedAt.IsZero() {
		assignmentAt = t.AssignedAt.UTC()
	}
	title := "Task assigned"
	if t.DueAt != nil && !t.DueAt.After(at) {
		title = "Overdue task assigned"
	}
	return notify.Event{
		MessageID: fmt.Sprintf("task_assigned:%s:%d", t.ID, assignmentAt.UnixNano()),
		Source:    "task",
		AgentName: "mcplexer",
		Role:      "task",
		Kind:      "task_assigned",
		Priority:  taskNotificationPriority(t.Priority),
		Title:     title,
		Body:      t.Title,
		Tags:      "task,human,assigned," + t.WorkspaceID,
		Link:      taskDetailLink(t),
		CreatedAt: at,
	}, true
}

func recordAssignmentDueReceipt(
	ctx context.Context,
	task *store.Task,
	evt notify.Event,
	receipts taskDueReceiptStore,
) {
	if task == nil || task.DueAt == nil || receipts == nil || task.DueAt.After(evt.CreatedAt) {
		return
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := receipts.RecordTaskDueNotification(
		c, task.ID, *task.DueAt, evt.MessageID, evt.CreatedAt,
	); err != nil {
		slog.Warn("record overdue assignment receipt failed", "task_id", task.ID, "error", err)
	}
}

func taskNotificationPriority(priority string) string {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "critical":
		return "critical"
	case "urgent", "high":
		return "high"
	default:
		return "normal"
	}
}

func taskDetailLink(t *store.Task) string {
	if t == nil || strings.TrimSpace(t.ID) == "" {
		return "/app"
	}
	link := "/tasks/" + url.PathEscape(t.ID)
	if ws := strings.TrimSpace(t.WorkspaceID); ws != "" {
		link += "?workspace=" + url.QueryEscape(ws)
	}
	return link
}
