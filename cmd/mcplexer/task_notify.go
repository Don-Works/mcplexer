package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

func bridgeHumanTaskNotifications(ctx context.Context, bus *tasks.Bus, notifyBus *notify.Bus) {
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
	if t.AssigneeOriginKind != store.TaskAssigneeHuman {
		return notify.Event{}, false
	}
	if evt.Kind != tasks.EventTaskCreated {
		return notify.Event{}, false
	}
	at := evt.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	priority := strings.ToLower(strings.TrimSpace(t.Priority))
	if priority == "" {
		priority = "normal"
	}
	return notify.Event{
		MessageID: fmt.Sprintf("%s:%s:%d", evt.Kind, t.ID, at.UnixNano()),
		Source:    "task",
		AgentName: "mcplexer",
		Role:      "task",
		Kind:      evt.Kind,
		Priority:  priority,
		Title:     "Human task created",
		Body:      t.Title,
		Tags:      "task,human," + t.WorkspaceID,
		Link:      taskDetailLink(t),
		CreatedAt: at,
	}, true
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
