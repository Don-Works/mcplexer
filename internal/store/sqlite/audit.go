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
	dataWhere := qualifyAmbiguousColumns(where)
	dataArgs := append([]any{}, args...)

	// Keyset cursor (time_* sorts only) overrides offset; append the
	// comparison to the qualified WHERE. latency_* sorts keep offset
	// paging because the opaque cursor token doesn't carry the latency key.
	useOffset := true
	if f.CursorTs != nil {
		if clause, kargs := auditKeysetClause(f.Sort, *f.CursorTs, f.CursorID); clause != "" {
			if dataWhere == "" {
				// No base WHERE — turn the leading " AND" into " WHERE".
				dataWhere = " WHERE 1=1" + clause
			} else {
				dataWhere += clause
			}
			dataArgs = append(dataArgs, kargs...)
			useOffset = false
		}
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
		dataWhere +
		` ORDER BY ` + auditSortOrderBy(f.Sort) + ` LIMIT ?`
	dataArgs = append(dataArgs, limit)
	if useOffset {
		dataQ += ` OFFSET ?`
		dataArgs = append(dataArgs, f.Offset)
	}

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

// CountChildCLIToolCallsBySession is the session-attributed counterpart of
// CountChildCLIToolCalls: same row filter, but grouped by the audit row's
// session_id and INNER JOINed to the sessions row that produced it, so a
// caller can tell WHICH MCP client each block of tool calls came from.
//
// Two extra constraints beyond the flat count, both load-bearing:
//
//   - The join is INNER. An audit row whose session row has been deleted
//     cannot be attributed to anything, so it is dropped rather than
//     silently folded into some run's total.
//   - sessions.connected_at must fall inside [start, end]. A CLI child's MCP
//     connection is opened by a subprocess the run itself spawned, so its
//     session always begins during the run window. The operator's own
//     long-lived orchestrator session (which announces the same client_type
//     as a claude_cli child — "claude-code") connected long before, and is
//     excluded here. This is the filter that stops a parent Claude Code
//     session's tool calls being billed to its own workers.
//
// Rows are returned newest-connection-last (connected_at ASC) so callers can
// reason about session overlap without re-sorting. An empty workspaceID or
// clientTypes short-circuits to nil, matching CountChildCLIToolCalls.
func (d *DB) CountChildCLIToolCallsBySession(
	ctx context.Context, workspaceID string, start, end time.Time, clientTypes []string,
) ([]store.ChildCLISessionCount, error) {
	if workspaceID == "" || len(clientTypes) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(clientTypes))
	placeholders = placeholders[:len(placeholders)-1] // drop trailing comma
	startStr, endStr := formatTime(start), formatTime(end)
	args := []any{workspaceID, startStr, endStr}
	for _, ct := range clientTypes {
		args = append(args, ct)
	}
	args = append(args, startStr, endStr)

	rows, err := d.q.QueryContext(ctx, `
		SELECT a.session_id, s.client_type, s.connected_at, s.disconnected_at,
		       COUNT(*)
		FROM audit_records a
		JOIN sessions s ON s.id = a.session_id
		WHERE a.workspace_id = ?
		  AND a.timestamp >= ?
		  AND a.timestamp <= ?
		  AND COALESCE(a.actor_kind, '') != 'worker'
		  AND a.client_type IN (`+placeholders+`)
		  AND a.status = 'success'
		  AND s.connected_at >= ?
		  AND s.connected_at <= ?
		GROUP BY a.session_id
		ORDER BY s.connected_at ASC`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("count child CLI tool calls by session: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.ChildCLISessionCount
	for rows.Next() {
		var c store.ChildCLISessionCount
		var connectedAt string
		var disconnectedAt *string
		if err := rows.Scan(&c.SessionID, &c.ClientType, &connectedAt,
			&disconnectedAt, &c.Count); err != nil {
			return nil, fmt.Errorf("scan child CLI session count: %w", err)
		}
		c.ConnectedAt = parseTime(connectedAt)
		c.DisconnectedAt = parseTimePtr(disconnectedAt)
		out = append(out, c)
	}
	return out, rows.Err()
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
	// downstream_server_id lives on BOTH audit_records and route_rules
	// (rr), so a bare reference is ambiguous in the JOINed data query.
	// Rewrite first — before workspace_id, since neither token is a
	// substring of the other and order is otherwise irrelevant here.
	where = strings.ReplaceAll(where, "downstream_server_id", "r.downstream_server_id")
	where = strings.ReplaceAll(where, "workspace_id", "r.workspace_id")
	// `id = ?` and `id IN (...)` are the two places a bare `id` lands
	// (buildAuditWhere uses bare names; the free-text filter emits the FTS
	// `id IN (SELECT id FROM audit_records_fts ...)` subquery). Without
	// qualification `id` is ambiguous across the audit_records /
	// route_rules / downstream_servers joins. The inner `SELECT id FROM
	// audit_records_fts` is untouched — it has no leading " id = " / " id IN "
	// padding, so only the outer reference is rewritten. The trailing space
	// disambiguates from `route_rule_id`, `session_id`, etc.
	where = strings.ReplaceAll(where, " id = ", " r.id = ")
	where = strings.ReplaceAll(where, " id IN ", " r.id IN ")
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
	// Richer exact-match filters (audit overhaul). Every column here lives
	// only on audit_records (no collision with the route_rules /
	// downstream_servers joins), so bare names are safe through
	// qualifyAmbiguousColumns.
	if f.ActorKind != nil {
		conds = append(conds, "actor_kind = ?")
		args = append(args, *f.ActorKind)
	}
	if f.ActorID != nil {
		conds = append(conds, "actor_id = ?")
		args = append(args, *f.ActorID)
	}
	if f.DownstreamServerID != nil {
		conds = append(conds, "downstream_server_id = ?")
		args = append(args, *f.DownstreamServerID)
	}
	if f.RouteRuleID != nil {
		conds = append(conds, "route_rule_id = ?")
		args = append(args, *f.RouteRuleID)
	}
	if f.ClientType != nil {
		conds = append(conds, "client_type = ?")
		args = append(args, *f.ClientType)
	}
	if f.ErrorCode != nil {
		conds = append(conds, "error_code = ?")
		args = append(args, *f.ErrorCode)
	}
	if f.Tier != nil {
		conds = append(conds, "tier = ?")
		args = append(args, *f.Tier)
	}
	if f.CacheHit != nil {
		v := 0
		if *f.CacheHit {
			v = 1
		}
		conds = append(conds, "cache_hit = ?")
		args = append(args, v)
	}
	if f.MinLatencyMs != nil {
		conds = append(conds, "latency_ms >= ?")
		args = append(args, *f.MinLatencyMs)
	}
	// Free-text: AND-restrict to the FTS index. The subquery's bare `id`
	// and `workspace_id` references stay inside the audit_records_fts
	// vtable, so qualifyAmbiguousColumns (which rewrites " id = " and
	// "workspace_id") must NOT touch them — guarded by auditFTSSentinel
	// below.
	if expr := sanitizeFTS5Query(f.Q); expr != "" {
		conds = append(conds, "id IN (SELECT id FROM audit_records_fts WHERE audit_records_fts MATCH ?)")
		args = append(args, expr)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// auditSortOrderBy maps the validated Sort allowlist to an ORDER BY clause
// (column-qualified for the JOINed data query). Unknown / empty sort falls
// back to time_desc. time_* sorts break ties on id so keyset pagination is
// stable; latency_* sorts add timestamp,id tiebreakers for determinism.
func auditSortOrderBy(sort string) string {
	switch sort {
	case "time_asc":
		return "r.timestamp ASC, r.id ASC"
	case "latency_desc":
		return "r.latency_ms DESC, r.timestamp DESC, r.id DESC"
	case "latency_asc":
		return "r.latency_ms ASC, r.timestamp ASC, r.id ASC"
	default: // time_desc
		return "r.timestamp DESC, r.id DESC"
	}
}

// auditKeysetClause builds the (timestamp,id) keyset comparison for the
// time_* sorts. latency_* sorts fall back to offset paging (their primary
// key isn't carried in the opaque cursor token), so this returns ("", nil)
// for them. cursorTs/cursorID come from the decoded cursor token.
func auditKeysetClause(sort string, cursorTs time.Time, cursorID string) (string, []any) {
	switch sort {
	case "time_asc":
		// page forward: rows strictly after the cursor.
		return " AND (r.timestamp > ? OR (r.timestamp = ? AND r.id > ?))",
			[]any{formatTime(cursorTs), formatTime(cursorTs), cursorID}
	case "time_desc", "":
		return " AND (r.timestamp < ? OR (r.timestamp = ? AND r.id < ?))",
			[]any{formatTime(cursorTs), formatTime(cursorTs), cursorID}
	default:
		// latency_* — opaque cursor can't express the latency key; caller
		// keeps offset paging.
		return "", nil
	}
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
