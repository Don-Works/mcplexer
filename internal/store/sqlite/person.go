// person.go — SQLite implementation of store.PersonStore (migration 094).
// A Person is a workspace-scoped CRM contact record, the first brain entity
// kind beyond task/memory/workspace. Mirrors the memory subsystem's substrate:
// a canonical row + FTS5 mirror. Entity-link operations live in
// person_entities.go to honour the 300-line cap.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/oklog/ulid/v2"
)

// WritePerson inserts a row. Name is required + unique per workspace (a
// duplicate maps to ErrAlreadyExists via mapConstraintError). ID/workspace/
// timestamps/source default.
func (d *DB) WritePerson(ctx context.Context, p *store.PersonEntry) error {
	if p == nil {
		return errors.New("WritePerson: nil entry")
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("WritePerson: name required")
	}
	normalizePersonWorkspaceID(p)
	if p.SourceKind == "" {
		p.SourceKind = store.PersonSourceAgent
	}
	if p.ID == "" {
		p.ID = ulid.Make().String()
	}
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = now
	}
	tags := normalizeJSON(p.TagsJSON, "[]")
	pinned := 0
	if p.Pinned {
		pinned = 1
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO crm_person (
			id, workspace_id, name, email, phone, company, role,
			tags_json, notes,
			source_kind, source_session_id, source_tool_call_id,
			pinned, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.ID, p.WorkspaceID, p.Name, p.Email, p.Phone, p.Company, p.Role,
		tags, p.Notes,
		p.SourceKind, nullString(p.SourceSessionID), nullString(p.SourceToolCallID),
		pinned, p.CreatedAt.Unix(), p.UpdatedAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert person: %w", mapConstraintError(err))
	}
	return nil
}

