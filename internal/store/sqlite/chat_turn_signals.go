// chat_turn_signals.go — per-turn feedback log for the concierge
// self-improvement loop. Backs ChatTurnSignalStore in internal/store.
// See migration 080 for schema + index design.
package sqlite

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// InsertChatTurnSignal writes one row. ID + CreatedAt are auto-filled
// when blank. Idempotent on (id) so a writer retry doesn't duplicate.
func (d *DB) InsertChatTurnSignal(ctx context.Context, s *store.ChatTurnSignal) error {
	if s == nil {
		return fmt.Errorf("InsertChatTurnSignal: nil signal")
	}
	if strings.TrimSpace(s.WorkerID) == "" {
		return fmt.Errorf("InsertChatTurnSignal: worker_id required")
	}
	if strings.TrimSpace(s.Channel) == "" {
		return fmt.Errorf("InsertChatTurnSignal: channel required")
	}
	if strings.TrimSpace(s.Label) == "" {
		return fmt.Errorf("InsertChatTurnSignal: label required")
	}
	if s.ID == "" {
		s.ID = ulid.Make().String()
	}
	if s.CreatedAt.IsZero() {
		s.CreatedAt = time.Now().UTC()
	}
	if s.ClassifierKind == "" {
		s.ClassifierKind = store.ChatTurnClassifierRule
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO chat_turn_signals(
			id, worker_id, workspace_id, user_id_external, channel,
			prompt_version, turn_id, label, user_message, assistant_message,
			confidence, classifier_kind, source_session_id, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO NOTHING`,
		s.ID, s.WorkerID, s.WorkspaceID, s.UserIDExternal, s.Channel,
		s.PromptVersion, s.TurnID, s.Label, s.UserMessage, s.AssistantMessage,
		s.Confidence, s.ClassifierKind, s.SourceSessionID, s.CreatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert chat_turn_signal: %w", err)
	}
	return nil
}

// ListChatTurnSignals returns rows matching the filter, ordered by
// created_at DESC.
func (d *DB) ListChatTurnSignals(
	ctx context.Context, f store.ChatTurnSignalFilter,
) ([]store.ChatTurnSignal, error) {
	var clauses []string
	var args []any
	if f.WorkerID != "" {
		clauses = append(clauses, "worker_id = ?")
		args = append(args, f.WorkerID)
	}
	if f.WorkspaceID != "" {
		clauses = append(clauses, "workspace_id = ?")
		args = append(args, f.WorkspaceID)
	}
	if f.UserIDExternal != "" {
		clauses = append(clauses, "user_id_external = ?")
		args = append(args, f.UserIDExternal)
	}
	if f.Channel != "" {
		clauses = append(clauses, "channel = ?")
		args = append(args, f.Channel)
	}
	if f.PromptVersion > 0 {
		clauses = append(clauses, "prompt_version = ?")
		args = append(args, f.PromptVersion)
	}
	if len(f.Labels) > 0 {
		placeholders := strings.Repeat("?,", len(f.Labels))
		placeholders = placeholders[:len(placeholders)-1]
		clauses = append(clauses, "label IN ("+placeholders+")")
		for _, l := range f.Labels {
			args = append(args, l)
		}
	}
	if f.NotPromoted {
		clauses = append(clauses, "promoted_to_refinement_id IS NULL")
	}
	where := ""
	if len(clauses) > 0 {
		where = " WHERE " + strings.Join(clauses, " AND ")
	}
	limit := f.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	args = append(args, limit)
	q := `
		SELECT id, worker_id, workspace_id, user_id_external, channel,
		       prompt_version, turn_id, label, user_message, assistant_message,
		       confidence, classifier_kind, source_session_id, created_at,
		       promoted_to_refinement_id
		FROM chat_turn_signals` + where + `
		ORDER BY created_at DESC, id DESC
		LIMIT ?`
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list chat_turn_signals: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.ChatTurnSignal
	for rows.Next() {
		var s store.ChatTurnSignal
		var createdAt int64
		var promoted *string
		if err := rows.Scan(
			&s.ID, &s.WorkerID, &s.WorkspaceID, &s.UserIDExternal, &s.Channel,
			&s.PromptVersion, &s.TurnID, &s.Label, &s.UserMessage, &s.AssistantMessage,
			&s.Confidence, &s.ClassifierKind, &s.SourceSessionID, &createdAt,
			&promoted,
		); err != nil {
			return nil, fmt.Errorf("scan chat_turn_signal: %w", err)
		}
		s.CreatedAt = time.Unix(createdAt, 0).UTC()
		if promoted != nil {
			s.PromotedToRefinementID = *promoted
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// MarkChatTurnSignalPromoted stamps the refinement linkage. Idempotent.
func (d *DB) MarkChatTurnSignalPromoted(
	ctx context.Context, signalID, refinementID string,
) error {
	if strings.TrimSpace(signalID) == "" || strings.TrimSpace(refinementID) == "" {
		return fmt.Errorf("MarkChatTurnSignalPromoted: signalID + refinementID required")
	}
	_, err := d.q.ExecContext(ctx,
		`UPDATE chat_turn_signals
		 SET promoted_to_refinement_id = ?
		 WHERE id = ? AND promoted_to_refinement_id IS NULL`,
		refinementID, signalID)
	if err != nil {
		return fmt.Errorf("mark chat_turn_signal promoted: %w", err)
	}
	return nil
}

// ForgetChatTurnSignalsBySource hard-purges signals from the named session.
func (d *DB) ForgetChatTurnSignalsBySource(
	ctx context.Context, sessionID string,
) (int, error) {
	if strings.TrimSpace(sessionID) == "" {
		return 0, fmt.Errorf("ForgetChatTurnSignalsBySource: sessionID required")
	}
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM chat_turn_signals WHERE source_session_id = ?`,
		sessionID)
	if err != nil {
		return 0, fmt.Errorf("forget chat_turn_signals: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
