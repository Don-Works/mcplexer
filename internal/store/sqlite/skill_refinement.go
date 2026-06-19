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

// RecordRefinementProposal inserts a new skill_refinement_proposals
// row. Defaults: ID=ULID, CreatedAt=now(UTC), Status="pending",
// MetadataJSON="{}". See store.SkillRefinementStore for the contract.
func (d *DB) RecordRefinementProposal(
	ctx context.Context, p *store.SkillRefinementProposal,
) error {
	if p == nil {
		return errors.New("RecordRefinementProposal: nil proposal")
	}
	if strings.TrimSpace(p.SkillName) == "" {
		return errors.New("RecordRefinementProposal: skill_name required")
	}
	if strings.TrimSpace(p.Friction) == "" {
		return errors.New("RecordRefinementProposal: friction required")
	}
	if strings.TrimSpace(p.SuggestedChange) == "" {
		return errors.New("RecordRefinementProposal: suggested_change required")
	}
	if strings.TrimSpace(p.Rationale) == "" {
		return errors.New("RecordRefinementProposal: rationale required")
	}
	if strings.TrimSpace(p.ProposedBySessionID) == "" {
		return errors.New("RecordRefinementProposal: proposed_by_session_id required")
	}
	if strings.TrimSpace(p.WorkspaceID) == "" {
		return errors.New("RecordRefinementProposal: workspace_id required")
	}
	if p.ID == "" {
		p.ID = ulid.Make().String()
	}
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now().UTC()
	}
	if p.Status == "" {
		p.Status = store.RefinementStatusPending
	}
	if len(p.MetadataJSON) == 0 {
		p.MetadataJSON = json.RawMessage(`{}`)
	}

	var (
		candidateAt sql.NullString
		resolvedAt  sql.NullString
	)
	if p.CandidateAt != nil && !p.CandidateAt.IsZero() {
		candidateAt = sql.NullString{
			String: p.CandidateAt.UTC().Format(time.RFC3339Nano), Valid: true,
		}
	}
	if p.ResolvedAt != nil && !p.ResolvedAt.IsZero() {
		resolvedAt = sql.NullString{
			String: p.ResolvedAt.UTC().Format(time.RFC3339Nano), Valid: true,
		}
	}

	_, err := d.q.ExecContext(ctx, `
		INSERT INTO skill_refinement_proposals (
			id, skill_name, skill_version, friction, suggested_change, rationale,
			proposed_by_session_id, proposed_by_peer_id, workspace_id,
			created_at, status, candidate_at, resolved_at,
			resolved_by_session_id, resolution_note, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.SkillName, p.SkillVersion, p.Friction, p.SuggestedChange, p.Rationale,
		p.ProposedBySessionID, nullStr(p.ProposedByPeerID), p.WorkspaceID,
		p.CreatedAt.UTC().Format(time.RFC3339Nano), p.Status, candidateAt, resolvedAt,
		nullStr(p.ResolvedBySessionID), nullStr(p.ResolutionNote), string(p.MetadataJSON),
	)
	if err != nil {
		if isUniqueConstraint(err) {
			return store.ErrAlreadyExists
		}
		return fmt.Errorf("insert skill_refinement_proposals: %w", err)
	}
	return nil
}

// UpdateRefinementProposal applies a partial patch. When Status flips
// to a terminal value (promoted/rejected) AND ResolvedAt is nil, the
// store stamps now(UTC) so a single "resolve this proposal" call
// closes the row in one round-trip.
func (d *DB) UpdateRefinementProposal(
	ctx context.Context, id string, patch store.RefinementProposalPatch,
) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("UpdateRefinementProposal: id required")
	}
	if patch.Status != nil && isTerminalRefinementStatus(*patch.Status) && patch.ResolvedAt == nil {
		now := time.Now().UTC()
		patch.ResolvedAt = &now
	}

	sets, args := buildRefinementUpdateSets(patch)
	if len(sets) == 0 {
		// No-op patch — still verify the row exists so callers get
		// ErrNotFound when they refer to a deleted/missing id.
		_, err := d.GetRefinementProposal(ctx, id)
		return err
	}
	args = append(args, id)
	res, err := d.q.ExecContext(ctx,
		`UPDATE skill_refinement_proposals SET `+strings.Join(sets, ", ")+` WHERE id = ?`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("update skill_refinement_proposals: %w", err)
	}
	return checkRowsAffected(res)
}

// buildRefinementUpdateSets translates the patch into SQL SET clauses
// + args. Extracted to keep UpdateRefinementProposal under the 50-line
// cap.
func buildRefinementUpdateSets(patch store.RefinementProposalPatch) ([]string, []any) {
	var (
		sets []string
		args []any
	)
	if patch.Status != nil {
		sets = append(sets, "status = ?")
		args = append(args, *patch.Status)
	}
	if patch.CandidateAt != nil {
		sets = append(sets, "candidate_at = ?")
		args = append(args, patch.CandidateAt.UTC().Format(time.RFC3339Nano))
	}
	if patch.ResolvedAt != nil {
		sets = append(sets, "resolved_at = ?")
		args = append(args, patch.ResolvedAt.UTC().Format(time.RFC3339Nano))
	}
	if patch.ResolvedBySessionID != nil {
		sets = append(sets, "resolved_by_session_id = ?")
		args = append(args, nullStr(*patch.ResolvedBySessionID))
	}
	if patch.ResolutionNote != nil {
		sets = append(sets, "resolution_note = ?")
		args = append(args, nullStr(*patch.ResolutionNote))
	}
	if patch.MetadataJSON != nil {
		sets = append(sets, "metadata_json = ?")
		args = append(args, string(patch.MetadataJSON))
	}
	return sets, args
}

// GetRefinementProposal returns one row by id. ErrNotFound when missing.
func (d *DB) GetRefinementProposal(
	ctx context.Context, id string,
) (*store.SkillRefinementProposal, error) {
	row := d.q.QueryRowContext(ctx, refinementSelectCols+` WHERE id = ?`, id)
	p, err := scanRefinementProposal(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get skill_refinement_proposal: %w", err)
	}
	return p, nil
}

// ListRefinementProposals returns rows matching the filter, ordered by
// created_at DESC. Default limit 100, hard cap 500.
func (d *DB) ListRefinementProposals(
	ctx context.Context, f store.RefinementFilter,
) ([]store.SkillRefinementProposal, error) {
	var (
		conds []string
		args  []any
	)
	if f.SkillName != "" {
		conds = append(conds, "skill_name = ?")
		args = append(args, f.SkillName)
	}
	if f.Status != "" {
		conds = append(conds, "status = ?")
		args = append(args, f.Status)
	}
	if f.WorkspaceID != "" {
		conds = append(conds, "workspace_id = ?")
		args = append(args, f.WorkspaceID)
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}

	q := refinementSelectCols
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)

	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("query skill_refinement_proposals: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var out []store.SkillRefinementProposal
	for rows.Next() {
		p, err := scanRefinementProposal(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan skill_refinement_proposal: %w", err)
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate skill_refinement_proposals: %w", err)
	}
	return out, nil
}

// CountSimilarProposals counts rows with the same skill_name whose
// `friction` column contains `frictionSubstring` (case-sensitive
// substring — the quorum aggregator does its own lowercase+trim
// normalisation before calling). Backs the mesh-quorum gate.
func (d *DB) CountSimilarProposals(
	ctx context.Context, skillName, frictionSubstring string,
) (int, error) {
	if strings.TrimSpace(skillName) == "" {
		return 0, errors.New("CountSimilarProposals: skill_name required")
	}
	if strings.TrimSpace(frictionSubstring) == "" {
		return 0, errors.New("CountSimilarProposals: friction_substring required")
	}
	var count int
	// Use lower(friction) LIKE lower(?) so the comparison stays
	// case-insensitive in the SQL — the aggregator's normalisation is
	// best-effort but the DB matches what the human sees.
	pattern := "%" + frictionSubstring + "%"
	err := d.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM skill_refinement_proposals
		WHERE skill_name = ? AND lower(friction) LIKE lower(?)`,
		skillName, pattern,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count similar proposals: %w", err)
	}
	return count, nil
}

