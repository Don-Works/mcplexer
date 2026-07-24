// Package renotify re-asks the persistence question on a timer.
//
// The 2026-07-20 incident: a recurring order-sync job hung. The process stayed
// alive and emitted nothing, so every liveness signal — process table, unit
// status, container healthcheck — stayed green. No error-pattern detection
// could ever have caught it, because there was no error to pattern-match. The
// only observable was "time since the last successful completion exceeded the
// normal cadence", and nothing was measuring that. Ingest sat at zero for
// roughly twelve hours and the operator found it himself the next morning.
//
// The store already holds a correct policy for this (monitoringNotificationDue)
// and already holds the evidence (last_seen advances on every mapped log batch,
// for free, with no AI in the loop). What was missing was anyone asking. The
// triage path only asks when a worker runs, and a template held at a steady
// severity after triage never returns to a worker — so for the exact case the
// policy was written for, it was never evaluated a second time.
//
// This sweep is that second evaluation, and every one after it. It is entirely
// deterministic: no model is called, no prompt grows, no worker is woken.
package renotify

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

const (
	// defaultInterval is how often the sweep re-asks. The tightest quiet
	// period the policy can produce is 30m (a genuine critical), so a 5m tick
	// adds at most a sixth of the shortest cadence as latency, and is noise
	// against the 4h/12h age-escalation tiers. Cheaper than that buys nothing:
	// the evidence it reads (last_seen) only advances when the collector runs,
	// and the work is one indexed query per workspace.
	defaultInterval = 5 * time.Minute

	// defaultLimit bounds the candidate scan per workspace per tick. A backlog
	// larger than this drains across ticks rather than stalling the daemon;
	// the store orders candidates least-recently-notified first, so the bound
	// rotates rather than starves.
	defaultLimit = 100
)

// Store is the sweep's slice of the store.
type Store = store.MonitoringRenotifyStore

// Sweeper re-evaluates the shared persistence policy for unresolved incidents
// and dispatches through the existing notification path.
type Sweeper struct {
	store    Store
	notifier distill.Notifier
	now      func() time.Time
	interval time.Duration
	limit    int
}

// New wires a sweeper. Returns nil when either dependency is missing, so a
// caller can start it unconditionally at boot.
func New(st Store, notifier distill.Notifier) *Sweeper {
	if st == nil || notifier == nil {
		return nil
	}
	return &Sweeper{
		store: st, notifier: notifier, now: time.Now,
		interval: defaultInterval, limit: defaultLimit,
	}
}

// Run loops until ctx is cancelled. Call in a goroutine at daemon boot —
// without that call this package is dead code and the silence returns.
func (s *Sweeper) Run(ctx context.Context) {
	if s == nil {
		return
	}
	t := time.NewTicker(s.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep(ctx)
		}
	}
}

// Sweep runs one pass across every workspace. Exported so the daemon (and
// tests) can drive a single tick without a timer.
func (s *Sweeper) Sweep(ctx context.Context) {
	if s == nil {
		return
	}
	workspaces, err := s.store.ListWorkspaces(ctx)
	if err != nil {
		slog.Warn("renotify: list workspaces", "error", err)
		return
	}
	// Every workspace, not just one: incidents are per-workspace and a
	// silently un-swept workspace is indistinguishable from a healthy one.
	for _, ws := range workspaces {
		if ctx.Err() != nil {
			return
		}
		s.sweepWorkspace(ctx, ws.ID)
	}
}

func (s *Sweeper) sweepWorkspace(ctx context.Context, workspaceID string) {
	now := s.now().UTC()
	due, err := s.store.ListMonitoringIncidentsDueForRenotify(ctx, workspaceID, now, s.limit)
	if err != nil {
		slog.Warn("renotify: list due incidents", "workspace", workspaceID, "error", err)
		return
	}
	for _, candidate := range due {
		if ctx.Err() != nil {
			return
		}
		s.renotify(ctx, candidate, now)
	}
}

