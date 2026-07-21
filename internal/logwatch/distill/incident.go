// incident.go — the seam that turns a fired anomaly into a LINKABLE alert.
//
// Before this seam an anomaly notification carried only a template hash in its
// footer: no canonical task, so the renderer had no task link to emit and an
// operator had nothing to click. Here the distiller hands the anomaly to an
// IncidentEnsurer that elects (or reuses) one canonical incident + task by a
// STABLE class key, then stamps the notification's TaskID so the renderer emits
// a clickable link.
//
// It is deterministic — no model is consulted. Convergence is the whole point:
// repeats of the same shape roll into ONE incident and ONE task, they do not
// spawn a task per alert. The class key is derived here so an anomaly-filed
// incident and a later worker triage of the same template land on the same
// class and never file siblings (see anomalyClassKey).
package distill

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// IncidentEnsurer files a fired anomaly as a canonical monitoring incident and
// its one canonical task, deterministically and idempotently by class key, and
// stamps the incident's notification clock after a successful dispatch. It is
// implemented in daemon wiring against the task service and the incident store
// so the distiller stays free of both. Nil-safe by construction: a nil ensurer
// means anomalies dispatch without a linkable task, exactly as before.
type IncidentEnsurer interface {
	// EnsureIncident converges the anomaly onto its canonical incident + task by
	// ClassKey and returns the refs the notification needs to be linkable. An
	// input with no TemplateIDs elects a canonical task only (the rate-spike
	// case): the incident ledger is template-keyed, so a template-less episode
	// gets the linkable task without a MonitoringIncident row.
	EnsureIncident(ctx context.Context, in IncidentInput) (*IncidentRef, error)
	// MarkNotified stamps the incident's notification clock so the daemon
	// renotify sweep does not immediately re-fire a just-dispatched incident. A
	// blank incidentID (the task-only case) is a no-op.
	MarkNotified(ctx context.Context, incidentID, severity string, at time.Time) error
	// CloseIncident resolves the canonical task for a class on recovery, so a
	// transient episode does not leave an open task nobody will ever close.
	CloseIncident(ctx context.Context, workspaceID, classKey, note string) error
}

// IncidentInput is one anomaly to be filed. ClassKey is the stable convergence
// identity; TemplateIDs are the exact templates linked to the class (empty for a
// source-scoped rate spike).
type IncidentInput struct {
	WorkspaceID string
	ClassKey    string
	Title       string
	Body        string
	Severity    string
	SourceID    string
	TemplateIDs []string
	ObservedAt  time.Time
}

// IncidentRef is what the distiller needs to make the alert linkable and to
// close the notification loop. IncidentID is blank when only a task was elected.
type IncidentRef struct {
	TaskID      string
	IncidentID  string
	NewIncident bool
}

// WithIncidents late-binds the ensurer. Boot builds the distiller before the
// task service exists, so the ensurer is attached once both are up (see
// startMonitoringCollector). Returns the distiller for call-site chaining.
func (d *Distiller) WithIncidents(e IncidentEnsurer) *Distiller {
	d.incidents = e
	return d
}

// anomalyClassKey is the incident class for a single-template anomaly. It MUST
// equal the gateway triage path's default single-template class
// (handler_monitoring_class.go: "template:" + id) so an anomaly-filed incident
// and a later worker triage of the SAME template converge on one incident and
// one task rather than filing siblings.
func anomalyClassKey(templateID string) string { return "template:" + templateID }

// rateSpikeClassKey is the incident/task class shared by every rate-spike
// episode on one source, so repeats dedupe onto one task.
func rateSpikeClassKey(sourceID string) string { return "ratespike:" + sourceID }

// linkAnomalyIncident files a fired template anomaly and stamps n.TaskID. It
// returns the ref (nil when no ensurer is wired or the ensure failed) so the
// caller can stamp the notification clock after a successful dispatch. A failure
// degrades to an unlinked alert rather than dropping it — the operator being
// told late-without-a-link beats not being told.
func (d *Distiller) linkAnomalyIncident(
	ctx context.Context, n *Notification, src *store.LogSource, agg *templateAgg,
) *IncidentRef {
	return d.linkIncident(ctx, n, IncidentInput{
		WorkspaceID: src.WorkspaceID,
		ClassKey:    anomalyClassKey(agg.tpl.ID),
		Title:       n.Title,
		Body:        n.Body,
		Severity:    agg.tpl.Severity,
		SourceID:    src.ID,
		TemplateIDs: []string{agg.tpl.ID},
		ObservedAt:  agg.tpl.LastSeen,
	})
}

// linkRateSpikeIncident elects the source's canonical rate-spike task and stamps
// n.TaskID. No template is linked (a spike is a rate phenomenon, not a shape),
// so this yields a linkable task without a MonitoringIncident row.
func (d *Distiller) linkRateSpikeIncident(
	ctx context.Context, n *Notification, src *store.LogSource,
) *IncidentRef {
	return d.linkIncident(ctx, n, IncidentInput{
		WorkspaceID: src.WorkspaceID,
		ClassKey:    rateSpikeClassKey(src.ID),
		Title:       n.Title,
		Body:        n.Body,
		Severity:    n.Severity,
		SourceID:    src.ID,
		ObservedAt:  d.now().UTC(),
	})
}

func (d *Distiller) linkIncident(ctx context.Context, n *Notification, in IncidentInput) *IncidentRef {
	if d.incidents == nil {
		return nil
	}
	ref, err := d.incidents.EnsureIncident(ctx, in)
	if err != nil {
		slog.Warn("distill: ensure incident", "class", in.ClassKey, "error", err)
		return nil
	}
	if ref == nil {
		return nil
	}
	if ref.TaskID != "" {
		// Only TaskID is stamped: IncidentID drives the dispatcher's throttle key
		// and episode identity, which the distiller has already set deliberately.
		n.TaskID = ref.TaskID
	}
	return ref
}

// markIncidentNotified stamps the incident's notification clock after a real
// dispatch. Skipped on the task-only path (blank incidentID) and when no ensurer
// is wired.
func (d *Distiller) markIncidentNotified(ctx context.Context, ref *IncidentRef, severity string) {
	if d.incidents == nil || ref == nil || ref.IncidentID == "" {
		return
	}
	if err := d.incidents.MarkNotified(ctx, ref.IncidentID, severity, d.now().UTC()); err != nil {
		slog.Warn("distill: mark incident notified", "incident", ref.IncidentID, "error", err)
	}
}

// closeRateSpikeIncident resolves the source's canonical rate-spike task when
// the rate recovers to its baseline, mirroring the absence path's recovery-close.
func (d *Distiller) closeRateSpikeIncident(ctx context.Context, src *store.LogSource) {
	if d.incidents == nil {
		return
	}
	note := "error rate on " + src.Name + " returned to its trailing baseline"
	if err := d.incidents.CloseIncident(ctx, src.WorkspaceID, rateSpikeClassKey(src.ID), note); err != nil {
		slog.Warn("distill: close rate spike incident", "source", src.Name, "error", err)
	}
}
