package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

const workerRunCols = `id, worker_id, workspace_id, started_at, finished_at, duration_ms,
    status, prompt_rendered, model_provider, model_id,
    input_tokens, output_tokens, cost_usd, billing_model, subscription_bucket, real_cost_usd, tool_calls_count,
	output_text, error, mesh_message_ids_json, audit_record_ids_json,
	result_branch, result_commit, result_changed,
	trigger_kind, trigger_message_id, trigger_source_peer, trigger_chain_depth`

// CreateWorkerRun inserts one execution-ledger row. ID is generated when
// empty. StartedAt defaults to now. JSON columns default to "[]" when
// empty. Status defaults to "running".
func (d *DB) CreateWorkerRun(ctx context.Context, r *store.WorkerRun) error {
	if r == nil {
		return errors.New("CreateWorkerRun: run required")
	}
	if r.WorkerID == "" {
		return errors.New("CreateWorkerRun: worker_id required")
	}
	if r.ID == "" {
		r.ID = uuid.NewString()
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	if r.Status == "" {
		r.Status = "running"
	}
	if r.MeshMessageIDsJSON == "" {
		r.MeshMessageIDsJSON = "[]"
	}
	if r.AuditRecordIDsJSON == "" {
		r.AuditRecordIDsJSON = "[]"
	}
	triggerKind := r.TriggerKind
	if triggerKind == "" {
		triggerKind = "schedule"
	}
	// Backfill workspace_id from the parent worker if the caller didn't
	// set it explicitly. Keeps the runner code site-agnostic — it only
	// needs the worker_id and the DB layer denormalizes from there.
	if r.WorkspaceID == "" {
		var ws sql.NullString
		_ = d.q.QueryRowContext(ctx, `SELECT workspace_id FROM workers WHERE id = ?`, r.WorkerID).Scan(&ws)
		if ws.Valid {
			r.WorkspaceID = ws.String
		}
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO worker_runs (`+workerRunCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		        ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.WorkerID, r.WorkspaceID, formatTime(r.StartedAt),
		formatTimePtr(r.FinishedAt), r.DurationMS,
		r.Status, r.PromptRendered, r.ModelProvider, r.ModelID,
		r.InputTokens, r.OutputTokens, r.CostUSD,
		r.BillingModel, r.SubscriptionBucket, r.RealCostUSD,
		r.ToolCallsCount,
		r.OutputText, r.Error, r.MeshMessageIDsJSON, r.AuditRecordIDsJSON,
		r.ResultBranch, r.ResultCommit, r.ResultChanged,
		triggerKind, r.TriggerMessageID, r.TriggerSourcePeer, r.TriggerChainDepth,
	)
	if err != nil {
		return fmt.Errorf("insert worker_run: %w", err)
	}
	return nil
}

// UpdateWorkerRunStatus commits the terminal snapshot captured in fin.
// DurationMS is derived from the row's started_at when fin's value is
// zero. Returns ErrWorkerRunNotFound when no row matches.
func (d *DB) UpdateWorkerRunStatus(
	ctx context.Context, runID string, fin store.WorkerRunFinalize,
) error {
	if runID == "" {
		return errors.New("UpdateWorkerRunStatus: runID required")
	}
	if fin.Status == "" {
		return errors.New("UpdateWorkerRunStatus: status required")
	}
	if fin.FinishedAt.IsZero() {
		return errors.New("UpdateWorkerRunStatus: finishedAt required")
	}
	dur, err := d.resolveRunDuration(ctx, runID, fin.FinishedAt)
	if err != nil {
		return err
	}
	// The `status != 'cancelled'` guard makes operator hard-stop the
	// single source of truth for a cancelled run: once CancelRun (or a
	// live runner finalize) has written status='cancelled', a LATER
	// natural-completion finalize from a runner that was already mid-kill
	// must NOT clobber that terminal state back to success/failure. Every
	// other transition (running→success, awaiting_approval→rejected,
	// running→failure, …) is unaffected because the source row is never
	// 'cancelled'.
	res, err := d.q.ExecContext(ctx, `
		UPDATE worker_runs
		SET status = ?, finished_at = ?, duration_ms = ?,
		    input_tokens = ?, output_tokens = ?, cost_usd = ?,
		    tool_calls_count = ?, output_text = ?, error = ?,
		    mesh_message_ids_json = ?, audit_record_ids_json = ?,
		    billing_model = ?, subscription_bucket = ?, real_cost_usd = ?,
		    result_branch = CASE WHEN ? THEN ? ELSE result_branch END,
		    result_commit = CASE WHEN ? THEN ? ELSE result_commit END,
		    result_changed = CASE WHEN ? THEN ? ELSE result_changed END
		WHERE id = ? AND status != 'cancelled'`,
		fin.Status, formatTime(fin.FinishedAt), dur,
		fin.InputTokens, fin.OutputTokens, fin.CostUSD,
		fin.ToolCallsCount, fin.OutputText, fin.Error,
		nonEmptyJSON(fin.MeshMessageIDsJSON, "[]"),
		nonEmptyJSON(fin.AuditRecordIDsJSON, "[]"),
		fin.BillingModel, fin.SubscriptionBucket, fin.RealCostUSD,
		fin.HasGitResult, fin.ResultBranch,
		fin.HasGitResult, fin.ResultCommit,
		fin.HasGitResult, fin.ResultChanged,
		runID,
	)
	if err != nil {
		return fmt.Errorf("update worker_run status: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		// Either the row is genuinely missing → NotFound, or it is
		// already 'cancelled' → silent no-op so the late finalize doesn't
		// overwrite the operator's terminal state.
		var st string
		switch scanErr := d.q.QueryRowContext(ctx,
			`SELECT status FROM worker_runs WHERE id = ?`, runID).Scan(&st); {
		case errors.Is(scanErr, sql.ErrNoRows):
			return store.ErrWorkerRunNotFound
		case scanErr != nil:
			return fmt.Errorf("recheck run after guarded finalize: %w", scanErr)
		}
		if st == "cancelled" && fin.HasGitResult {
			artifactRes, artifactErr := d.q.ExecContext(ctx, `
				UPDATE worker_runs
				SET result_branch = ?, result_commit = ?, result_changed = ?
				WHERE id = ? AND status = 'cancelled'`,
				fin.ResultBranch, fin.ResultCommit, fin.ResultChanged, runID,
			)
			if artifactErr != nil {
				return fmt.Errorf("persist cancelled worker_run snapshot: %w", artifactErr)
			}
			if artifactAffected, _ := artifactRes.RowsAffected(); artifactAffected == 0 {
				return errors.New("cancelled worker_run changed while persisting snapshot")
			}
		}
		return nil
	}
	return nil
}

// ReapOrphanedRunningRuns marks every worker_run row that is still in
// status='running' or status='dispatched' (and is NOT owned by a
// delegation worker) AND whose started_at is before startedBefore as
// status='interrupted' with error='interrupted by daemon restart'.
// post-boot rows (started_at >= startedBefore) are left untouched.
//
// Called at daemon startup: pre-boot rows lost their in-process runner
// when the prior process died. The status 'interrupted' is distinct
// from 'failure' so dashboards can tell an operator kill / run bug from
// a clean daemon restart.
//
// Delegation workers (name LIKE 'delegate-%') are excluded — those are
// handled by the separate ResumeOrphanedDelegations path.
//
// duration_ms and finished_at are derived from reasonNow so the audit
// ledger shows the wall-clock the runner WAS alive for (not the
// orphan-detection time). Returns the count of rows swept.
func (d *DB) ReapOrphanedRunningRuns(
	ctx context.Context, startedBefore, reasonNow time.Time,
) (int, error) {
	res, err := d.q.ExecContext(ctx, `
		UPDATE worker_runs
		SET status = 'interrupted',
		    finished_at = ?,
		    duration_ms = CASE
		        WHEN started_at IS NULL OR started_at = '' THEN 0
		        ELSE CAST((julianday(?) - julianday(started_at)) * 86400000 AS INTEGER)
		    END,
		    error = 'interrupted by daemon restart'
		WHERE status IN ('running', 'dispatched')
		  AND started_at < ?
		  AND worker_id NOT IN (SELECT id FROM workers WHERE name LIKE 'delegate-%')`,
		formatTime(reasonNow), formatTime(reasonNow), formatTime(startedBefore))
	if err != nil {
		return 0, fmt.Errorf("reap orphaned worker_runs: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// resolveRunDuration looks up started_at on the row and returns the
// elapsed ms against finishedAt. Returns ErrWorkerRunNotFound when the
// row is missing so the caller can short-circuit.
func (d *DB) resolveRunDuration(
	ctx context.Context, runID string, finishedAt time.Time,
) (int64, error) {
	var startedAtStr string
	err := d.q.QueryRowContext(ctx,
		`SELECT started_at FROM worker_runs WHERE id = ?`, runID,
	).Scan(&startedAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrWorkerRunNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("read started_at: %w", err)
	}
	startedAt := parseTime(startedAtStr)
	if startedAt.IsZero() {
		return 0, nil
	}
	return finishedAt.Sub(startedAt).Milliseconds(), nil
}

func nonEmptyJSON(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// GetWorkerRun returns one row or ErrWorkerRunNotFound.
func (d *DB) GetWorkerRun(ctx context.Context, id string) (*store.WorkerRun, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+workerRunCols+` FROM worker_runs WHERE id = ?`, id)
	r, err := scanWorkerRun(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrWorkerRunNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get worker_run: %w", err)
	}
	return r, nil
}

// ListWorkerRuns returns runs for workerID ordered started_at DESC.
// limit <= 0 falls back to listWorkerRunCap.
func (d *DB) ListWorkerRuns(
	ctx context.Context, workerID string, limit int,
) ([]*store.WorkerRun, error) {
	if limit <= 0 || limit > listWorkerRunCap {
		limit = listWorkerRunCap
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+workerRunCols+`
		FROM worker_runs
		WHERE worker_id = ?
		ORDER BY started_at DESC
		LIMIT ?`,
		workerID, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list worker_runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*store.WorkerRun
	for rows.Next() {
		r, err := scanWorkerRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan worker_run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListRunningWorkerRuns returns every currently running run for one worker.
// It backs the worker-admin disable path, where an operator expects a
// pause toggle to stop all active executions, not merely the most recent
// ledger row.
func (d *DB) ListRunningWorkerRuns(
	ctx context.Context, workerID string,
) ([]*store.WorkerRun, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+workerRunCols+`
		FROM worker_runs
		WHERE worker_id = ? AND status = 'running'
		ORDER BY started_at DESC`,
		workerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list running worker_runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*store.WorkerRun
	for rows.Next() {
		r, err := scanWorkerRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan running worker_run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// CountRunningWorkerRuns returns the number of running-status rows for
// workerID. Backs the concurrency_policy check before scheduling a new
// dispatch.
func (d *DB) CountRunningWorkerRuns(
	ctx context.Context, workerID string,
) (int, error) {
	var n int
	err := d.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM worker_runs
		WHERE worker_id = ? AND status = 'running'`,
		workerID,
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count running worker_runs: %w", err)
	}
	return n, nil
}

// SumCostThisMonth returns the total cost_usd of WorkerRun rows for
// workerID whose started_at falls within the current calendar month in
// UTC. Backs the monthly-budget auto-pause check (M1). NULL/empty
// strings (no runs) collapse to 0.
func (d *DB) SumCostThisMonth(
	ctx context.Context, workerID string, now time.Time,
) (float64, error) {
	monthStart := time.Date(
		now.UTC().Year(), now.UTC().Month(), 1, 0, 0, 0, 0, time.UTC,
	)
	var sum sql.NullFloat64
	err := d.q.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(cost_usd), 0) FROM worker_runs
		WHERE worker_id = ? AND started_at >= ?`,
		workerID, formatTime(monthStart),
	).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum cost this month: %w", err)
	}
	if !sum.Valid {
		return 0, nil
	}
	return sum.Float64, nil
}

// LastFailureStatuses returns the Status of the last n WorkerRun rows
// for workerID, ordered started_at DESC, excluding still-running rows
// AND operator-cancelled rows. Backs the consecutive-failure auto-pause
// check — the caller compares every returned status against "failure".
//
// Cancelled rows are excluded (not merely "not a failure") so an
// operator hard-stop is fully transparent to the streak: it neither
// counts as a failure nor resets a genuine failure run. A human pulling
// the plug on a stuck delegation must not mask, or be mistaken for, a
// worker that is actually failing.
func (d *DB) LastFailureStatuses(
	ctx context.Context, workerID string, n int,
) ([]string, error) {
	if n <= 0 {
		return nil, nil
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT status FROM worker_runs
		WHERE worker_id = ? AND status NOT IN ('running', 'cancelled')
		ORDER BY started_at DESC
		LIMIT ?`,
		workerID, n,
	)
	if err != nil {
		return nil, fmt.Errorf("last failure statuses: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := make([]string, 0, n)
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, fmt.Errorf("scan status: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ReconcileOrphanedRuns flips rows still in status='running' whose
// started_at is older than `olderThan` into status='failure' with the
// supplied reason as the error text. Used at daemon boot to clear runs
// the previous process left dangling (panic, SIGKILL, crash). The
// finished_at + duration_ms columns are derived from the row's
// started_at relative to `now`, so the dashboard duration view stays
// sensible. Returns the number of rows reconciled. Idempotent: a
// follow-up call returns 0 because the rows are no longer 'running'.
//
// Implementation note: we do this as a two-step (SELECT id+started_at
// + UPDATE ... WHERE id = ?) rather than one UPDATE so each row's
// duration_ms is computed individually against its own started_at.
// The single-statement form would either need a SQL expression for
// duration (sqlite has no portable seconds-between helper for our
// stored string timestamps) or wrong-by-a-batch durations.
func (d *DB) ReconcileOrphanedRuns(
	ctx context.Context, olderThan, now time.Time, reason string,
) (int64, error) {
	if reason == "" {
		reason = "orphaned run reconciled at daemon boot"
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, started_at FROM worker_runs
		WHERE status = 'running' AND started_at < ?`,
		formatTime(olderThan),
	)
	if err != nil {
		return 0, fmt.Errorf("scan orphaned runs: %w", err)
	}
	type orphan struct {
		id        string
		startedAt time.Time
	}
	var found []orphan
	for rows.Next() {
		var id, startedAt string
		if err := rows.Scan(&id, &startedAt); err != nil {
			_ = rows.Close()
			return 0, fmt.Errorf("scan orphan row: %w", err)
		}
		found = append(found, orphan{id: id, startedAt: parseTime(startedAt)})
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return 0, fmt.Errorf("scan orphans rows.Err: %w", err)
	}
	_ = rows.Close()
	var n int64
	finishedAtStr := formatTime(now)
	for _, o := range found {
		dur := int64(0)
		if !o.startedAt.IsZero() {
			dur = now.Sub(o.startedAt).Milliseconds()
		}
		res, err := d.q.ExecContext(ctx, `
			UPDATE worker_runs
			SET status = 'failure', finished_at = ?, duration_ms = ?,
			    error = ?
			WHERE id = ? AND status = 'running'`,
			finishedAtStr, dur, reason, o.id,
		)
		if err != nil {
			return n, fmt.Errorf("reconcile run %s: %w", o.id, err)
		}
		affected, _ := res.RowsAffected()
		n += affected
	}
	return n, nil
}

// CancelRun flips one status='running' row to status='cancelled' with
// `reason` as the error text. Returns store.ErrWorkerRunNotFound when
// the row is missing, store.ErrRunNotCancellable when it's already
// terminal. Idempotent in the second sense — calling cancel on a row
// that's already cancelled / failure / success returns the
// not-cancellable error rather than overwriting the terminal state.
//
// This is the DIRECT-FLIP path used only for runs with no live runner
// registry entry (orphan/stub running rows whose runner died). Live
// runs are hard-stopped by the runner itself, which is the single writer
// of their terminal 'cancelled' state — the admin service decides which
// path applies (see admin.Service.CancelRun). The distinct 'cancelled'
// status (vs the prior 'failure') keeps an operator hard-stop out of the
// consecutive-failure auto-pause streak and out of delegation /
// model-rank failure counts.
func (d *DB) CancelRun(
	ctx context.Context, runID string, now time.Time, reason string,
) error {
	if runID == "" {
		return errors.New("CancelRun: runID required")
	}
	if reason == "" {
		reason = "cancelled by operator"
	}
	var status, startedAtStr string
	err := d.q.QueryRowContext(ctx, `
		SELECT status, started_at FROM worker_runs WHERE id = ?`,
		runID,
	).Scan(&status, &startedAtStr)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrWorkerRunNotFound
	}
	if err != nil {
		return fmt.Errorf("read run for cancel: %w", err)
	}
	if status != "running" {
		return store.ErrRunNotCancellable
	}
	startedAt := parseTime(startedAtStr)
	dur := int64(0)
	if !startedAt.IsZero() {
		dur = now.Sub(startedAt).Milliseconds()
	}
	res, err := d.q.ExecContext(ctx, `
		UPDATE worker_runs
		SET status = 'cancelled', finished_at = ?, duration_ms = ?,
		    error = ?
		WHERE id = ? AND status = 'running'`,
		formatTime(now), dur, reason, runID,
	)
	if err != nil {
		return fmt.Errorf("update worker_run on cancel: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Raced with another finalize between our SELECT and UPDATE.
		// Treat the same as "already terminal" so the caller sees a
		// stable error surface.
		return store.ErrRunNotCancellable
	}
	return nil
}

// PruneWorkerRuns deletes worker_runs older than `beforeCutoff`, but
// preserves the most recent `keepPerWorker` rows per worker_id so a
// low-volume worker doesn't lose its history just because all of its
// runs are older than the age cap. The dual rule:
//
//	DELETE WHERE started_at < cutoff
//	  AND row_number_within_worker > keepPerWorker
//
// row_number is computed via ROW_NUMBER() over ALL of a worker's runs
// ordered started_at DESC (newest = rank 1) — newer-than-cutoff rows
// count toward the floor so a chatty worker keeps a healthy tail of
// recent history rather than the floor being a per-cohort surprise.
// Pass keepPerWorker <= 0 to disable the floor (cutoff alone — useful
// for hard purges).
func (d *DB) PruneWorkerRuns(
	ctx context.Context, keepPerWorker int, beforeCutoff time.Time,
) (int64, error) {
	if keepPerWorker < 0 {
		keepPerWorker = 0
	}
	res, err := d.q.ExecContext(ctx, `
		DELETE FROM worker_runs
		WHERE id IN (
		    SELECT id FROM (
		        SELECT id, started_at,
		            ROW_NUMBER() OVER (
		                PARTITION BY worker_id
		                ORDER BY started_at DESC
		            ) AS rn
		        FROM worker_runs
		    ) ranked
		    WHERE ranked.rn > ? AND ranked.started_at < ?
		)`,
		keepPerWorker, formatTime(beforeCutoff),
	)
	if err != nil {
		return 0, fmt.Errorf("prune worker_runs: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune worker_runs rows affected: %w", err)
	}
	return n, nil
}

func (d *DB) ListOrphanedDelegationRuns(ctx context.Context) ([]*store.WorkerRun, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+workerRunCols+`
		FROM worker_runs
		WHERE status = 'running'
		  AND worker_id IN (SELECT id FROM workers WHERE name LIKE 'delegate-%')
		ORDER BY started_at ASC`)
	if err != nil {
		return nil, fmt.Errorf("list orphaned delegation runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*store.WorkerRun
	for rows.Next() {
		r, err := scanWorkerRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan orphaned delegation run: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanWorkerRun(r scanner) (*store.WorkerRun, error) {
	var (
		run        store.WorkerRun
		startedAt  string
		finishedAt sql.NullString
	)
	err := r.Scan(
		&run.ID, &run.WorkerID, &run.WorkspaceID, &startedAt, &finishedAt, &run.DurationMS,
		&run.Status, &run.PromptRendered, &run.ModelProvider, &run.ModelID,
		&run.InputTokens, &run.OutputTokens, &run.CostUSD,
		&run.BillingModel, &run.SubscriptionBucket, &run.RealCostUSD,
		&run.ToolCallsCount,
		&run.OutputText, &run.Error,
		&run.MeshMessageIDsJSON, &run.AuditRecordIDsJSON,
		&run.ResultBranch, &run.ResultCommit, &run.ResultChanged,
		&run.TriggerKind, &run.TriggerMessageID, &run.TriggerSourcePeer, &run.TriggerChainDepth,
	)
	if err != nil {
		return nil, err
	}
	if run.TriggerKind == "" {
		run.TriggerKind = "schedule"
	}
	run.StartedAt = parseTime(startedAt)
	if finishedAt.Valid {
		t := parseTime(finishedAt.String)
		run.FinishedAt = &t
	}
	return &run, nil
}
