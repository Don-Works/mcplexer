package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

func (d *DB) InsertAuditRecord(ctx context.Context, r *store.AuditRecord) error {
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now().UTC()
	}
	if r.CreatedAt.IsZero() {
		r.CreatedAt = time.Now().UTC()
	}

	params := normalizeJSON(r.ParamsRedacted, "{}")

	cacheHit := 0
	if r.CacheHit {
		cacheHit = 1
	}

	var skillID any
	if r.SkillID != nil {
		skillID = *r.SkillID
	}

	// Tier-consent columns (migration 082). Pass NULL when empty so the
	// dashboard JSON omits the field (consistent with json:omitempty on
	// the model). Storing "" vs NULL distinguishes "explicitly empty
	// envelope" from "legacy row" downstream — we always use NULL on
	// empty.
	tier := nullableString(r.Tier)
	acceptedBy := nullableJSON(r.AcceptedBy)
	grantOrigin := nullableJSON(r.GrantOrigin)
	denialReason := nullableString(r.DenialReason)

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO audit_records
			(id, timestamp, session_id, client_type, model, workspace_id,
			 workspace_name, subpath, tool_name, params_redacted, route_rule_id,
			 downstream_server_id, downstream_instance_id, auth_scope_id,
			 status, error_code, error_message, latency_ms, response_size,
			 cache_hit, execution_id, skill_id, created_at,
			 actor_kind, actor_id, correlation_id,
			 tier, accepted_by, grant_origin, denial_reason)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, formatTime(r.Timestamp), r.SessionID, r.ClientType, r.Model,
		r.WorkspaceID, r.WorkspaceName, r.Subpath, r.ToolName, params, r.RouteRuleID,
		r.DownstreamServerID, r.DownstreamInstanceID, r.AuthScopeID,
		r.Status, r.ErrorCode, r.ErrorMessage, r.LatencyMs, r.ResponseSize,
		cacheHit, r.ExecutionID, skillID, formatTime(r.CreatedAt),
		r.ActorKind, r.ActorID, r.CorrelationID,
		tier, acceptedBy, grantOrigin, denialReason,
	)
	return err
}

// nullableString returns nil when s is empty, otherwise s. Lets the
// driver write NULL so dashboards can distinguish "unset" from "set to
// empty string".
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableJSON returns nil when raw is empty, otherwise the raw bytes
// as a string (modernc/sqlite accepts both []byte and string for TEXT
// columns; string is the more portable choice).
func nullableJSON(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	return string(raw)
}

