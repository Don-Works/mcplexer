package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/google/uuid"
)

// listWorkerRunCap bounds ListWorkerRuns when the caller passes limit<=0
// so a runaway ledger can't return half a million rows to the admin UI.
const listWorkerRunCap = 500

const workerCols = `id, name, description, model_provider, model_id,
    model_endpoint_url, secret_scope_id, skill_name, skill_version,
    skill_refs_json,
    prompt_template, parameters_json, schedule_spec, tool_allowlist_json,
    capability_profile_json,
    output_channels_json, exec_mode, concurrency_policy, memory_scope_id,
    max_input_tokens, max_output_tokens, max_tool_calls,
    max_wall_clock_seconds, max_monthly_cost_usd, max_consecutive_failures,
    auto_paused_reason,
    source_template_name, source_template_version,
    enabled, workspace_id, created_at, updated_at,
    archived_at, archived_reason,
    pre_execute_script, post_execute_script`

const workerWorkspaceAccessCols = `worker_id, workspace_id, access, created_at, updated_at`

// CreateWorker inserts one row. Caller may pre-set ID (ULID/UUID); we
// generate a UUID when ID is empty. CreatedAt / UpdatedAt are stamped
// when zero. Defaults: ExecMode="propose", ConcurrencyPolicy="skip".
func (d *DB) CreateWorker(ctx context.Context, w *store.Worker) error {
	if w == nil {
		return errors.New("CreateWorker: worker required")
	}
	if w.ID == "" {
		w.ID = uuid.NewString()
	}
	now := time.Now().UTC()
	if w.CreatedAt.IsZero() {
		w.CreatedAt = now
	}
	if w.UpdatedAt.IsZero() {
		w.UpdatedAt = now
	}
	applyWorkerDefaults(w)
	skillRefsJSON, err := marshalSkillRefs(w.SkillRefs)
	if err != nil {
		return fmt.Errorf("marshal skill_refs: %w", err)
	}
	w.WorkspaceAccess = normalizeWorkerWorkspaceAccess(w.ID, w.WorkspaceID, w.WorkspaceAccess)
	err = d.withTx(ctx, func(q queryable) error {
		if _, err := q.ExecContext(ctx, `
			INSERT INTO workers (`+workerCols+`)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
			        ?, ?, ?, ?, ?, ?, ?,
			        ?, ?,
			        ?, ?, ?, ?,
			        ?, ?,
			        ?, ?)`,
			w.ID, w.Name, w.Description, w.ModelProvider, w.ModelID,
			w.ModelEndpointURL, w.SecretScopeID, w.SkillName, w.SkillVersion,
			skillRefsJSON,
			w.PromptTemplate, w.ParametersJSON, w.ScheduleSpec,
			w.ToolAllowlistJSON, w.CapabilityProfileJSON, w.OutputChannelsJSON,
			w.ExecMode, w.ConcurrencyPolicy, nullString(w.MemoryScopeID),
			w.MaxInputTokens, w.MaxOutputTokens, w.MaxToolCalls,
			w.MaxWallClockSeconds, w.MaxMonthlyCostUSD, w.MaxConsecutiveFailures,
			w.AutoPausedReason,
			w.SourceTemplateName, w.SourceTemplateVersion,
			boolToInt(w.Enabled), w.WorkspaceID,
			formatTime(w.CreatedAt), formatTime(w.UpdatedAt),
			formatTimePtr(w.ArchivedAt), w.ArchivedReason,
			w.PreExecuteScript, w.PostExecuteScript,
		); err != nil {
			return mapConstraintError(err)
		}
		return replaceWorkerWorkspaceAccess(ctx, q, w.ID, w.WorkspaceID, w.WorkspaceAccess)
	})
	return err
}

