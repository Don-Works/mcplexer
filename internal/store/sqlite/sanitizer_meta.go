package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const sanitizerMetaCols = `id, scope, scope_id, denylist_enabled, envelope_enabled,
    classifier_enabled, classifier_model, action_on_match,
    detected_count, redacted_count, blocked_count,
    last_event_at, created_at, updated_at`

// validSanitizerCounters bounds IncrementSanitizerCounter — we never
// interpolate the counter name into SQL without first checking it against
// this allowlist.
var validSanitizerCounters = map[string]string{
	"detected_count": "detected_count",
	"redacted_count": "redacted_count",
	"blocked_count":  "blocked_count",
}

// GetSanitizerMeta returns the row for (scope, scopeID) or ErrNotFound.
func (d *DB) GetSanitizerMeta(
	ctx context.Context, scope, scopeID string,
) (*store.SanitizerMeta, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+sanitizerMetaCols+` FROM sanitizer_meta
		 WHERE scope = ? AND scope_id = ?`, scope, scopeID)
	m, err := scanSanitizerMeta(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get sanitizer_meta: %w", err)
	}
	return m, nil
}

// UpsertSanitizerMeta inserts a new row for (scope, scope_id) or updates
// the policy columns on conflict. Counters are preserved on update — use
// IncrementSanitizerCounter to bump them.
func (d *DB) UpsertSanitizerMeta(ctx context.Context, m *store.SanitizerMeta) error {
	if m == nil || m.ID == "" || m.Scope == "" {
		return errors.New("UpsertSanitizerMeta: id + scope required")
	}
	now := time.Now().UTC()
	if m.CreatedAt.IsZero() {
		m.CreatedAt = now
	}
	m.UpdatedAt = now
	if m.ActionOnMatch == "" {
		m.ActionOnMatch = "envelope"
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO sanitizer_meta (`+sanitizerMetaCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(scope, scope_id) DO UPDATE SET
			denylist_enabled   = excluded.denylist_enabled,
			envelope_enabled   = excluded.envelope_enabled,
			classifier_enabled = excluded.classifier_enabled,
			classifier_model   = excluded.classifier_model,
			action_on_match    = excluded.action_on_match,
			updated_at         = excluded.updated_at`,
		m.ID, m.Scope, m.ScopeID,
		boolToInt(m.DenylistEnabled), boolToInt(m.EnvelopeEnabled),
		boolToInt(m.ClassifierEnabled), m.ClassifierModel, m.ActionOnMatch,
		m.DetectedCount, m.RedactedCount, m.BlockedCount,
		formatTimePtr(m.LastEventAt),
		formatTime(m.CreatedAt), formatTime(m.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("upsert sanitizer_meta: %w", err)
	}
	return nil
}

// ListSanitizerMeta returns every policy row ordered by (scope, scope_id).
func (d *DB) ListSanitizerMeta(ctx context.Context) ([]store.SanitizerMeta, error) {
	rows, err := d.q.QueryContext(ctx,
		`SELECT `+sanitizerMetaCols+` FROM sanitizer_meta
		 ORDER BY scope, scope_id`)
	if err != nil {
		return nil, fmt.Errorf("list sanitizer_meta: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []store.SanitizerMeta
	for rows.Next() {
		m, err := scanSanitizerMeta(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sanitizer_meta: %w", err)
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

// IncrementSanitizerCounter atomically bumps the named counter by 1 and
// stamps last_event_at = now for the (scope, scopeID) row. counter must
// be one of detected_count, redacted_count, blocked_count.
func (d *DB) IncrementSanitizerCounter(
	ctx context.Context, scope, scopeID, counter string,
) error {
	col, ok := validSanitizerCounters[counter]
	if !ok {
		return fmt.Errorf("IncrementSanitizerCounter: invalid counter %q", counter)
	}
	now := formatTime(time.Now().UTC())
	// Safe to interpolate `col` here because it came from the allowlist
	// above; never user input.
	res, err := d.q.ExecContext(ctx, `
		UPDATE sanitizer_meta
		SET `+col+` = `+col+` + 1, last_event_at = ?, updated_at = ?
		WHERE scope = ? AND scope_id = ?`,
		now, now, scope, scopeID,
	)
	if err != nil {
		return fmt.Errorf("increment sanitizer counter: %w", err)
	}
	return checkRowsAffected(res)
}

func scanSanitizerMeta(r scanner) (*store.SanitizerMeta, error) {
	var (
		m                              store.SanitizerMeta
		denylist, envelope, classifier int
		lastEvent                      sql.NullString
		createdAt, updatedAt           string
	)
	err := r.Scan(
		&m.ID, &m.Scope, &m.ScopeID, &denylist, &envelope, &classifier,
		&m.ClassifierModel, &m.ActionOnMatch,
		&m.DetectedCount, &m.RedactedCount, &m.BlockedCount,
		&lastEvent, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}
	m.DenylistEnabled = denylist != 0
	m.EnvelopeEnabled = envelope != 0
	m.ClassifierEnabled = classifier != 0
	if lastEvent.Valid {
		t := parseTime(lastEvent.String)
		m.LastEventAt = &t
	}
	m.CreatedAt = parseTime(createdAt)
	m.UpdatedAt = parseTime(updatedAt)
	return &m, nil
}
