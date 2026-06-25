// audit_alerts.go — deterministic, locally-computed audit alerts.
//
// Every alert is a threshold crossing the store can fully explain — no
// external LLM, no heuristics that can't be traced back to a count or a
// ratio. Two families:
//   - AuditAnomalies   — operational: error-rate spike vs prior window,
//     p95 latency spike, volume surge per tool.
//   - AuditSecurityEvents — security: blocked/denied counts, denial_reason
//     rows, cross-org tier shares, secret.* access spikes.
package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// Anomaly thresholds — deterministic so an operator can reason about why
// an alert fired. Tuned conservative to avoid noise on a quiet gateway.
const (
	anomalyMinErrors       = 5    // need this many errors before error-rate fires
	anomalyErrorRateFactor = 2.0  // window error-rate >= factor * baseline
	anomalyMinErrorRate    = 0.25 // ...and at least this absolute rate
	anomalyLatencyFactor   = 2.0  // window p95 >= factor * baseline p95
	anomalyMinLatencyMs    = 500  // ...and at least this absolute p95
	anomalyVolumeFactor    = 3.0  // window volume >= factor * baseline volume
	anomalyMinVolume       = 20   // ...and at least this many calls
)

// toolWindowStat is the per-tool aggregate over a single time window.
type toolWindowStat struct {
	tool   string
	ws     string
	calls  int
	errors int
	p95    float64
}

// AuditAnomalies computes operational anomalies over [now-window, now]
// (the "window") against [now-2*window, now-window] (the "baseline").
func (d *DB) AuditAnomalies(
	ctx context.Context, ws string, window time.Duration,
) ([]store.AuditAlert, error) {
	if window <= 0 {
		window = time.Hour
	}
	now := time.Now().UTC()
	winStart := now.Add(-window)
	baseStart := now.Add(-2 * window)

	cur, err := d.toolWindowStats(ctx, ws, winStart, now)
	if err != nil {
		return nil, err
	}
	base, err := d.toolWindowStats(ctx, ws, baseStart, winStart)
	if err != nil {
		return nil, err
	}
	baseByKey := make(map[string]toolWindowStat, len(base))
	for _, b := range base {
		baseByKey[b.tool+"\x00"+b.ws] = b
	}

	var alerts []store.AuditAlert
	for _, c := range cur {
		b := baseByKey[c.tool+"\x00"+c.ws]
		alerts = appendErrorRateAlert(alerts, c, b, winStart, now)
		alerts = appendLatencyAlert(alerts, c, b, winStart, now)
		alerts = appendVolumeAlert(alerts, c, b, winStart, now)
	}
	return alerts, nil
}

// appendErrorRateAlert fires when the window error-rate is both ≥ factor×
// baseline and ≥ the absolute floor, with enough errors to be meaningful.
func appendErrorRateAlert(
	alerts []store.AuditAlert, c, b toolWindowStat, from, to time.Time,
) []store.AuditAlert {
	if c.errors < anomalyMinErrors || c.calls == 0 {
		return alerts
	}
	rate := float64(c.errors) / float64(c.calls)
	baseRate := 0.0
	if b.calls > 0 {
		baseRate = float64(b.errors) / float64(b.calls)
	}
	if rate < anomalyMinErrorRate {
		return alerts
	}
	// Either ≥ factor× a non-zero baseline, or a brand-new spike (no
	// baseline traffic at all) above the absolute floor.
	if baseRate > 0 && rate < anomalyErrorRateFactor*baseRate {
		return alerts
	}
	return append(alerts, store.AuditAlert{
		ID:       "anomaly:error_rate:" + c.tool + ":" + c.ws,
		Kind:     "anomaly",
		Severity: severityFor(rate, 0.5),
		Title:    "Error-rate spike: " + c.tool,
		Detail: fmt.Sprintf("%.0f%% errors (%d/%d) vs %.0f%% baseline",
			rate*100, c.errors, c.calls, baseRate*100),
		ToolName: c.tool, WorkspaceID: c.ws, Count: c.errors,
		Metric: rate, Baseline: baseRate,
		FirstSeen: from, LastSeen: to,
		Filter: alertFilter(c.tool, c.ws, "error"),
	})
}

// appendLatencyAlert fires when window p95 latency is ≥ factor× baseline
// p95 and ≥ the absolute floor.
func appendLatencyAlert(
	alerts []store.AuditAlert, c, b toolWindowStat, from, to time.Time,
) []store.AuditAlert {
	if c.p95 < anomalyMinLatencyMs {
		return alerts
	}
	if b.p95 > 0 && c.p95 < anomalyLatencyFactor*b.p95 {
		return alerts
	}
	if b.p95 == 0 && c.calls < anomalyMinErrors {
		return alerts // no baseline + thin traffic → not enough signal
	}
	return append(alerts, store.AuditAlert{
		ID:       "anomaly:latency:" + c.tool + ":" + c.ws,
		Kind:     "anomaly",
		Severity: "warning",
		Title:    "Latency spike: " + c.tool,
		Detail: fmt.Sprintf("p95 %.0fms vs %.0fms baseline (%d calls)",
			c.p95, b.p95, c.calls),
		ToolName: c.tool, WorkspaceID: c.ws, Count: c.calls,
		Metric: c.p95, Baseline: b.p95,
		FirstSeen: from, LastSeen: to,
		Filter: alertFilter(c.tool, c.ws, ""),
	})
}