// marshalSkillRefs renders the canonical skill refs JSON. Nil / empty
// slice serialises to "[]" so the schema's NOT NULL default holds.
func marshalSkillRefs(refs []store.SkillRef) (string, error) {
	if len(refs) == 0 {
		return "[]", nil
	}
	b, err := json.Marshal(refs)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// unmarshalSkillRefs parses the canonical column into a SkillRef slice.
// Empty / "null" / parse failure all collapse to nil — the legacy
// (skill_name, skill_version) fallback in EffectiveSkillRefs takes over
// so a corrupted row never silently drops the worker's skill.
func unmarshalSkillRefs(s string) []store.SkillRef {
	if s == "" || s == "null" || s == "[]" {
		return nil
	}
	var refs []store.SkillRef
	if err := json.Unmarshal([]byte(s), &refs); err != nil {
		return nil
	}
	if len(refs) == 0 {
		return nil
	}
	return refs
}

// applyWorkerDefaults backfills the JSON columns + enum defaults so a
// minimally-populated Worker round-trips correctly. Mutates in place.
func applyWorkerDefaults(w *store.Worker) {
	if w.ParametersJSON == "" {
		w.ParametersJSON = "{}"
	}
	if w.ToolAllowlistJSON == "" {
		w.ToolAllowlistJSON = "[]"
	}
	if w.OutputChannelsJSON == "" {
		w.OutputChannelsJSON = "[]"
	}
	if w.ExecMode == "" {
		w.ExecMode = "propose"
	}
	if w.ConcurrencyPolicy == "" {
		w.ConcurrencyPolicy = "skip"
	}
}

// GetWorker returns one row or store.ErrWorkerNotFound.
func (d *DB) GetWorker(ctx context.Context, id string) (*store.Worker, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+workerCols+` FROM workers WHERE id = ?`, id)
	w, err := scanWorker(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrWorkerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get worker: %w", err)
	}
	if err := d.hydrateWorkerWorkspaceAccess(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

// GetWorkerByName returns the row for (workspaceID, name) or
// ErrWorkerNotFound. The unique index makes this an O(1) lookup.
func (d *DB) GetWorkerByName(
	ctx context.Context, workspaceID, name string,
) (*store.Worker, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+workerCols+` FROM workers
		 WHERE workspace_id = ? AND name = ? AND archived_at IS NULL`,
		workspaceID, name)
	w, err := scanWorker(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrWorkerNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get worker by name: %w", err)
	}
	if err := d.hydrateWorkerWorkspaceAccess(ctx, w); err != nil {
		return nil, err
	}
	return w, nil
}

// ListWorkers returns workers in workspaceID. When enabledOnly=true only
// rows with enabled=1 are returned. Ordered created_at DESC.
func (d *DB) ListWorkers(
	ctx context.Context, workspaceID string, enabledOnly bool,
) ([]*store.Worker, error) {
	return d.listWorkers(ctx, workspaceID, enabledOnly, false)
}

// ListWorkersIncludingArchived returns workers in workspaceID including
// archived rows. This is an admin/reporting escape hatch; normal callers
// should use ListWorkers so archived rows do not clutter operator surfaces
// or scheduling bootstraps.
func (d *DB) ListWorkersIncludingArchived(
	ctx context.Context, workspaceID string, enabledOnly bool,
) ([]*store.Worker, error) {
	return d.listWorkers(ctx, workspaceID, enabledOnly, true)
}

func (d *DB) listWorkers(
	ctx context.Context, workspaceID string, enabledOnly bool, includeArchived bool,
) ([]*store.Worker, error) {
	query := `SELECT ` + workerCols + ` FROM workers WHERE workspace_id = ?`
	args := []any{workspaceID}
	if !includeArchived {
		query += ` AND archived_at IS NULL`
	}
	if enabledOnly {
		query += ` AND enabled = 1`
	}
	query += ` ORDER BY created_at DESC`
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list workers: %w", err)
	}
	defer rows.Close()
	var out []*store.Worker
	for rows.Next() {
		w, err := scanWorker(rows)
		if err != nil {
			return nil, fmt.Errorf("scan worker: %w", err)
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, w := range out {
		if err := d.hydrateWorkerWorkspaceAccess(ctx, w); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UpdateWorker writes every mutable column for w and bumps updated_at.
// Returns ErrWorkerNotFound if the row is missing.
func (d *DB) UpdateWorker(ctx context.Context, w *store.Worker) error {
	if w == nil || w.ID == "" {
		return errors.New("UpdateWorker: id required")
	}
	w.UpdatedAt = time.Now().UTC()
	applyWorkerDefaults(w)
	skillRefsJSON, err := marshalSkillRefs(w.SkillRefs)
	if err != nil {
		return fmt.Errorf("marshal skill_refs: %w", err)
	}
	w.WorkspaceAccess = normalizeWorkerWorkspaceAccess(w.ID, w.WorkspaceID, w.WorkspaceAccess)
	err = d.withTx(ctx, func(q queryable) error {
		res, err := q.ExecContext(ctx, `
			UPDATE workers
			SET name = ?, description = ?, model_provider = ?, model_id = ?,
			    model_endpoint_url = ?, secret_scope_id = ?,
			    skill_name = ?, skill_version = ?, skill_refs_json = ?,
			    prompt_template = ?, parameters_json = ?, schedule_spec = ?,
			    tool_allowlist_json = ?, capability_profile_json = ?,
			    output_channels_json = ?,
			    exec_mode = ?, concurrency_policy = ?, memory_scope_id = ?,
			    max_input_tokens = ?, max_output_tokens = ?, max_tool_calls = ?,
			    max_wall_clock_seconds = ?, max_monthly_cost_usd = ?,
			    max_consecutive_failures = ?, auto_paused_reason = ?,
			    source_template_name = ?, source_template_version = ?,
			    enabled = ?, workspace_id = ?, updated_at = ?,
			    archived_at = ?, archived_reason = ?,
			    pre_execute_script = ?, post_execute_script = ?
			WHERE id = ?`,
			w.Name, w.Description, w.ModelProvider, w.ModelID,
			w.ModelEndpointURL, w.SecretScopeID,
			w.SkillName, w.SkillVersion, skillRefsJSON,
			w.PromptTemplate, w.ParametersJSON, w.ScheduleSpec,
			w.ToolAllowlistJSON, w.CapabilityProfileJSON, w.OutputChannelsJSON,
			w.ExecMode, w.ConcurrencyPolicy, nullString(w.MemoryScopeID),
			w.MaxInputTokens, w.MaxOutputTokens, w.MaxToolCalls,
			w.MaxWallClockSeconds, w.MaxMonthlyCostUSD,
			w.MaxConsecutiveFailures, w.AutoPausedReason,
			w.SourceTemplateName, w.SourceTemplateVersion,
			boolToInt(w.Enabled), w.WorkspaceID,
			formatTime(w.UpdatedAt), formatTimePtr(w.ArchivedAt), w.ArchivedReason,
			w.PreExecuteScript, w.PostExecuteScript, w.ID,
		)
		if err != nil {
			return mapConstraintError(err)
		}
		if err := mapNotFound(res, store.ErrWorkerNotFound); err != nil {
			return err
		}
		return replaceWorkerWorkspaceAccess(ctx, q, w.ID, w.WorkspaceID, w.WorkspaceAccess)
	})
	return err
}

// ListWorkerWorkspaceAccess returns the explicit workspace grants for a
// worker. Legacy rows without grant records fall back to the worker's
// preferred workspace with write access.
func (d *DB) ListWorkerWorkspaceAccess(ctx context.Context, workerID string) ([]store.WorkerWorkspaceAccess, error) {
	grants, err := listWorkerWorkspaceAccess(ctx, d.q, workerID)
	if err != nil {
		return nil, err
	}
	if len(grants) > 0 {
		return grants, nil
	}
	workspaceID, err := d.workerHomeWorkspaceID(ctx, workerID)
	if err != nil {
		return nil, err
	}
	return normalizeWorkerWorkspaceAccess(workerID, workspaceID, nil), nil
}

func (d *DB) workerHomeWorkspaceID(ctx context.Context, workerID string) (string, error) {
	var workspaceID string
	err := d.q.QueryRowContext(ctx, `SELECT workspace_id FROM workers WHERE id = ?`, workerID).Scan(&workspaceID)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", store.ErrWorkerNotFound
		}
		return "", fmt.Errorf("get worker workspace id: %w", err)
	}
	return workspaceID, nil
}

// ReplaceWorkerWorkspaceAccess atomically replaces every workspace grant
// for a worker. The preferred workspace is always retained with write
// access so legacy Worker.WorkspaceID semantics stay valid.
func (d *DB) ReplaceWorkerWorkspaceAccess(ctx context.Context, workerID string, grants []store.WorkerWorkspaceAccess) error {
	if strings.TrimSpace(workerID) == "" {
		return errors.New("ReplaceWorkerWorkspaceAccess: worker_id required")
	}
	w, err := d.GetWorker(ctx, workerID)
	if err != nil {
		return err
	}
	return d.withTx(ctx, func(q queryable) error {
		return replaceWorkerWorkspaceAccess(ctx, q, workerID, w.WorkspaceID, grants)
	})
}

// DeleteWorker hard-deletes the row. Returns ErrWorkerNotFound if absent.
// Worker_runs rows are intentionally NOT cascaded — the audit ledger
// keeps the history of executions, just orphaned.
func (d *DB) DeleteWorker(ctx context.Context, id string) error {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM workers WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete worker: %w", err)
	}
	return mapNotFound(res, store.ErrWorkerNotFound)
}

// mapNotFound returns sentinel when the row was missing instead of the
// generic ErrNotFound. Keeps WorkerStore's contract distinct from the
// global ErrNotFound used by other stores.
func mapNotFound(res sql.Result, sentinel error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return sentinel
	}
	return nil
}

