package store

import (
	"context"
	"time"
)

// MonitoringRenotifyCandidate is one unresolved incident that the shared
// persistence policy has judged due for another notification.
//
// It carries the verdict alongside the row because the policy itself lives in
// the sqlite package (it is computed from columns only the store owns) and is
// deliberately not exported: the daemon sweep must not re-derive severity or
// reason for itself, or the two implementations drift and the 2026-07-20
// silence returns in a new shape.
type MonitoringRenotifyCandidate struct {
	Incident *MonitoringIncident `json:"incident"`
	// NotificationReason is the policy's vocabulary — age_escalation,
	// persistent_incident, unnotified_incident, severity_escalation.
	NotificationReason string `json:"notification_reason"`
	// EffectiveSeverity is the classifier severity raised by sustained
	// incident age. Dispatch AND MarkMonitoringIncidentNotified with this
	// value, never Incident.Severity: channel min_severity defaults to
	// "error", so an ageing warn incident that is dispatched at its raw
	// severity is filtered out at every channel and the operator hears
	// nothing — which is exactly the failure this sweep exists to end.
	EffectiveSeverity string `json:"effective_severity"`
}

// MonitoringRenotifyStore is the daemon re-notification sweep's slice of the
// store. It is defined at the consumer boundary rather than folded into
// MonitoringStore, following MonitoringExpectedSignalStore: adding a sweep
// must not force every store mock across the tree to grow a method. *sqlite.DB
// satisfies it.
type MonitoringRenotifyStore interface {
	// ListMonitoringIncidentsDueForRenotify returns the incidents in one
	// workspace that the persistence policy says should be notified again at
	// now. Candidates are pre-filtered in SQL (still being observed, not
	// benign, at or above warn) and the Go policy is applied per row, so the
	// result contains only genuinely due incidents.
	//
	// limit bounds the SQL candidate scan, not the returned slice, so a large
	// backlog cannot stall the daemon. Candidates are ordered
	// least-recently-notified first, which is the same order the policy
	// becomes due in, so the bound never starves an incident indefinitely:
	// anything notified moves to the back of the queue.
	ListMonitoringIncidentsDueForRenotify(
		ctx context.Context, workspaceID string, now time.Time, limit int,
	) ([]*MonitoringRenotifyCandidate, error)

	MarkMonitoringIncidentNotified(ctx context.Context, incidentID, severity string, at time.Time) error
	ListWorkspaces(ctx context.Context) ([]Workspace, error)
}
