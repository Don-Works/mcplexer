package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/don-works/mcplexer/internal/store"
)

// RecordSkillRun inserts a new skill_runs row. Defaults: ID=ULID,
// StartedAt=now(UTC), Outcome="running", PhasesJSON/ToolsUsedJSON="[]",
// MetadataJSON="{}".
func (d *DB) RecordSkillRun(ctx context.Context, r *store.SkillRun) error {
	if r == nil {
		return errors.New("RecordSkillRun: nil run")
	}
	if strings.TrimSpace(r.SkillName) == "" {
		return errors.New("RecordSkillRun: skill_name required")
	}
	if strings.TrimSpace(r.WorkspaceID) == "" {
		return errors.New("RecordSkillRun: workspace_id required")
	}
	if r.ID == "" {
		r.ID = ulid.Make().String()
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	if r.Outcome == "" {
		r.Outcome = store.SkillRunOutcomeRunning
	}
	if len(r.PhasesJSON) == 0 {
		r.PhasesJSON = json.RawMessage(`[]`)
	}
	if len(r.ToolsUsedJSON) == 0 {
		r.ToolsUsedJSON = json.RawMessage(`[]`)
	}
	if len(r.MetadataJSON) == 0 {
		r.MetadataJSON = json.RawMessage(`{}`)
	}
	var completedAt sql.NullString
	if r.CompletedAt != nil && !r.CompletedAt.IsZero() {
		completedAt = sql.NullString{String: r.CompletedAt.UTC().Format(time.RFC3339Nano), Valid: true}
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO skill_runs (
			id, skill_name, skill_version, workspace_id,
			started_at, completed_at, outcome,
			phases_json, tools_used_json,
			task_epic_id, agent_session_id, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.SkillName, r.SkillVersion, r.WorkspaceID,
		r.StartedAt.UTC().Format(time.RFC3339Nano), completedAt, r.Outcome,
		string(r.PhasesJSON), string(r.ToolsUsedJSON),
		nullStr(r.TaskEpicID), nullStr(r.AgentSessionID), string(r.MetadataJSON),
	)
	if err != nil {
		return fmt.Errorf("insert skill_runs: %w", err)
	}
	return nil
}

// UpdateSkillRun applies a partial patch. When patch.Outcome flips to
// a terminal value AND CompletedAt is nil, the store stamps now(UTC)
// so a single "finish this run" call closes the row in one round-trip.
func (d *DB) UpdateSkillRun(ctx context.Context, id string, patch store.SkillRunPatch) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("UpdateSkillRun: id required")
	}
	// Auto-stamp CompletedAt when transitioning to terminal outcome
	// without an explicit timestamp. Keeps the caller's payload small.
	if patch.Outcome != nil && isTerminalSkillRunOutcome(*patch.Outcome) && patch.CompletedAt == nil {
		now := time.Now().UTC()
		patch.CompletedAt = &now
	}

	var (
		sets []string
		args []any
	)
	if patch.Outcome != nil {
		sets = append(sets, "outcome = ?")
		args = append(args, *patch.Outcome)
	}
	if patch.CompletedAt != nil {
		sets = append(sets, "completed_at = ?")
		args = append(args, patch.CompletedAt.UTC().Format(time.RFC3339Nano))
	}
	if patch.PhasesJSON != nil {
		sets = append(sets, "phases_json = ?")
		args = append(args, string(patch.PhasesJSON))
	}
	if patch.ToolsUsedJSON != nil {
		sets = append(sets, "tools_used_json = ?")
		args = append(args, string(patch.ToolsUsedJSON))
	}
	if patch.TaskEpicID != nil {
		sets = append(sets, "task_epic_id = ?")
		args = append(args, nullStr(*patch.TaskEpicID))
	}
	if patch.MetadataJSON != nil {
		sets = append(sets, "metadata_json = ?")
		args = append(args, string(patch.MetadataJSON))
	}
	if len(sets) == 0 {
		// No-op patch — still verify the row exists so callers get
		// ErrNotFound when they refer to a deleted/missing id.
		_, err := d.GetSkillRun(ctx, id)
		return err
	}
	args = append(args, id)
	res, err := d.q.ExecContext(ctx,
		`UPDATE skill_runs SET `+strings.Join(sets, ", ")+` WHERE id = ?`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("update skill_runs: %w", err)
	}
	return checkRowsAffected(res)
}