func scanWorker(r scanner) (*store.Worker, error) {
	var (
		w                    store.Worker
		enabled              int
		memoryScopeID        sql.NullString
		archivedAt           sql.NullString
		skillRefsJSON        string
		createdAt, updatedAt string
	)
	err := r.Scan(
		&w.ID, &w.Name, &w.Description, &w.ModelProvider, &w.ModelID,
		&w.ModelEndpointURL, &w.SecretScopeID, &w.SkillName, &w.SkillVersion,
		&skillRefsJSON,
		&w.PromptTemplate, &w.ParametersJSON, &w.ScheduleSpec,
		&w.ToolAllowlistJSON, &w.CapabilityProfileJSON, &w.OutputChannelsJSON,
		&w.ExecMode, &w.ConcurrencyPolicy, &memoryScopeID,
		&w.MaxInputTokens, &w.MaxOutputTokens, &w.MaxToolCalls,
		&w.MaxWallClockSeconds, &w.MaxMonthlyCostUSD,
		&w.MaxConsecutiveFailures, &w.AutoPausedReason,
		&w.SourceTemplateName, &w.SourceTemplateVersion,
		&enabled, &w.WorkspaceID, &createdAt, &updatedAt,
		&archivedAt, &w.ArchivedReason,
		&w.PreExecuteScript, &w.PostExecuteScript,
	)
	if err != nil {
		return nil, err
	}
	w.Enabled = enabled != 0
	if memoryScopeID.Valid {
		w.MemoryScopeID = memoryScopeID.String
	}
	w.SkillRefs = unmarshalSkillRefs(skillRefsJSON)
	w.CreatedAt = parseTime(createdAt)
	w.UpdatedAt = parseTime(updatedAt)
	if archivedAt.Valid && strings.TrimSpace(archivedAt.String) != "" {
		t := parseTime(archivedAt.String)
		if !t.IsZero() {
			w.ArchivedAt = &t
		}
	}
	return &w, nil
}

