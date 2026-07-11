package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
)

const humanTaskDueInterval = time.Minute

type humanTaskLister interface {
	List(context.Context, store.TaskFilter) ([]store.Task, error)
}

type taskDueReceiptStore interface {
	HasTaskDueNotification(context.Context, string, time.Time) (bool, error)
	RecordTaskDueNotification(context.Context, string, time.Time, string, time.Time) error
}

func humanTaskDueNotificationLoop(
	ctx context.Context,
	tasks humanTaskLister,
	receipts taskDueReceiptStore,
	bus *notify.Bus,
) {
	if tasks == nil || receipts == nil || bus == nil {
		return
	}
	run := func(now time.Time) {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		count, err := publishDueHumanTaskNotifications(c, tasks, receipts, bus, now.UTC())
		if err != nil {
			slog.Warn("human task due notification scan failed", "error", err)
		}
		if count > 0 {
			slog.Info("human task due notifications published", "tasks", count)
		}
	}

	run(time.Now())
	ticker := time.NewTicker(humanTaskDueInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			run(now)
		}
	}
}

func publishDueHumanTaskNotifications(
	ctx context.Context,
	tasks humanTaskLister,
	receipts taskDueReceiptStore,
	bus *notify.Bus,
	now time.Time,
) (int, error) {
	due, err := listDueHumanTasks(ctx, tasks, now)
	if err != nil {
		return 0, err
	}
	pending := make([]store.Task, 0, len(due))
	for i := range due {
		seen, err := receipts.HasTaskDueNotification(ctx, due[i].ID, *due[i].DueAt)
		if err != nil {
			return 0, fmt.Errorf("check task %s due receipt: %w", due[i].ID, err)
		}
		if !seen {
			pending = append(pending, due[i])
		}
	}
	if len(pending) == 0 {
		return 0, nil
	}

	sortDueTasks(pending)
	evt := dueTaskNotification(pending, now)
	bus.Publish(evt)
	var receiptErr error
	for i := range pending {
		if err := receipts.RecordTaskDueNotification(
			ctx, pending[i].ID, *pending[i].DueAt, evt.MessageID, now,
		); err != nil && receiptErr == nil {
			receiptErr = fmt.Errorf("record task %s due receipt: %w", pending[i].ID, err)
		}
	}
	return len(pending), receiptErr
}

func listDueHumanTasks(ctx context.Context, tasks humanTaskLister, now time.Time) ([]store.Task, error) {
	const pageSize = 500
	openOnly := false
	var out []store.Task
	for offset := 0; ; offset += pageSize {
		rows, err := tasks.List(ctx, store.TaskFilter{
			OnlyTerminal:       &openOnly,
			AssigneeOriginKind: store.TaskAssigneeHuman,
			Limit:              pageSize,
			Offset:             offset,
		})
		if err != nil {
			return nil, fmt.Errorf("list open human tasks: %w", err)
		}
		for i := range rows {
			if humanTaskIsDue(&rows[i], now) {
				out = append(out, rows[i])
			}
		}
		if len(rows) < pageSize {
			return out, nil
		}
	}
}

func humanTaskIsDue(task *store.Task, now time.Time) bool {
	return task != nil && task.DeletedAt == nil && task.ClosedAt == nil &&
		task.AssigneeOriginKind == store.TaskAssigneeHuman &&
		strings.TrimSpace(task.AssigneeUserID) != "" && task.DueAt != nil &&
		!task.DueAt.After(now)
}

func dueTaskNotification(tasks []store.Task, now time.Time) notify.Event {
	if len(tasks) == 1 {
		return singleDueTaskNotification(&tasks[0], now)
	}
	title := fmt.Sprintf("%d human tasks are due", len(tasks))
	if tasksOverdue(tasks, now) {
		title = fmt.Sprintf("%d human tasks are overdue", len(tasks))
	}
	return notify.Event{
		MessageID: dueTaskBatchMessageID(tasks),
		Source:    "task",
		AgentName: "mcplexer",
		Role:      "task",
		Kind:      "task_due",
		Priority:  dueTaskPriority(tasks),
		Title:     title,
		Body:      dueTaskBatchBody(tasks),
		Tags:      "task,human,due,batch",
		Link:      "/app?source=notification",
		CreatedAt: now,
	}
}

func singleDueTaskNotification(task *store.Task, now time.Time) notify.Event {
	title := "Task due now"
	if now.Sub(task.DueAt.UTC()) > humanTaskDueInterval {
		title = "Task overdue"
	}
	return notify.Event{
		MessageID: fmt.Sprintf("task_due:%s:%d", task.ID, task.DueAt.UTC().Unix()),
		Source:    "task",
		AgentName: "mcplexer",
		Role:      "task",
		Kind:      "task_due",
		Priority:  dueTaskPriority([]store.Task{*task}),
		Title:     title,
		Body:      task.Title,
		Tags:      "task,human,due," + task.WorkspaceID,
		Link:      taskDetailLink(task),
		CreatedAt: now,
	}
}

func sortDueTasks(tasks []store.Task) {
	sort.Slice(tasks, func(i, j int) bool {
		pi := duePriorityRank(tasks[i].Priority)
		pj := duePriorityRank(tasks[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return tasks[i].DueAt.Before(*tasks[j].DueAt)
	})
}

func duePriorityRank(priority string) int {
	switch strings.ToLower(strings.TrimSpace(priority)) {
	case "critical":
		return 0
	case "urgent", "high":
		return 1
	default:
		return 2
	}
}

func dueTaskPriority(tasks []store.Task) string {
	for i := range tasks {
		if strings.EqualFold(strings.TrimSpace(tasks[i].Priority), "critical") {
			return "critical"
		}
	}
	return "high"
}

func tasksOverdue(tasks []store.Task, now time.Time) bool {
	for i := range tasks {
		if now.Sub(tasks[i].DueAt.UTC()) > humanTaskDueInterval {
			return true
		}
	}
	return false
}

func dueTaskBatchBody(tasks []store.Task) string {
	limit := 2
	if len(tasks) < limit {
		limit = len(tasks)
	}
	titles := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		title := strings.Join(strings.Fields(tasks[i].Title), " ")
		if title == "" {
			title = tasks[i].ID
		}
		titles = append(titles, title)
	}
	body := strings.Join(titles, "; ")
	if extra := len(tasks) - limit; extra > 0 {
		body += fmt.Sprintf("; +%d more", extra)
	}
	return body
}

func dueTaskBatchMessageID(tasks []store.Task) string {
	fingerprints := make([]string, 0, len(tasks))
	for i := range tasks {
		fingerprints = append(fingerprints,
			fmt.Sprintf("%s:%d", tasks[i].ID, tasks[i].DueAt.UTC().Unix()))
	}
	sort.Strings(fingerprints)
	sum := sha256.Sum256([]byte(strings.Join(fingerprints, "\n")))
	return fmt.Sprintf("task_due_batch:%x", sum[:8])
}