// GetSkillRun returns one row by id. ErrNotFound when missing.
func (d *DB) GetSkillRun(ctx context.Context, id string) (*store.SkillRun, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT id, skill_name, skill_version, workspace_id,
			started_at, completed_at, outcome,
			phases_json, tools_used_json,
			task_epic_id, agent_session_id, metadata_json
		FROM skill_runs WHERE id = ?`, id)
	r, err := scanSkillRun(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get skill_run: %w", err)
	}
	return r, nil
}

// ListSkillRuns returns rows matching the filter, ordered by
// started_at DESC. Default limit 50, hard cap 500.
func (d *DB) ListSkillRuns(ctx context.Context, f store.SkillRunFilter) ([]store.SkillRun, error) {
	var (
		conds []string
		args  []any
	)
	if f.SkillName != "" {
		conds = append(conds, "skill_name = ?")
		args = append(args, f.SkillName)
	}
	if f.WorkspaceID != "" {
		conds = append(conds, "workspace_id = ?")
		args = append(args, f.WorkspaceID)
	}
	if f.Outcome != "" {
		conds = append(conds, "outcome = ?")
		args = append(args, f.Outcome)
	}
	if f.Since != nil {
		conds = append(conds, "started_at >= ?")
		args = append(args, f.Since.UTC().Format(time.RFC3339Nano))
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	q := `SELECT id, skill_name, skill_version, workspace_id,
			started_at, completed_at, outcome,
			phases_json, tools_used_json,
			task_epic_id, agent_session_id, metadata_json
		FROM skill_runs`
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY started_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query skill_runs: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.SkillRun
	for rows.Next() {
		r, err := scanSkillRun(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan skill_runs row: %w", err)
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skill_runs: %w", err)
	}
	return out, nil
}

// scanSkillRun reads one row's columns. Shared between QueryRow + Query loops.
func scanSkillRun(scan func(...any) error) (*store.SkillRun, error) {
	var (
		r              store.SkillRun
		startedAt      string
		completedAt    sql.NullString
		phasesJSON     string
		toolsUsedJSON  string
		taskEpicID     sql.NullString
		agentSessionID sql.NullString
		metadataJSON   string
	)
	if err := scan(
		&r.ID, &r.SkillName, &r.SkillVersion, &r.WorkspaceID,
		&startedAt, &completedAt, &r.Outcome,
		&phasesJSON, &toolsUsedJSON,
		&taskEpicID, &agentSessionID, &metadataJSON,
	); err != nil {
		return nil, err
	}
	t, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		// Fall back to RFC3339 (no nanos) for hand-edited rows.
		t, err = time.Parse(time.RFC3339, startedAt)
		if err != nil {
			return nil, fmt.Errorf("parse started_at %q: %w", startedAt, err)
		}
	}
	r.StartedAt = t.UTC()
	if completedAt.Valid && completedAt.String != "" {
		c, err := time.Parse(time.RFC3339Nano, completedAt.String)
		if err != nil {
			c, err = time.Parse(time.RFC3339, completedAt.String)
			if err != nil {
				return nil, fmt.Errorf("parse completed_at %q: %w", completedAt.String, err)
			}
		}
		cUTC := c.UTC()
		r.CompletedAt = &cUTC
	}
	r.PhasesJSON = json.RawMessage(phasesJSON)
	r.ToolsUsedJSON = json.RawMessage(toolsUsedJSON)
	r.MetadataJSON = json.RawMessage(metadataJSON)
	if taskEpicID.Valid {
		r.TaskEpicID = taskEpicID.String
	}
	if agentSessionID.Valid {
		r.AgentSessionID = agentSessionID.String
	}
	return &r, nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func isTerminalSkillRunOutcome(o string) bool {
	switch o {
	case store.SkillRunOutcomeSuccess,
		store.SkillRunOutcomeFailure,
		store.SkillRunOutcomeCancelled:
		return true
	}
	return false
}