func (d *DB) hydrateWorkerWorkspaceAccess(ctx context.Context, w *store.Worker) error {
	if w == nil {
		return nil
	}
	grants, err := listWorkerWorkspaceAccess(ctx, d.q, w.ID)
	if err != nil {
		return err
	}
	w.WorkspaceAccess = normalizeWorkerWorkspaceAccess(w.ID, w.WorkspaceID, grants)
	return nil
}

func listWorkerWorkspaceAccess(
	ctx context.Context, q queryable, workerID string,
) ([]store.WorkerWorkspaceAccess, error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return nil, errors.New("ListWorkerWorkspaceAccess: worker_id required")
	}
	rows, err := q.QueryContext(ctx,
		`SELECT `+workerWorkspaceAccessCols+`
		   FROM worker_workspace_access
		  WHERE worker_id = ?
		  ORDER BY workspace_id ASC`,
		workerID,
	)
	if err != nil {
		return nil, fmt.Errorf("list worker workspace access: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.WorkerWorkspaceAccess
	for rows.Next() {
		var g store.WorkerWorkspaceAccess
		var createdAt, updatedAt string
		if err := rows.Scan(
			&g.WorkerID, &g.WorkspaceID, &g.Access, &createdAt, &updatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan worker workspace access: %w", err)
		}
		g.CreatedAt = parseTime(createdAt)
		g.UpdatedAt = parseTime(updatedAt)
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate worker workspace access: %w", err)
	}
	return out, nil
}

func replaceWorkerWorkspaceAccess(
	ctx context.Context,
	q queryable,
	workerID string,
	homeWorkspaceID string,
	grants []store.WorkerWorkspaceAccess,
) error {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return errors.New("replaceWorkerWorkspaceAccess: worker_id required")
	}
	grants = normalizeWorkerWorkspaceAccess(workerID, homeWorkspaceID, grants)
	if _, err := q.ExecContext(ctx,
		`DELETE FROM worker_workspace_access WHERE worker_id = ?`, workerID,
	); err != nil {
		return fmt.Errorf("delete worker workspace access: %w", err)
	}
	now := time.Now().UTC()
	for _, g := range grants {
		if strings.TrimSpace(g.WorkspaceID) == "" {
			return errors.New("worker_workspace_access.workspace_id required")
		}
		switch g.Access {
		case store.WorkerWorkspaceAccessRead, store.WorkerWorkspaceAccessWrite:
		default:
			return fmt.Errorf("worker_workspace_access.access must be read or write (got %q)", g.Access)
		}
		if g.CreatedAt.IsZero() {
			g.CreatedAt = now
		}
		g.UpdatedAt = now
		if _, err := q.ExecContext(ctx, `
			INSERT INTO worker_workspace_access (`+workerWorkspaceAccessCols+`)
			VALUES (?, ?, ?, ?, ?)`,
			workerID, g.WorkspaceID, g.Access,
			formatTime(g.CreatedAt), formatTime(g.UpdatedAt),
		); err != nil {
			return mapConstraintError(err)
		}
	}
	return nil
}

func normalizeWorkerWorkspaceAccess(
	workerID string,
	homeWorkspaceID string,
	grants []store.WorkerWorkspaceAccess,
) []store.WorkerWorkspaceAccess {
	workerID = strings.TrimSpace(workerID)
	homeWorkspaceID = strings.TrimSpace(homeWorkspaceID)
	byWorkspace := make(map[string]store.WorkerWorkspaceAccess, len(grants)+1)
	for _, g := range grants {
		g.WorkerID = workerID
		g.WorkspaceID = strings.TrimSpace(g.WorkspaceID)
		g.Access = strings.TrimSpace(g.Access)
		if g.WorkspaceID == "" {
			continue
		}
		byWorkspace[g.WorkspaceID] = g
	}
	if homeWorkspaceID != "" {
		g := byWorkspace[homeWorkspaceID]
		g.WorkerID = workerID
		g.WorkspaceID = homeWorkspaceID
		g.Access = store.WorkerWorkspaceAccessWrite
		byWorkspace[homeWorkspaceID] = g
	}
	out := make([]store.WorkerWorkspaceAccess, 0, len(byWorkspace))
	for _, g := range byWorkspace {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].WorkspaceID < out[j].WorkspaceID
	})
	return out
}