// renotify dispatches one reminder and then advances the backoff. Marking is
// what stops the next tick re-firing the same incident five minutes later, so
// a dispatch that succeeded but failed to mark is logged loudly.
func (s *Sweeper) renotify(
	ctx context.Context, candidate *store.MonitoringRenotifyCandidate, now time.Time,
) {
	if candidate == nil || candidate.Incident == nil {
		return
	}
	incident := candidate.Incident
	// Use the store's deterministic dispatch severity. Persistence reminders
	// retain the classifier severity; age alone never manufactures a page.
	severity := candidate.EffectiveSeverity
	if !store.ValidSeverity(severity) {
		severity = incident.Severity
	}
	n := distill.Notification{
		WorkspaceID: incident.WorkspaceID,
		Severity:    severity,
		Title:       renotifyTitle(incident),
		Body:        renotifyBody(incident, candidate.NotificationReason, severity, now),
		TaskID:      incident.TaskID,
		IncidentID:  incident.ID,
		// Never a new incident: this path only ever revisits recorded ones.
		NewIncident: false,
	}
	if err := s.notifier.Notify(ctx, n); err != nil {
		slog.Warn("renotify: dispatch failed", "workspace", incident.WorkspaceID,
			"incident", incident.ID, "severity", severity, "error", err)
		return
	}
	// Record exactly what was dispatched so later comparisons stay coherent.
	if err := s.store.MarkMonitoringIncidentNotified(ctx, incident.ID, severity, now); err != nil {
		slog.Error("renotify: notification sent but backoff not advanced",
			"workspace", incident.WorkspaceID, "incident", incident.ID, "error", err)
		return
	}
	slog.Info("renotify: incident still unresolved", "workspace", incident.WorkspaceID,
		"incident", incident.ID, "reason", candidate.NotificationReason,
		"severity", severity, "raw_severity", incident.Severity)
}

func renotifyTitle(i *store.MonitoringIncident) string {
	title := strings.TrimSpace(i.Title)
	if title == "" || distill.IsGenericMonitoringTitle(title) {
		// Avoid "Still unresolved: new error-class log template…" — that is the
		// exact Chat shape that trained operators to ignore renotifies.
		if class := strings.TrimPrefix(i.ClassKey, "correlation:"); class != "" && class != i.ClassKey {
			title = class
		} else if title == "" {
			title = i.ClassKey
		}
	}
	return "Still unresolved: " + title
}

// renotifyBody is assembled from incident columns only — deterministic, and
// identical for the same row and clock on every machine.
func renotifyBody(i *store.MonitoringIncident, reason, severity string, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This incident is still recurring and has not been resolved (%s).\n\n",
		renotifyReasonLabel(reason))
	if title := strings.TrimSpace(i.Title); title != "" && !distill.IsGenericMonitoringTitle(title) {
		fmt.Fprintf(&b, "Title: %s\n", title)
	}
	fmt.Fprintf(&b, "Severity: %s (unchanged by age — reminder only)\n", severity)
	fmt.Fprintf(&b, "First seen: %s (%s ago)\n", i.FirstSeen.UTC().Format(time.RFC3339),
		renotifyDuration(now.Sub(i.FirstSeen)))
	fmt.Fprintf(&b, "Last seen: %s (%s ago)\n", i.LastSeen.UTC().Format(time.RFC3339),
		renotifyDuration(now.Sub(i.LastSeen)))
	fmt.Fprintf(&b, "Occurrence windows: %d\nEvents: %d\n", i.OccurrenceCount, i.EventCount)
	if i.LastNotifiedAt != nil {
		fmt.Fprintf(&b, "Last notified: %s (%s ago)\n", i.LastNotifiedAt.UTC().Format(time.RFC3339),
			renotifyDuration(now.Sub(*i.LastNotifiedAt)))
	}
	fmt.Fprintf(&b, "\nNext step: open the linked task and confirm whether impact is still live; ack or silence if already handled.\n")
	return b.String()
}

func renotifyReasonLabel(reason string) string {
	if reason == "age_escalation" {
		return "age reminder"
	}
	return strings.ReplaceAll(reason, "_", " ")
}

// renotifyDuration renders a whole-unit age. Minute resolution is deliberate:
// the operator's question is "how long has this been going", not "how many
// seconds", and a stable string keeps repeated reminders diffable.
func renotifyDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
}
