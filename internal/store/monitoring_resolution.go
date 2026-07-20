package store

import (
	"context"
	"errors"
	"time"
)

// Task-resolution feedback into monitoring triage.
//
// A logwatch task is filed by monitoring__commit_triage and linked to its
// incident in two directions that already existed before this file:
//
//	monitoring_incidents.task_id      -> tasks.id
//	tasks.meta.$.logwatch_class       -> monitoring_incidents.class_key
//
// What did not exist was anything that READ those links when the task reached
// a terminal status. This file is that feedback path. It is deterministic
// bookkeeping over columns the daemon already writes — it never invokes a
// model, and its whole purpose is to REDUCE model wake-ups.

// Monitoring resolution outcomes. These are the mapping targets for a task's
// terminal status kind, deliberately kept distinct from each other:
//
//   - MonitoringOutcomeBenign ("this was never a problem") suppresses. It sets
//     the incident's disposition to MonitoringDispositionBenign, which the
//     existing notification policy already mutes, and acks the incident's
//     currently linked templates so they stop counting toward novelty
//     wake-ups. This is the case that saves both noise and model spend.
//
//   - MonitoringOutcomeFixed ("this was real and we fixed it") does NOT
//     suppress anything. Nothing is acked and the disposition is untouched, so
//     if the class recurs later it notifies exactly as it would have before.
//     Conflating "fixed" with "benign" is how a genuine regression gets
//     swallowed, so the two are never collapsed.
const (
	MonitoringOutcomeBenign = "benign"
	MonitoringOutcomeFixed  = "fixed"
)

// ValidMonitoringOutcome reports whether o is a known resolution outcome.
func ValidMonitoringOutcome(o string) bool {
	return o == MonitoringOutcomeBenign || o == MonitoringOutcomeFixed
}

// Reasons a suppression was reversed. Stored verbatim on the resolution row so
// the operator read path can say WHY something stopped being suppressed.
const (
	// MonitoringClearReasonReopened — the canonical task was reopened,
	// either by an operator or by the recurrence path in commit_triage.
	MonitoringClearReasonReopened = "task_reopened"
	// MonitoringClearReasonRecurrence — a fresh triage landed on an incident
	// that was still suppressed. A class that is being triaged again is by
	// definition not settled, so the suppression is broken rather than
	// allowed to swallow the recurrence.
	MonitoringClearReasonRecurrence = "recurrence_triaged"
	// MonitoringClearReasonManual — an operator explicitly unsuppressed.
	MonitoringClearReasonManual = "manual"
)

// ErrMonitoringResolutionNotFound is returned when no resolution row exists
// for the requested incident.
var ErrMonitoringResolutionNotFound = errors.New("monitoring resolution not found")

// MonitoringResolution is the durable record of what one task resolution did
// to one incident. It is the reversal receipt: every field needed to undo the
// suppression exactly, plus who did it and when.
type MonitoringResolution struct {
	IncidentID  string `json:"incident_id"`
	WorkspaceID string `json:"workspace_id"`
	TaskID      string `json:"task_id"`
	// Outcome is MonitoringOutcomeBenign or MonitoringOutcomeFixed.
	Outcome string `json:"outcome"`
	// StatusText is the operator's own terminal status word ("done",
	// "wontfix", ...), kept verbatim rather than reduced to the bucket.
	StatusText string `json:"status_text"`
	// DispositionBefore is the incident disposition at suppression time.
	// Clearing restores exactly this rather than assuming "actionable".
	DispositionBefore    string `json:"disposition_before,omitempty"`
	SeverityAtResolution string `json:"severity_at_resolution,omitempty"`
	// AckedTemplateIDs are the templates THIS resolution acked. Clearing
	// un-acks only these, so an operator's own prior monitoring__ack on some
	// other template in the class is never silently undone.
	AckedTemplateIDs  []string   `json:"acked_template_ids"`
	ResolvedAt        time.Time  `json:"resolved_at"`
	ResolvedBySession string     `json:"resolved_by_session,omitempty"`
	ResolvedByActor   string     `json:"resolved_by_actor,omitempty"`
	ClearedAt         *time.Time `json:"cleared_at,omitempty"`
	ClearedReason     string     `json:"cleared_reason,omitempty"`
	ClearedBySession  string     `json:"cleared_by_session,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`

	// Incident projection, filled by the list read path so an operator can
	// answer "what is suppressed and why" without a second query.
	ClassKey         string    `json:"class_key,omitempty"`
	IncidentTitle    string    `json:"incident_title,omitempty"`
	Severity         string    `json:"severity,omitempty"`
	Disposition      string    `json:"disposition,omitempty"`
	IncidentLastSeen time.Time `json:"incident_last_seen,omitempty"`
	// TaskStatus / TaskClosed describe the canonical task as it stands now,
	// so a stale suppression (task reopened but resolution not cleared) is
	// visible rather than invisible.
	TaskStatus string `json:"task_status,omitempty"`
	TaskClosed bool   `json:"task_closed"`
}

// Suppressing reports whether this resolution is actively suppressing the
// incident: a live (uncleared) benign resolution. A "fixed" resolution is
// never suppressing, which is the whole point of keeping the two distinct.
func (r *MonitoringResolution) Suppressing() bool {
	return r != nil && r.Outcome == MonitoringOutcomeBenign && r.ClearedAt == nil
}

// MonitoringResolutionInput is one task-resolution feedback event.
//
// The caller (the tasks service, on terminal entry) supplies only facts it
// already has. The store resolves the incident from TaskID, decides what the
// outcome implies, and applies it in a single transaction.
type MonitoringResolutionInput struct {
	WorkspaceID string
	TaskID      string
	// Outcome must be MonitoringOutcomeBenign or MonitoringOutcomeFixed.
	Outcome string
	// StatusText is the raw terminal status word from the task.
	StatusText string
	BySession  string
	ByActor    string
	ResolvedAt time.Time
}

// MonitoringResolutionStore is the feedback surface: task resolution in,
// incident/template state out, plus the operator's read and reversal paths.
//
// It is deliberately a separate interface from MonitoringStore so the tasks
// service can depend on this narrow slice (and so a test fake can implement
// three methods rather than forty).
type MonitoringResolutionStore interface {
	// ApplyMonitoringTaskResolution feeds a terminal task back to its
	// incident. It is a no-op returning (nil, nil) when the task is not
	// linked to any incident — legacy and hand-written tasks flow through
	// this path constantly and must never fail it.
	ApplyMonitoringTaskResolution(ctx context.Context, in MonitoringResolutionInput) (*MonitoringResolution, error)

	// ClearMonitoringResolutionForTask reverses whatever the resolution did:
	// restores the prior disposition, un-acks exactly the templates the
	// resolution acked, and clears the incident's last_notified_at so the
	// next observation of this class is guaranteed to notify. Returns
	// (nil, nil) when there was no live resolution. Idempotent.
	ClearMonitoringResolutionForTask(ctx context.Context, workspaceID, taskID, reason, bySession string) (*MonitoringResolution, error)

	// ClearMonitoringResolution is the same reversal addressed by incident id
	// — the operator-facing "unsuppress this" path.
	ClearMonitoringResolution(ctx context.Context, workspaceID, incidentID, reason, bySession string) (*MonitoringResolution, error)

	// ListMonitoringResolutions is the visibility requirement: what is
	// currently suppressed, why, by whom, and since when. suppressingOnly
	// restricts to live benign rows (the things actually muting alerts).
	ListMonitoringResolutions(ctx context.Context, workspaceID string, suppressingOnly bool, limit int) ([]*MonitoringResolution, error)
}
