package escalate

import (
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
)

type humanClaim struct {
	key      string
	previous humanIncidentState
	current  humanIncidentState
	existed  bool
}

type humanIncidentState struct {
	taskID      string
	interrupted bool
}

func humanIncidentLink(n distill.Notification) string {
	if n.TaskID != "" {
		link := "/tasks/" + url.PathEscape(n.TaskID)
		if n.WorkspaceID != "" {
			link += "?workspace=" + url.QueryEscape(n.WorkspaceID)
		}
		return link
	}
	if n.WorkspaceID != "" {
		return "/monitoring?workspace=" + url.QueryEscape(n.WorkspaceID)
	}
	return "/monitoring"
}

func humanIncidentEvent(workspaceName string, n distill.Notification, at time.Time) notify.Event {
	source := firstNonEmpty(n.SourceName, n.RemoteHostName)
	body := "New critical monitoring incident"
	if source != "" {
		body += " from " + source
	}
	if n.TaskID != "" {
		body += ". Tap to open task " + n.TaskID + "."
	} else {
		body += ". Tap to review Monitoring."
	}
	idKind, id := "template", n.TemplateID
	if n.IncidentID != "" {
		idKind, id = "incident", n.IncidentID
	}
	if n.TaskID != "" {
		idKind, id = "task", n.TaskID
	}
	return notify.Event{
		MessageID: "monitoring_critical:" + n.WorkspaceID + ":" + idKind + ":" + id,
		Source:    "monitoring", AgentName: "log-watch", Role: "incident",
		Kind: "monitoring_critical_new", Priority: "critical",
		Title: "CRITICAL · " + firstNonEmpty(workspaceName, "unknown-system") + " · " + strings.TrimSpace(n.Title),
		Body:  body, Tags: "monitoring,logwatch,critical,new," + n.WorkspaceID,
		Link: humanIncidentLink(n), CreatedAt: at.UTC(),
	}
}

func humanIncidentKey(n distill.Notification) string {
	if n.IncidentID != "" {
		return n.WorkspaceID + "/incident/" + n.IncidentID
	}
	if n.TemplateID != "" {
		return n.WorkspaceID + "/" + n.TemplateID
	}
	return n.WorkspaceID + "/task/" + n.TaskID
}

func (d *Dispatcher) claimHumanIncident(n distill.Notification) (HumanPublisher, humanClaim, bool) {
	if !isHumanCandidate(n) {
		return nil, humanClaim{}, false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.human == nil {
		return nil, humanClaim{}, false
	}
	return d.claimHumanIncidentLocked(humanIncidentKey(n), n.TaskID)
}

func (d *Dispatcher) claimHumanIncidentLocked(key, taskID string) (HumanPublisher, humanClaim, bool) {
	previous, seen := d.humanIncidents[key]
	if !seen {
		if len(d.humanIncidents) >= maxTrackedHumanIncidents {
			slog.Warn("escalate: human-incident dedupe map reset at cap", "cap", maxTrackedHumanIncidents)
			d.humanIncidents = map[string]humanIncidentState{}
		}
		current := humanIncidentState{taskID: taskID}
		d.humanIncidents[key] = current
		return d.human, humanClaim{key: key, current: current}, true
	}
	current, regression := nextHumanState(previous, taskID)
	d.humanIncidents[key] = current
	if previous.interrupted && !regression {
		return d.human, humanClaim{}, false
	}
	claim := humanClaim{key: key, previous: previous, current: current, existed: true}
	return d.human, claim, true
}

func nextHumanState(previous humanIncidentState, taskID string) (humanIncidentState, bool) {
	current := previous
	if previous.taskID == "" && taskID != "" {
		current.taskID = taskID
	}
	if previous.taskID != "" && taskID != "" && previous.taskID != taskID {
		return humanIncidentState{taskID: taskID}, true
	}
	return current, false
}

func (d *Dispatcher) releaseHumanIncident(claim humanClaim) {
	if claim.key == "" {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	current, exists := d.humanIncidents[claim.key]
	if !exists || current != claim.current {
		return
	}
	if claim.existed {
		d.humanIncidents[claim.key] = claim.previous
	} else {
		delete(d.humanIncidents, claim.key)
	}
}

func (d *Dispatcher) completeHumanIncident(claim humanClaim, interrupted bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	current, exists := d.humanIncidents[claim.key]
	if !exists || current != claim.current {
		return
	}
	current.interrupted = interrupted
	d.humanIncidents[claim.key] = current
}

func (d *Dispatcher) humanIncidentAccepted(n distill.Notification) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.humanIncidents[humanIncidentKey(n)].interrupted
}

func (d *Dispatcher) reserveHumanPush(workspaceID string) (time.Time, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	recent := d.humanPerHour[workspaceID][:0]
	for _, sentAt := range d.humanPerHour[workspaceID] {
		if now.Sub(sentAt) < time.Hour {
			recent = append(recent, sentAt)
		}
	}
	if len(recent) >= maxHumanPushesPerHour {
		d.humanPerHour[workspaceID] = recent
		return time.Time{}, false
	}
	d.humanPerHour[workspaceID] = append(recent, now)
	return now, true
}

func (d *Dispatcher) releaseHumanPush(workspaceID string, reservedAt time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	times := d.humanPerHour[workspaceID]
	for index := len(times) - 1; index >= 0; index-- {
		if times[index].Equal(reservedAt) {
			d.humanPerHour[workspaceID] = append(times[:index], times[index+1:]...)
			return
		}
	}
}

func isHumanCandidate(n distill.Notification) bool {
	// Test notifications exercise channel wiring only — they must never fire a
	// real durable phone-buzzing critical alert, nor mark the incident seen
	// (which would silence the genuine escalation for that template).
	return !n.Test && n.NewIncident && n.Severity == store.SeverityCritical &&
		(n.TemplateID != "" || n.TaskID != "")
}