const refinementSelectCols = `
	SELECT id, skill_name, skill_version, friction, suggested_change, rationale,
		proposed_by_session_id, proposed_by_peer_id, workspace_id,
		created_at, status, candidate_at, resolved_at,
		resolved_by_session_id, resolution_note, metadata_json
	FROM skill_refinement_proposals`

// scanRefinementProposal reads one row's columns. Shared between
// QueryRow + Query loops.
func scanRefinementProposal(scan func(...any) error) (*store.SkillRefinementProposal, error) {
	var (
		p              store.SkillRefinementProposal
		proposedByPeer sql.NullString
		createdAt      string
		candidateAt    sql.NullString
		resolvedAt     sql.NullString
		resolvedBySess sql.NullString
		resolutionNote sql.NullString
		metadataJSON   string
	)
	if err := scan(
		&p.ID, &p.SkillName, &p.SkillVersion, &p.Friction, &p.SuggestedChange, &p.Rationale,
		&p.ProposedBySessionID, &proposedByPeer, &p.WorkspaceID,
		&createdAt, &p.Status, &candidateAt, &resolvedAt,
		&resolvedBySess, &resolutionNote, &metadataJSON,
	); err != nil {
		return nil, err
	}
	t, err := parseRefinementTime(createdAt)
	if err != nil {
		return nil, fmt.Errorf("parse created_at %q: %w", createdAt, err)
	}
	p.CreatedAt = t
	if candidateAt.Valid && candidateAt.String != "" {
		c, err := parseRefinementTime(candidateAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse candidate_at %q: %w", candidateAt.String, err)
		}
		p.CandidateAt = &c
	}
	if resolvedAt.Valid && resolvedAt.String != "" {
		r, err := parseRefinementTime(resolvedAt.String)
		if err != nil {
			return nil, fmt.Errorf("parse resolved_at %q: %w", resolvedAt.String, err)
		}
		p.ResolvedAt = &r
	}
	if proposedByPeer.Valid {
		p.ProposedByPeerID = proposedByPeer.String
	}
	if resolvedBySess.Valid {
		p.ResolvedBySessionID = resolvedBySess.String
	}
	if resolutionNote.Valid {
		p.ResolutionNote = resolutionNote.String
	}
	p.MetadataJSON = json.RawMessage(metadataJSON)
	return &p, nil
}

// parseRefinementTime tolerates both RFC3339Nano (what we write) and
// RFC3339 (legacy / hand-edited rows). Returns UTC.
func parseRefinementTime(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t, err = time.Parse(time.RFC3339, s)
		if err != nil {
			return time.Time{}, err
		}
	}
	return t.UTC(), nil
}

func isTerminalRefinementStatus(s string) bool {
	switch s {
	case store.RefinementStatusPromoted, store.RefinementStatusRejected, store.RefinementStatusApplied:
		return true
	}
	return false
}

// isUniqueConstraint matches modernc.org/sqlite's UNIQUE violation
// surface. Mirrors the existing helpers in this package; pulled into
// its own predicate so each call site stays readable.
func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "constraint failed: unique")
}
