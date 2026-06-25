// audit_alerts_helpers.go — match-count + small classification helpers
// shared by the audit alert computation and the saved-search evaluator.
package sqlite

import (
	"context"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

// CountAuditMatching counts rows matching the filter (q + every exact
// dimension). Backs the saved-search evaluator's threshold check. Reuses
// buildAuditWhere so the count is identical to what QueryAuditRecords
// would page over.
func (d *DB) CountAuditMatching(ctx context.Context, f store.AuditFilter) (int, error) {
	where, args := buildAuditWhere(f)
	var n int
	err := d.q.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_records"+where, args...).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count audit matching: %w", err)
	}
	return n, nil
}

// alertFilter builds the deep-link AuditFilter subset for an alert.
func alertFilter(tool, ws, status string) map[string]any {
	m := map[string]any{}
	if tool != "" {
		m["tool_name"] = tool
	}
	if ws != "" {
		m["workspace_id"] = ws
	}
	if status != "" {
		m["status"] = status
	}
	return m
}

// severityFor maps a rate metric to a severity band. critical when the
// metric crosses crit, else warning.
func severityFor(metric, crit float64) string {
	if metric >= crit {
		return "critical"
	}
	return "warning"
}

// severityForCount maps a raw count to a severity band relative to a
// critical ceiling.
func severityForCount(n, crit int) string {
	switch {
	case n >= crit:
		return "critical"
	case n >= crit/2:
		return "warning"
	default:
		return "info"
	}
}