// appendVolumeAlert fires when call volume is ≥ factor× baseline and over
// the absolute floor — a traffic surge worth a glance.
func appendVolumeAlert(
	alerts []store.AuditAlert, c, b toolWindowStat, from, to time.Time,
) []store.AuditAlert {
	if c.calls < anomalyMinVolume {
		return alerts
	}
	if b.calls == 0 || float64(c.calls) < anomalyVolumeFactor*float64(b.calls) {
		return alerts
	}
	return append(alerts, store.AuditAlert{
		ID:       "anomaly:volume:" + c.tool + ":" + c.ws,
		Kind:     "anomaly",
		Severity: "info",
		Title:    "Volume surge: " + c.tool,
		Detail: fmt.Sprintf("%d calls vs %d baseline (%.1fx)",
			c.calls, b.calls, float64(c.calls)/float64(b.calls)),
		ToolName: c.tool, WorkspaceID: c.ws, Count: c.calls,
		Metric: float64(c.calls), Baseline: float64(b.calls),
		FirstSeen: from, LastSeen: to,
		Filter: alertFilter(c.tool, c.ws, ""),
	})
}

// toolWindowStats aggregates per (tool_name, workspace_id) over a window:
// call count, error count, and an approximate p95 latency (NTILE(20)).
func (d *DB) toolWindowStats(
	ctx context.Context, ws string, from, to time.Time,
) ([]toolWindowStat, error) {
	args := []any{formatTime(from), formatTime(to)}
	wsClause := ""
	if ws != "" {
		wsClause = " AND workspace_id = ?"
		args = append(args, ws)
	}
	q := `
		WITH scoped AS (
			SELECT tool_name, workspace_id, status, latency_ms,
				NTILE(20) OVER (PARTITION BY tool_name, workspace_id ORDER BY latency_ms) AS b
			FROM audit_records
			WHERE timestamp >= ? AND timestamp <= ?` + wsClause + `
		)
		SELECT tool_name, workspace_id,
			COUNT(*) AS calls,
			COUNT(*) FILTER (WHERE status = 'error') AS errors,
			COALESCE(MAX(latency_ms) FILTER (WHERE b = 19), 0) AS p95
		FROM scoped
		GROUP BY tool_name, workspace_id`
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("tool window stats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []toolWindowStat
	for rows.Next() {
		var s toolWindowStat
		if err := rows.Scan(&s.tool, &s.ws, &s.calls, &s.errors, &s.p95); err != nil {
			return nil, fmt.Errorf("scan tool window stat: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// AuditSecurityEvents surfaces security-relevant signal over [now-window,
// now]: blocked/denied counts, denial_reason rows, cross-org tier shares,
// and secret.* tool-access spikes.
func (d *DB) AuditSecurityEvents(
	ctx context.Context, ws string, window time.Duration,
) ([]store.AuditAlert, error) {
	if window <= 0 {
		window = time.Hour
	}
	now := time.Now().UTC()
	from := now.Add(-window)
	var alerts []store.AuditAlert

	blocked, err := d.securityCount(ctx, ws, from, now, "status IN ('blocked','denied')")
	if err != nil {
		return nil, err
	}
	if blocked > 0 {
		alerts = append(alerts, store.AuditAlert{
			ID: "security:blocked:" + ws, Kind: "security",
			Severity: severityForCount(blocked, 25), Title: "Blocked/denied calls",
			Detail:      fmt.Sprintf("%d blocked or denied tool calls in window", blocked),
			WorkspaceID: ws, Count: blocked, FirstSeen: from, LastSeen: now,
			Filter: map[string]any{"status": "blocked"},
		})
	}

	denials, err := d.securityCount(ctx, ws, from, now, "COALESCE(denial_reason,'') != ''")
	if err != nil {
		return nil, err
	}
	if denials > 0 {
		alerts = append(alerts, store.AuditAlert{
			ID: "security:denial_reason:" + ws, Kind: "security",
			Severity: severityForCount(denials, 25), Title: "Policy denials",
			Detail:      fmt.Sprintf("%d calls carried a denial_reason", denials),
			WorkspaceID: ws, Count: denials, FirstSeen: from, LastSeen: now,
		})
	}

	crossOrg, err := d.securityCount(ctx, ws, from, now, "tier = 'cross_org'")
	if err != nil {
		return nil, err
	}
	if crossOrg > 0 {
		alerts = append(alerts, store.AuditAlert{
			ID: "security:cross_org:" + ws, Kind: "security",
			Severity: "warning", Title: "Cross-org shares",
			Detail:      fmt.Sprintf("%d cross-org boundary shares in window", crossOrg),
			WorkspaceID: ws, Count: crossOrg, FirstSeen: from, LastSeen: now,
			Filter: map[string]any{"tier": "cross_org"},
		})
	}

	secrets, err := d.securityCount(ctx, ws, from, now, "tool_name LIKE 'secret%'")
	if err != nil {
		return nil, err
	}
	if secrets >= 50 {
		alerts = append(alerts, store.AuditAlert{
			ID: "security:secret_access:" + ws, Kind: "security",
			Severity: severityForCount(secrets, 200), Title: "Secret-access spike",
			Detail:      fmt.Sprintf("%d secret.* tool calls in window", secrets),
			WorkspaceID: ws, Count: secrets, FirstSeen: from, LastSeen: now,
		})
	}
	return alerts, nil
}

// securityCount runs a scoped COUNT(*) over the window with an extra
// predicate. The predicate is a fixed string literal (never user input).
func (d *DB) securityCount(
	ctx context.Context, ws string, from, to time.Time, predicate string,
) (int, error) {
	args := []any{formatTime(from), formatTime(to)}
	wsClause := ""
	if ws != "" {
		wsClause = " AND workspace_id = ?"
		args = append(args, ws)
	}
	var n int
	err := d.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_records
		 WHERE timestamp >= ? AND timestamp <= ?`+wsClause+` AND `+predicate,
		args...,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("security count: %w", err)
	}
	return n, nil
}
