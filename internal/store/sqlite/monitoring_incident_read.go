// monitoring_incident_read.go — SQLite side of store.MonitoringIncidentReadStore.
//
// The derived fields are computed HERE rather than in the API layer because the
// policy they come from (monitoringEffectiveSeverity, monitoringIncidentActive)
// lives in this package alongside the notification decision that uses it. One
// definition, so a severity the operator reads can never disagree with the
// severity the dispatcher acted on.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// ListMonitoringIncidents returns one workspace's incidents, most recently seen
// first. The status filter is expressed in SQL rather than applied after the
// fact so the limit bounds the ANSWER, not the scan — filtering in Go would let
// a page of inactive rows starve the active ones the operator asked for.
func (d *DB) ListMonitoringIncidents(
	ctx context.Context, f store.MonitoringIncidentFilter,
) ([]*store.MonitoringIncidentView, error) {
	if f.WorkspaceID == "" {
		return nil, errors.New("ListMonitoringIncidents: workspace_id required")
	}
	now := time.Now().UTC()
	query := `SELECT ` + monitoringIncidentReadCols + `
		FROM monitoring_incidents WHERE workspace_id = ?`
	args := []any{f.WorkspaceID}
	if f.Disposition != "" {
		query += ` AND disposition = ?`
		args = append(args, f.Disposition)
	}
	if !f.Since.IsZero() {
		query += ` AND last_seen >= ?`
		args = append(args, formatTime(f.Since.UTC()))
	}
	switch f.Status {
	case store.MonitoringIncidentStatusActive:
		query += ` AND last_seen >= ?`
		args = append(args, formatTime(now.Add(-monitoringIncidentActiveWindow)))
	case store.MonitoringIncidentStatusInactive:
		query += ` AND last_seen < ?`
		args = append(args, formatTime(now.Add(-monitoringIncidentActiveWindow)))
	}
	query += ` ORDER BY last_seen DESC, id DESC LIMIT ?`
	args = append(args, monitoringIncidentLimit(f.Limit))

	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list monitoring incidents: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	out := []*store.MonitoringIncidentView{}
	for rows.Next() {
		incident, err := scanMonitoringIncident(rows)
		if err != nil {
			return nil, fmt.Errorf("scan monitoring incident: %w", err)
		}
		out = append(out, monitoringIncidentView(incident, now))
	}
	return out, rows.Err()
}

// GetMonitoringIncident returns one incident scoped to a workspace. A row in
// another workspace is reported exactly as a missing one, so the response shape
// cannot be used to probe for foreign incident ids.
func (d *DB) GetMonitoringIncident(
	ctx context.Context, workspaceID, id string,
) (*store.MonitoringIncidentView, error) {
	if workspaceID == "" || id == "" {
		return nil, store.ErrMonitoringIncidentNotFound
	}
	row := d.q.QueryRowContext(ctx, `SELECT `+monitoringIncidentReadCols+`
		FROM monitoring_incidents WHERE id = ? AND workspace_id = ?`, id, workspaceID)
	incident, err := scanMonitoringIncident(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrMonitoringIncidentNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get monitoring incident: %w", err)
	}
	return monitoringIncidentView(incident, time.Now().UTC()), nil
}

// ListMonitoringOccurrences returns an incident's episode ledger, most recent
// first. Callers scope the incident to a workspace before asking.
func (d *DB) ListMonitoringOccurrences(
	ctx context.Context, incidentID string, limit int,
) ([]*store.MonitoringOccurrence, error) {
	if incidentID == "" {
		return []*store.MonitoringOccurrence{}, nil
	}
	if limit <= 0 || limit > store.MonitoringOccurrenceListMaxLimit {
		limit = store.MonitoringOccurrenceListMaxLimit
	}
	rows, err := d.q.QueryContext(ctx, `SELECT `+monitoringOccurrenceCols+`
		FROM monitoring_occurrences WHERE incident_id = ?
		ORDER BY last_seen DESC, id DESC LIMIT ?`, incidentID, limit)
	if err != nil {
		return nil, fmt.Errorf("list monitoring occurrences: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	out := []*store.MonitoringOccurrence{}
	for rows.Next() {
		occurrence, err := scanMonitoringOccurrence(rows)
		if err != nil {
			return nil, fmt.Errorf("scan monitoring occurrence: %w", err)
		}
		out = append(out, occurrence)
	}
	return out, rows.Err()
}

// monitoringIncidentView attaches the policy-derived fields to a stored row.
func monitoringIncidentView(
	i *store.MonitoringIncident, now time.Time,
) *store.MonitoringIncidentView {
	kind, ref := store.ClassifyIncidentClassKey(i.ClassKey)
	// Compute the in-force suppression flags off the SAME predicates the
	// notification policy (monitoringActionSuppressed) and the suppressed-list
	// endpoint use, so every surface agrees on what is actually muted. AckActive
	// and SilenceActive fold escalation-piercing (and, for silence, expiry) in;
	// Suppressed is their union — the value the dispatcher's gate keys on.
	effective := monitoringEffectiveSeverity(i, now)
	ackActive := i.AckedAt != nil && !severityHigher(effective, i.AckedSeverity)
	silenceActive := monitoringSilenceActive(i, now) &&
		!severityHigher(effective, i.SilencedSeverity)
	view := &store.MonitoringIncidentView{
		MonitoringIncident: i,
		EffectiveSeverity:  effective,
		ClassKind:          kind,
		ClassRef:           ref,
		Active:             monitoringIncidentActive(i, now),
		AckActive:          ackActive,
		SilenceActive:      silenceActive,
		Suppressed:         ackActive || silenceActive,
	}
	if kind == store.IncidentClassAbsence || kind == store.IncidentClassCollection {
		view.ExpectedSignalID = ref
	}
	return view
}

func monitoringIncidentLimit(limit int) int {
	switch {
	case limit <= 0:
		return store.MonitoringIncidentListDefaultLimit
	case limit > store.MonitoringIncidentListMaxLimit:
		return store.MonitoringIncidentListMaxLimit
	default:
		return limit
	}
}