// UpdatePerson rewrites the human-editable fields in place. The crm_person_au
// FTS trigger fires. ErrNotFound when the row is absent or soft-deleted.
func (d *DB) UpdatePerson(ctx context.Context, p *store.PersonEntry) error {
	if p == nil {
		return errors.New("UpdatePerson: nil entry")
	}
	if strings.TrimSpace(p.ID) == "" {
		return errors.New("UpdatePerson: id required")
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("UpdatePerson: name required")
	}
	normalizePersonWorkspaceID(p)
	tags := normalizeJSON(p.TagsJSON, "[]")
	pinned := 0
	if p.Pinned {
		pinned = 1
	}
	if p.UpdatedAt.IsZero() {
		p.UpdatedAt = time.Now().UTC()
	}
	res, err := d.q.ExecContext(ctx, `
		UPDATE crm_person SET
			workspace_id = ?, name = ?, email = ?, phone = ?, company = ?, role = ?,
			tags_json = ?, notes = ?, pinned = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`,
		p.WorkspaceID, p.Name, p.Email, p.Phone, p.Company, p.Role,
		tags, p.Notes, pinned, p.UpdatedAt.Unix(),
		p.ID,
	)
	if err != nil {
		return fmt.Errorf("update person: %w", mapConstraintError(err))
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// GetPerson returns one row by id, excluding soft-deleted rows.
func (d *DB) GetPerson(ctx context.Context, id string) (*store.PersonEntry, error) {
	row := d.q.QueryRowContext(ctx, `
		SELECT `+personSelectCols+`
		FROM crm_person
		WHERE id = ? AND deleted_at IS NULL`, id)
	p, err := scanPerson(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get person: %w", err)
	}
	return p, nil
}

// ListPeople returns rows matching the filter, ordered by updated_at DESC.
func (d *DB) ListPeople(ctx context.Context, f store.PersonFilter) ([]store.PersonEntry, error) {
	where, args := personWhere(f, "")
	q := `SELECT ` + personSelectCols + `
		FROM crm_person
		WHERE 1=1 ` + where + `
		ORDER BY updated_at DESC, id DESC`
	if f.Limit > 0 {
		q += ` LIMIT ?`
		args = append(args, f.Limit)
		if f.Offset > 0 {
			q += ` OFFSET ?`
			args = append(args, f.Offset)
		}
	}
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list people: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var out []store.PersonEntry
	for rows.Next() {
		p, err := scanPerson(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("scan person: %w", err)
		}
		if !personTagsMatch(*p, f.Tags) {
			continue
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// SearchPeople runs an FTS5 MATCH over every text field in the filter scope.
// An empty/sanitised-away query falls back to ListPeople.
func (d *DB) SearchPeople(
	ctx context.Context, f store.PersonFilter, query string,
) ([]store.PersonHit, error) {
	expr := sanitizeFTS5Query(query)
	if expr == "" {
		entries, err := d.ListPeople(ctx, f)
		if err != nil {
			return nil, err
		}
		hits := make([]store.PersonHit, 0, len(entries))
		for _, e := range entries {
			hits = append(hits, store.PersonHit{Entry: e, Score: 0, Source: "list"})
		}
		return hits, nil
	}
	where, args := personWhere(f, "p")
	args = append([]any{expr}, args...)
	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT ` + personColsPrefixed("p") + `, fts.rank
		FROM crm_person_fts fts
		JOIN crm_person p ON p.rowid = fts.rowid
		WHERE crm_person_fts MATCH ? ` + where + `
		ORDER BY fts.rank
		LIMIT ?`
	args = append(args, limit)
	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("search people: %w", err)
	}
	defer rows.Close() //nolint:errcheck
	var hits []store.PersonHit
	for rows.Next() {
		var rank float64
		p, err := scanPersonWithExtra(rows.Scan, &rank)
		if err != nil {
			return nil, fmt.Errorf("scan person hit: %w", err)
		}
		if !personTagsMatch(*p, f.Tags) {
			continue
		}
		hits = append(hits, store.PersonHit{Entry: *p, Score: rank, Source: "fts"})
	}
	return hits, rows.Err()
}

// SoftDeletePerson stamps deleted_at. Idempotent.
func (d *DB) SoftDeletePerson(ctx context.Context, id string) error {
	now := time.Now().Unix()
	res, err := d.q.ExecContext(ctx, `
		UPDATE crm_person SET deleted_at = ?, updated_at = ?
		WHERE id = ? AND deleted_at IS NULL`, now, now, id)
	if err != nil {
		return fmt.Errorf("soft delete person: %w", err)
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		// Already deleted or never existed — idempotent no-op.
		return nil
	}
	return nil
}

// CountPeople returns the count of live (non-deleted) people.
func (d *DB) CountPeople(ctx context.Context) (int, error) {
	var n int
	err := d.q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM crm_person WHERE deleted_at IS NULL`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count people: %w", err)
	}
	return n, nil
}

const personSelectCols = `id, name, email, phone, company, role,
		workspace_id,
		tags_json, notes,
		source_kind, source_session_id, source_tool_call_id,
		pinned, created_at, updated_at, deleted_at`

// personWhere builds the shared WHERE fragment for list/search. prefix
// qualifies columns (e.g. "p") in the FTS join; "" for the bare table.
func personWhere(f store.PersonFilter, prefix string) (string, []any) {
	col := func(name string) string {
		if prefix == "" {
			return name
		}
		return prefix + "." + name
	}
	var b strings.Builder
	var args []any
	if !f.IncludeDeleted {
		b.WriteString(" AND " + col("deleted_at") + " IS NULL")
	}
	if strings.TrimSpace(f.WorkspaceID) != "" {
		b.WriteString(" AND " + col("workspace_id") + " = ?")
		args = append(args, strings.TrimSpace(f.WorkspaceID))
	}
	if f.Name != "" {
		b.WriteString(" AND " + col("name") + " = ?")
		args = append(args, f.Name)
	}
	if f.Company != "" {
		b.WriteString(" AND " + col("company") + " = ?")
		args = append(args, f.Company)
	}
	for _, e := range f.Entities {
		kind := strings.ToLower(strings.TrimSpace(e.Kind))
		id := strings.ToLower(strings.TrimSpace(e.ID))
		if kind == "" || id == "" {
			continue
		}
		b.WriteString(` AND EXISTS (SELECT 1 FROM person_entities pe
			WHERE pe.person_id = ` + col("id") + `
			  AND pe.entity_kind = ? AND pe.entity_id = ?)`)
		args = append(args, kind, id)
	}
	return b.String(), args
}

func personColsPrefixed(prefix string) string {
	cols := strings.Split(personSelectCols, ",")
	for i, c := range cols {
		cols[i] = prefix + "." + strings.TrimSpace(c)
	}
	return strings.Join(cols, ", ")
}

// personTagsMatch reports whether p carries every tag in want (AND), applied
// in Go because tags are a JSON blob, not a normalised column.
func personTagsMatch(p store.PersonEntry, want []string) bool {
	if len(want) == 0 {
		return true
	}
	var have []string
	if len(p.TagsJSON) > 0 {
		_ = json.Unmarshal(p.TagsJSON, &have)
	}
	set := make(map[string]struct{}, len(have))
	for _, t := range have {
		set[strings.ToLower(t)] = struct{}{}
	}
	for _, t := range want {
		if _, ok := set[strings.ToLower(t)]; !ok {
			return false
		}
	}
	return true
}

func scanPerson(scan func(...any) error) (*store.PersonEntry, error) {
	return scanPersonWithExtra(scan)
}

func scanPersonWithExtra(scan func(...any) error, extra ...any) (*store.PersonEntry, error) {
	var (
		p                            store.PersonEntry
		tags                         string
		sourceSession, sourceToolCID sql.NullString
		deletedAt                    sql.NullInt64
		createdAt, updatedAt         int64
		pinned                       int
	)
	dest := []any{
		&p.ID, &p.Name, &p.Email, &p.Phone, &p.Company, &p.Role,
		&p.WorkspaceID,
		&tags, &p.Notes,
		&p.SourceKind, &sourceSession, &sourceToolCID,
		&pinned, &createdAt, &updatedAt, &deletedAt,
	}
	dest = append(dest, extra...)
	if err := scan(dest...); err != nil {
		return nil, err
	}
	p.TagsJSON = json.RawMessage(tags)
	if sourceSession.Valid {
		p.SourceSessionID = sourceSession.String
	}
	if sourceToolCID.Valid {
		p.SourceToolCallID = sourceToolCID.String
	}
	p.Pinned = pinned != 0
	p.CreatedAt = time.Unix(createdAt, 0).UTC()
	p.UpdatedAt = time.Unix(updatedAt, 0).UTC()
	if deletedAt.Valid {
		t := time.Unix(deletedAt.Int64, 0).UTC()
		p.DeletedAt = &t
	}
	normalizePersonWorkspaceID(&p)
	return &p, nil
}

func normalizePersonWorkspaceID(p *store.PersonEntry) {
	p.WorkspaceID = strings.TrimSpace(p.WorkspaceID)
	if p.WorkspaceID == "" || p.WorkspaceID == "global" {
		p.WorkspaceID = store.PersonDefaultWorkspaceID
	}
}
