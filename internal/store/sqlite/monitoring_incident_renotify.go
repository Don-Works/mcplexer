// monitoring_incident_renotify.go — the query side of the daemon
// re-notification sweep.
//
// Gap A, restated precisely (2026-07-20 incident): monitoringNotificationDue is
// a correct persistence policy that was never re-evaluated for the case it was
// written for. ListPendingLogTemplates filters "triaged_at IS NULL" and
// UpsertLogTemplate only clears triaged_at on a severity INCREASE, so a
// template repeating at a steady severity after triage never returns to the
// worker. RecordMonitoringTriage is therefore never called again, and the
// policy — which only ever runs inside that call — is never consulted. The
// incident kept recurring, last_seen kept advancing, and nothing asked the one
// question that mattered.
//
// This query re-asks it on a timer instead, from the daemon, against the same
// pure function. No model is invoked, no prompt grows, no worker is woken: the
// verdict is computed from columns the collector already writes for free.
package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// monitoringRenotifyScanLimit bounds the candidate scan when a caller passes a
// non-positive limit. Chosen well above any plausible unresolved-incident
// count for one workspace, so the default behaviour is "sweep everything" while
// still refusing to scan an unbounded table.
const monitoringRenotifyScanLimit = 500

// ListMonitoringIncidentsDueForRenotify implements store.MonitoringRenotifyStore.
func (d *DB) ListMonitoringIncidentsDueForRenotify(
	ctx context.Context, workspaceID string, now time.Time, limit int,
) ([]*store.MonitoringRenotifyCandidate, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fmt.Errorf("ListMonitoringIncidentsDueForRenotify: workspace_id required")
	}
	if limit <= 0 || limit > monitoringRenotifyScanLimit {
		limit = monitoringRenotifyScanLimit
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	rows, err := d.q.QueryContext(ctx, monitoringRenotifyQuery(),
		workspaceID, formatTime(now.Add(-monitoringIncidentActiveWindow)),
		store.MonitoringDispositionBenign, limit)
	if err != nil {
		return nil, fmt.Errorf("list monitoring incidents due for renotify: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	out := []*store.MonitoringRenotifyCandidate{}
	for rows.Next() {
		incident, err := scanMonitoringIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("scan monitoring renotify candidate: %w", err)
		}
		// newIncident is false by construction: a row that exists in this
		// table has already been recorded, so "first ever notification" is
		// never this sweep's verdict to give.
		decision := monitoringNotificationDue(incident, false, now)
		if !decision.Notify {
			continue
		}
		out = append(out, &store.MonitoringRenotifyCandidate{
			Incident:           incident,
			NotificationReason: decision.Reason,
			EffectiveSeverity:  decision.EffectiveSeverity,
		})
	}
	return out, rows.Err()
}

// monitoringRenotifyQuery pre-filters candidates so the Go policy only ever
// examines rows that could plausibly be due.
//
//   - last_seen >= now-activeWindow drops incidents that stopped recurring.
//     The policy would drop them too, but doing it in SQL keeps a long tail of
//     historical incidents out of every tick forever.
//   - disposition != benign and severity >= warn mirror the policy's two hard
//     floors exactly. They are floors on the CLASSIFIER severity, before any
//     age escalation, so info-level noise can never age into a page.
//
// Ordering is least-recently-notified first (never-notified before all of
// them), which is precisely the order incidents come due in, so LIMIT bounds
// the work per tick without starving anyone: a notified incident has its
// last_notified_at stamped to now and rotates to the back of the queue.
func monitoringRenotifyQuery() string {
	return `SELECT ` + monitoringIncidentReadCols + `
		FROM monitoring_incidents
		WHERE workspace_id = ?
		  AND last_seen >= ?
		  AND disposition != ?
		  AND severity IN (` + monitoringNotifiableSeverityList() + `)
		ORDER BY last_notified_at IS NULL DESC, last_notified_at ASC, first_seen ASC
		LIMIT ?`
}

// monitoringNotifiableSeverityList renders the at-or-above-warn severities as
// a SQL literal list. Derived from the severity ladder rather than hardcoded so
// a new severity cannot silently fall out of the sweep; every value is a
// compile-time constant from store, so no user input reaches the statement.
func monitoringNotifiableSeverityList() string {
	quoted := make([]string, 0, len(monitoringSeverityLadder))
	for _, severity := range monitoringSeverityLadder {
		if store.SeverityRank(severity) < store.SeverityRank(store.SeverityWarn) {
			continue
		}
		quoted = append(quoted, "'"+severity+"'")
	}
	return strings.Join(quoted, ", ")
}
