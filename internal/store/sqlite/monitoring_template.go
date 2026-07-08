// monitoring_template.go — sqlite impl of the distiller's template
// store + raw-line ring buffer (migration 128, M3). Templates are the
// dedup unit: one row per masked line shape per source, with lifetime
// counts. Window counts are computed from log_lines at query time so
// digests are stateless.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const logTemplateCols = `id, source_id, masked, severity, count,
    window_count, first_seen, last_seen, sample_first, sample_last,
    acked, ack_note`

// UpsertLogTemplate records n occurrences of a template. Returns
// isNew=true when this is the first time the shape was seen on the
// source — the distiller's novelty signal.
func (d *DB) UpsertLogTemplate(ctx context.Context, t *store.LogTemplate, n int64) (bool, error) {
	if t == nil || t.ID == "" || t.SourceID == "" {
		return false, errors.New("UpsertLogTemplate: id + source_id required")
	}
	if n <= 0 {
		return false, errors.New("UpsertLogTemplate: n must be positive")
	}
	res, err := d.q.ExecContext(ctx, `
		UPDATE log_templates SET count = count + ?, last_seen = ?,
			sample_last = ?
		WHERE id = ?`,
		n, formatTime(t.LastSeen.UTC()), t.SampleLast, t.ID)
	if err != nil {
		return false, fmt.Errorf("upsert log template: %w", err)
	}
	if affected, _ := res.RowsAffected(); affected > 0 {
		return false, nil
	}
	_, err = d.q.ExecContext(ctx, `
		INSERT INTO log_templates (`+logTemplateCols+`)
		VALUES (?, ?, ?, ?, ?, 0, ?, ?, ?, ?, 0, '')`,
		t.ID, t.SourceID, t.Masked, t.Severity, n,
		formatTime(t.FirstSeen.UTC()), formatTime(t.LastSeen.UTC()),
		t.SampleFirst, t.SampleLast)
	if err != nil {
		return false, mapConstraintError(err)
	}
	return true, nil
}

// GetLogTemplate returns one template row or ErrLogTemplateNotFound.
func (d *DB) GetLogTemplate(ctx context.Context, id string) (*store.LogTemplate, error) {
	row := d.q.QueryRowContext(ctx,
		`SELECT `+logTemplateCols+` FROM log_templates WHERE id = ?`, id)
	t, err := scanLogTemplate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrLogTemplateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get log template: %w", err)
	}
	return t, nil
}

// ListLogTemplates returns templates for the given sources, filtered
// to those last seen at/after since (zero = all), ordered by lifetime
// count descending. limit<=0 means no cap.
func (d *DB) ListLogTemplates(ctx context.Context, sourceIDs []string, since time.Time, limit int) ([]*store.LogTemplate, error) {
	if len(sourceIDs) == 0 {
		return []*store.LogTemplate{}, nil
	}
	query := `SELECT ` + logTemplateCols + ` FROM log_templates
		WHERE source_id IN (` + placeholders(len(sourceIDs)) + `)`
	args := make([]any, 0, len(sourceIDs)+2)
	for _, id := range sourceIDs {
		args = append(args, id)
	}
	if !since.IsZero() {
		query += ` AND last_seen >= ?`
		args = append(args, formatTime(since.UTC()))
	}
	query += ` ORDER BY count DESC`
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := d.q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list log templates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []*store.LogTemplate{}
	for rows.Next() {
		t, err := scanLogTemplate(rows)
		if err != nil {
			return nil, fmt.Errorf("scan log template: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AckLogTemplate marks a template known/expected, excluding it from
// novelty wake-ups. Note is optional context for teammates.
func (d *DB) AckLogTemplate(ctx context.Context, id, note string) error {
	res, err := d.q.ExecContext(ctx,
		`UPDATE log_templates SET acked = 1, ack_note = ? WHERE id = ?`, note, id)
	if err != nil {
		return fmt.Errorf("ack log template: %w", err)
	}
	return requireRowAffected(res, store.ErrLogTemplateNotFound)
}

// InsertLogLines batch-appends redacted lines to the ring buffer.
func (d *DB) InsertLogLines(ctx context.Context, lines []store.LogLine) error {
	if len(lines) == 0 {
		return nil
	}
	var sb strings.Builder
	sb.WriteString(`INSERT INTO log_lines (source_id, template_id, ts, line) VALUES `)
	args := make([]any, 0, len(lines)*4)
	for i, l := range lines {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString("(?, ?, ?, ?)")
		args = append(args, l.SourceID, l.TemplateID, formatTime(l.TS.UTC()), l.Line)
	}
	if _, err := d.q.ExecContext(ctx, sb.String(), args...); err != nil {
		return fmt.Errorf("insert log lines: %w", err)
	}
	return nil
}

// PruneLogLines enforces the per-source retention caps: age first,
// then oldest-beyond-byte-budget. Returns rows removed.
func (d *DB) PruneLogLines(ctx context.Context, sourceID string, maxAge time.Time, maxBytes int64) (int64, error) {
	res, err := d.q.ExecContext(ctx,
		`DELETE FROM log_lines WHERE source_id = ? AND ts < ?`,
		sourceID, formatTime(maxAge.UTC()))
	if err != nil {
		return 0, fmt.Errorf("prune log lines by age: %w", err)
	}
	removed, _ := res.RowsAffected()
	if maxBytes <= 0 {
		return removed, nil
	}
	var total sql.NullInt64
	if err := d.q.QueryRowContext(ctx,
		`SELECT SUM(LENGTH(line)) FROM log_lines WHERE source_id = ?`,
		sourceID).Scan(&total); err != nil {
		return removed, fmt.Errorf("prune log lines size: %w", err)
	}
	if !total.Valid || total.Int64 <= maxBytes {
		return removed, nil
	}
	// Drop the oldest half beyond budget in one pass; the next prune
	// converges. Avoids a per-row loop on a hot path.
	res, err = d.q.ExecContext(ctx, `
		DELETE FROM log_lines WHERE rowid IN (
			SELECT rowid FROM log_lines WHERE source_id = ?
			ORDER BY ts ASC
			LIMIT (SELECT COUNT(*)/2 FROM log_lines WHERE source_id = ?)
		)`, sourceID, sourceID)
	if err != nil {
		return removed, fmt.Errorf("prune log lines by size: %w", err)
	}
	n, _ := res.RowsAffected()
	return removed + n, nil
}

// CountLinesByTemplate returns per-template line counts within the
// window — the digest's stateless window_count.
func (d *DB) CountLinesByTemplate(ctx context.Context, sourceIDs []string, since time.Time) (map[string]int64, error) {
	out := map[string]int64{}
	if len(sourceIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(sourceIDs)+1)
	for _, id := range sourceIDs {
		args = append(args, id)
	}
	args = append(args, formatTime(since.UTC()))
	rows, err := d.q.QueryContext(ctx, `
		SELECT template_id, COUNT(*) FROM log_lines
		WHERE source_id IN (`+placeholders(len(sourceIDs))+`) AND ts >= ?
		GROUP BY template_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("count lines by template: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var n int64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// SearchLogLines greps the ring buffer with a LIKE match, newest
// first, capped.
func (d *DB) SearchLogLines(ctx context.Context, sourceID, q string, limit int) ([]*store.LogLine, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT source_id, template_id, ts, line FROM log_lines
		WHERE source_id = ? AND line LIKE ?
		ORDER BY ts DESC LIMIT ?`,
		sourceID, "%"+q+"%", limit)
	if err != nil {
		return nil, fmt.Errorf("search log lines: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectLogLines(rows)
}

// ListLogLinesByTemplate returns recent raw lines for one template —
// the drill-down surface behind monitoring.raw.
func (d *DB) ListLogLinesByTemplate(ctx context.Context, templateID string, limit int) ([]*store.LogLine, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT source_id, template_id, ts, line FROM log_lines
		WHERE template_id = ? ORDER BY ts DESC LIMIT ?`, templateID, limit)
	if err != nil {
		return nil, fmt.Errorf("list log lines by template: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return collectLogLines(rows)
}

// placeholders renders "?, ?, ?" for an IN clause of n items.
func placeholders(n int) string {
	return strings.TrimRight(strings.Repeat("?,", n), ",")
}

func collectLogLines(rows *sql.Rows) ([]*store.LogLine, error) {
	out := []*store.LogLine{}
	for rows.Next() {
		var l store.LogLine
		var ts string
		if err := rows.Scan(&l.SourceID, &l.TemplateID, &ts, &l.Line); err != nil {
			return nil, err
		}
		l.TS = parseTime(ts)
		out = append(out, &l)
	}
	return out, rows.Err()
}

func scanLogTemplate(row interface{ Scan(...any) error }) (*store.LogTemplate, error) {
	var t store.LogTemplate
	var acked int
	var firstSeen, lastSeen string
	err := row.Scan(&t.ID, &t.SourceID, &t.Masked, &t.Severity, &t.Count,
		&t.WindowCount, &firstSeen, &lastSeen, &t.SampleFirst, &t.SampleLast,
		&acked, &t.AckNote)
	if err != nil {
		return nil, err
	}
	t.Acked = acked != 0
	t.FirstSeen = parseTime(firstSeen)
	t.LastSeen = parseTime(lastSeen)
	return &t, nil
}