func (d *DB) QueryAuditRecords(
	ctx context.Context, f store.AuditFilter,
) ([]store.AuditRecord, int, error) {
	where, args := buildAuditWhere(f)

	// Count total.
	var total int
	countQ := "SELECT COUNT(*) FROM audit_records" + where
	if err := d.q.QueryRowContext(ctx, countQ, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	// Fetch page.
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	dataQ := `SELECT
		r.id, r.timestamp, r.session_id, r.client_type, r.model, r.workspace_id,
		r.workspace_name, r.subpath, r.tool_name, r.params_redacted, r.route_rule_id,
		r.downstream_server_id, r.downstream_instance_id, r.auth_scope_id,
		r.status, r.error_code, r.error_message, r.latency_ms, r.response_size,
		r.cache_hit, r.execution_id, r.skill_id, r.created_at,
		r.actor_kind, r.actor_id, r.correlation_id,
		r.tier, r.accepted_by, r.grant_origin, r.denial_reason,
		COALESCE(rr.path_glob, '') as route_rule_summary,
		COALESCE(ds.name, '') as downstream_server_name
		FROM audit_records r
		LEFT JOIN route_rules rr ON r.route_rule_id = rr.id
		LEFT JOIN downstream_servers ds ON r.downstream_server_id = ds.id ` +
		qualifyAmbiguousColumns(where) +
		` ORDER BY r.timestamp DESC LIMIT ? OFFSET ?`
	dataArgs := append(args, limit, f.Offset)

	rows, err := d.q.QueryContext(ctx, dataQ, dataArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.AuditRecord
	for rows.Next() {
		r, err := scanAuditRow(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *r)
	}
	return out, total, rows.Err()
}

func (d *DB) GetAuditStats(
	ctx context.Context, workspaceID string, after, before time.Time,
) (*store.AuditStats, error) {
	var s store.AuditStats

	var whereClause string
	var args []any
	if workspaceID != "" {
		whereClause = "WHERE workspace_id = ? AND timestamp >= ? AND timestamp <= ?"
		args = []any{workspaceID, formatTime(after), formatTime(before)}
	} else {
		whereClause = "WHERE timestamp >= ? AND timestamp <= ?"
		args = []any{formatTime(after), formatTime(before)}
	}

	err := d.q.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status = 'success'),
			COUNT(*) FILTER (WHERE status = 'error'),
			COUNT(*) FILTER (WHERE status = 'blocked'),
			COALESCE(AVG(latency_ms), 0)
		FROM audit_records
		`+whereClause,
		args...,
	).Scan(&s.TotalRequests, &s.SuccessCount, &s.ErrorCount, &s.BlockedCount, &s.AvgLatencyMs)
	if err != nil {
		return nil, err
	}

	// P95 latency approximation.
	err = d.q.QueryRowContext(ctx, `
		SELECT COALESCE(latency_ms, 0) FROM audit_records
		`+whereClause+`
		ORDER BY latency_ms ASC
		LIMIT 1 OFFSET (
			SELECT CAST(COUNT(*) * 0.95 AS INTEGER) FROM audit_records
			`+whereClause+`
		)`,
		append(args, args...)...,
	).Scan(&s.P95LatencyMs)
	if err != nil {
		// No rows is fine — P95 stays 0.
		s.P95LatencyMs = 0
	}
	return &s, nil
}

func (d *DB) GetDashboardTimeSeries(
	ctx context.Context, after, before time.Time,
) ([]store.TimeSeriesPoint, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT
			strftime('%Y-%m-%dT%H:%M:00Z', timestamp) AS bucket,
			COUNT(DISTINCT session_id) AS sessions,
			COUNT(DISTINCT downstream_server_id) AS servers,
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE status = 'error') AS errors
		FROM audit_records
		WHERE timestamp >= ? AND timestamp <= ?
		GROUP BY bucket
		ORDER BY bucket ASC`,
		formatTime(after), formatTime(before),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.TimeSeriesPoint
	for rows.Next() {
		var p store.TimeSeriesPoint
		var bucket string
		if err := rows.Scan(&bucket, &p.Sessions, &p.Servers, &p.Total, &p.Errors); err != nil {
			return nil, fmt.Errorf("scan time series row: %w", err)
		}
		p.Bucket = parseTime(bucket)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) GetDashboardTimeSeriesBucketed(
	ctx context.Context, after, before time.Time, bucketSec int,
) ([]store.TimeSeriesPoint, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT
			strftime('%Y-%m-%dT%H:%M:%SZ', (CAST(strftime('%s', timestamp) AS INTEGER) / ?) * ?, 'unixepoch') AS bucket,
			COUNT(DISTINCT session_id) AS sessions,
			COUNT(DISTINCT downstream_server_id) AS servers,
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE status = 'error') AS errors,
			COALESCE(AVG(latency_ms), 0) AS avg_latency_ms
		FROM audit_records
		WHERE timestamp >= ? AND timestamp <= ?
		GROUP BY bucket
		ORDER BY bucket ASC`,
		bucketSec, bucketSec, formatTime(after), formatTime(before),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.TimeSeriesPoint
	for rows.Next() {
		var p store.TimeSeriesPoint
		var bucket string
		if err := rows.Scan(&bucket, &p.Sessions, &p.Servers, &p.Total, &p.Errors, &p.AvgLatencyMs); err != nil {
			return nil, fmt.Errorf("scan bucketed time series row: %w", err)
		}
		p.Bucket = parseTime(bucket)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (d *DB) GetToolLeaderboard(
	ctx context.Context, after, before time.Time, limit int,
) ([]store.ToolLeaderboardEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := d.q.QueryContext(ctx, `
		WITH ranked AS (
			SELECT tool_name, latency_ms,
				NTILE(20) OVER (PARTITION BY tool_name ORDER BY latency_ms) AS bucket
			FROM audit_records
			WHERE timestamp >= ? AND timestamp <= ?
		)
		SELECT
			r.tool_name,
			COALESCE(ds.name, '') AS server_name,
			COUNT(*) AS call_count,
			COUNT(*) FILTER (WHERE r.status = 'error') AS error_count,
			COALESCE(AVG(r.latency_ms), 0) AS avg_latency_ms,
			COALESCE((
				SELECT MAX(rk.latency_ms) FROM ranked rk
				WHERE rk.tool_name = r.tool_name AND rk.bucket = 19
			), 0) AS p95_latency_ms
		FROM audit_records r
		LEFT JOIN downstream_servers ds ON r.downstream_server_id = ds.id
		WHERE r.timestamp >= ? AND r.timestamp <= ?
		GROUP BY r.tool_name
		ORDER BY call_count DESC
		LIMIT ?`,
		formatTime(after), formatTime(before),
		formatTime(after), formatTime(before), limit,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.ToolLeaderboardEntry
	for rows.Next() {
		var e store.ToolLeaderboardEntry
		if err := rows.Scan(&e.ToolName, &e.ServerName, &e.CallCount, &e.ErrorCount, &e.AvgLatencyMs, &e.P95LatencyMs); err != nil {
			return nil, fmt.Errorf("scan tool leaderboard row: %w", err)
		}
		if e.CallCount > 0 {
			e.ErrorRate = float64(e.ErrorCount) / float64(e.CallCount) * 100
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) GetServerHealth(
	ctx context.Context, after, before time.Time,
) ([]store.ServerHealthEntry, error) {
	rows, err := d.q.QueryContext(ctx, `
		WITH ranked AS (
			SELECT downstream_server_id, latency_ms,
				NTILE(20) OVER (PARTITION BY downstream_server_id ORDER BY latency_ms) AS bucket
			FROM audit_records
			WHERE timestamp >= ? AND timestamp <= ?
		)
		SELECT
			r.downstream_server_id,
			COALESCE(ds.name, r.downstream_server_id) AS server_name,
			COUNT(*) AS call_count,
			COUNT(*) FILTER (WHERE r.status = 'error') AS error_count,
			COALESCE(AVG(r.latency_ms), 0) AS avg_latency_ms,
			COALESCE((
				SELECT MAX(rk.latency_ms) FROM ranked rk
				WHERE rk.downstream_server_id = r.downstream_server_id AND rk.bucket = 19
			), 0) AS p95_latency_ms
		FROM audit_records r
		LEFT JOIN downstream_servers ds ON r.downstream_server_id = ds.id
		WHERE r.timestamp >= ? AND r.timestamp <= ?
		GROUP BY r.downstream_server_id
		ORDER BY call_count DESC`,
		formatTime(after), formatTime(before),
		formatTime(after), formatTime(before),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.ServerHealthEntry
	for rows.Next() {
		var e store.ServerHealthEntry
		if err := rows.Scan(&e.ServerID, &e.ServerName, &e.CallCount, &e.ErrorCount, &e.AvgLatencyMs, &e.P95LatencyMs); err != nil {
			return nil, fmt.Errorf("scan server health row: %w", err)
		}
		if e.CallCount > 0 {
			e.ErrorRate = float64(e.ErrorCount) / float64(e.CallCount) * 100
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) GetErrorBreakdown(
	ctx context.Context, after, before time.Time, limit int,
) ([]store.ErrorBreakdownEntry, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT
			r.tool_name AS group_key,
			COALESCE(ds.name, '') AS server_name,
			CASE
				WHEN r.status = 'blocked' THEN 'blocked'
				ELSE 'error'
			END AS error_type,
			COUNT(*) AS cnt
		FROM audit_records r
		LEFT JOIN downstream_servers ds ON r.downstream_server_id = ds.id
		WHERE r.status IN ('error', 'blocked') AND r.timestamp >= ? AND r.timestamp <= ?
		GROUP BY r.tool_name, error_type
		ORDER BY cnt DESC
		LIMIT ?`,
		formatTime(after), formatTime(before), limit,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.ErrorBreakdownEntry
	for rows.Next() {
		var e store.ErrorBreakdownEntry
		if err := rows.Scan(&e.GroupKey, &e.ServerName, &e.ErrorType, &e.Count); err != nil {
			return nil, fmt.Errorf("scan error breakdown row: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) GetRouteHitMap(
	ctx context.Context, after, before time.Time,
) ([]store.RouteHitEntry, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT
			r.route_rule_id,
			COALESCE(rr.name, '') AS rule_name,
			COALESCE(rr.path_glob, '') AS path_glob,
			COUNT(*) AS hit_count,
			COUNT(*) FILTER (WHERE r.status = 'error') AS error_count
		FROM audit_records r
		LEFT JOIN route_rules rr ON r.route_rule_id = rr.id
		WHERE r.timestamp >= ? AND r.timestamp <= ?
		GROUP BY r.route_rule_id
		ORDER BY hit_count DESC`,
		formatTime(after), formatTime(before),
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []store.RouteHitEntry
	for rows.Next() {
		var e store.RouteHitEntry
		if err := rows.Scan(&e.RouteRuleID, &e.RuleName, &e.PathGlob, &e.HitCount, &e.ErrorCount); err != nil {
			return nil, fmt.Errorf("scan route hit map row: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (d *DB) GetAuditCacheStats(
	ctx context.Context, after, before time.Time,
) (*store.AuditCacheStats, error) {
	var s store.AuditCacheStats
	err := d.q.QueryRowContext(ctx, `
		SELECT
			COUNT(*) FILTER (WHERE cache_hit = 1) AS hits,
			COUNT(*) FILTER (WHERE cache_hit = 0 AND status IN ('success', 'blocked')) AS misses
		FROM audit_records
		WHERE timestamp >= ? AND timestamp <= ?
			AND tool_name NOT LIKE 'mcplexer__%'`,
		formatTime(after), formatTime(before),
	).Scan(&s.Hits, &s.Misses)
	if err != nil {
		return nil, err
	}
	total := s.Hits + s.Misses
	if total > 0 {
		s.HitRate = float64(s.Hits) / float64(total)
	}
	return &s, nil
}

// CountChildCLIToolCalls returns the number of audit_records produced
// by a CLI-child MCP session inside the given (workspace, time-window).
// Used by workers/admin to derive WorkerRun.tool_calls_count for the
// claude_cli / opencode_cli / grok_cli / mimo_cli adapter families — see store.AuditStore for
// the filter spec.
//
// Returns 0 when workspaceID is empty (the WorkerRun row's WorkspaceID
// is denormalised at run creation, so a zero value means "not bound to
// a workspace" and there's no safe window to count over). Returns 0
// when clientTypes is empty rather than building an `IN ()` that
// SQLite rejects.
//
// Implementation note: the query uses the (workspace_id, timestamp)
// index from migration 001 so the time-window filter stays cheap even
// on a large audit_records table.
func (d *DB) CountChildCLIToolCalls(
	ctx context.Context, workspaceID string, start, end time.Time, clientTypes []string,
) (int, error) {
	if workspaceID == "" || len(clientTypes) == 0 {
		return 0, nil
	}
	placeholders := strings.Repeat("?,", len(clientTypes))
	placeholders = placeholders[:len(placeholders)-1] // drop trailing comma
	args := []any{workspaceID, formatTime(start), formatTime(end)}
	for _, ct := range clientTypes {
		args = append(args, ct)
	}
	var n int
	err := d.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM audit_records
		WHERE workspace_id = ?
		  AND timestamp >= ?
		  AND timestamp <= ?
		  AND COALESCE(actor_kind, '') != 'worker'
		  AND client_type IN (`+placeholders+`)
		  AND status = 'success'`,
		args...,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count child CLI tool calls: %w", err)
	}
	return n, nil
}

// PruneAuditRecords deletes audit_records whose created_at is older than
// `before`. Safe to call concurrently — SQLite serialises writes and the
// statement is a single-shot DELETE. Returns the number of rows removed
// (0 when nothing matches, which is the normal idle state).
func (d *DB) PruneAuditRecords(
	ctx context.Context, before time.Time,
) (int64, error) {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM audit_records WHERE created_at < ?`,
		formatTime(before),
	)
	if err != nil {
		return 0, fmt.Errorf("prune audit_records: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune audit_records rows affected: %w", err)
	}
	return n, nil
}

// qualifyAmbiguousColumns rewrites bare column references in the WHERE
// clause that collide with route_rules / downstream_servers joins
// (id, workspace_id). buildAuditWhere can't know whether its output
// will be used against the bare audit_records table or the JOINed
// dataQ, so the rewrite lives here at the call site. Order-sensitive:
// "id =" is rewritten last so "workspace_id" → "r.workspace_id"
// doesn't get re-rewritten to "r.workspace_r.id".
func qualifyAmbiguousColumns(where string) string {
	where = strings.ReplaceAll(where, "workspace_id", "r.workspace_id")
	// `id = ?` is the only place a bare `id` lands today (buildAuditWhere
	// uses bare names). The trailing space disambiguates from
	// `route_rule_id`, `session_id`, etc. Both sides of `=` get a
	// padding so we don't munge those.
	where = strings.ReplaceAll(where, " id = ", " r.id = ")
	return where
}

func buildAuditWhere(f store.AuditFilter) (string, []any) {
	var conds []string
	var args []any
	if f.ID != nil {
		conds = append(conds, "id = ?")
		args = append(args, *f.ID)
	}
	if f.SessionID != nil {
		conds = append(conds, "session_id = ?")
		args = append(args, *f.SessionID)
	}
	if f.WorkspaceID != nil {
		conds = append(conds, "workspace_id = ?")
		args = append(args, *f.WorkspaceID)
	}
	if f.ToolName != nil {
		conds = append(conds, "tool_name = ?")
		args = append(args, *f.ToolName)
	}
	if f.Status != nil {
		conds = append(conds, "status = ?")
		args = append(args, *f.Status)
	}
	if f.ExecutionID != nil {
		conds = append(conds, "execution_id = ?")
		args = append(args, *f.ExecutionID)
	}
	if f.After != nil {
		conds = append(conds, "timestamp >= ?")
		args = append(args, formatTime(*f.After))
	}
	if f.Before != nil {
		conds = append(conds, "timestamp <= ?")
		args = append(args, formatTime(*f.Before))
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

func scanAuditRow(row rowScanner) (*store.AuditRecord, error) {
	var r store.AuditRecord
	var ts, createdAt, params string
	var cacheHit int
	var skillID sql.NullString
	var tier, acceptedBy, grantOrigin, denialReason sql.NullString
	err := row.Scan(
		&r.ID, &ts, &r.SessionID, &r.ClientType, &r.Model,
		&r.WorkspaceID, &r.WorkspaceName, &r.Subpath, &r.ToolName, &params,
		&r.RouteRuleID, &r.DownstreamServerID, &r.DownstreamInstanceID,
		&r.AuthScopeID, &r.Status, &r.ErrorCode, &r.ErrorMessage,
		&r.LatencyMs, &r.ResponseSize, &cacheHit, &r.ExecutionID, &skillID, &createdAt,
		&r.ActorKind, &r.ActorID, &r.CorrelationID,
		&tier, &acceptedBy, &grantOrigin, &denialReason,
		&r.RouteRuleSummary, &r.DownstreamServerName,
	)
	if err != nil {
		return nil, fmt.Errorf("scan audit row: %w", err)
	}
	r.ParamsRedacted = json.RawMessage(params)
	r.CacheHit = cacheHit != 0
	r.Timestamp = parseTime(ts)
	r.CreatedAt = parseTime(createdAt)
	if skillID.Valid {
		v := skillID.String
		r.SkillID = &v
	}
	if tier.Valid {
		r.Tier = tier.String
	}
	if acceptedBy.Valid && acceptedBy.String != "" {
		r.AcceptedBy = json.RawMessage(acceptedBy.String)
	}
	if grantOrigin.Valid && grantOrigin.String != "" {
		r.GrantOrigin = json.RawMessage(grantOrigin.String)
	}
	if denialReason.Valid {
		r.DenialReason = denialReason.String
	}
	return &r, nil
}
