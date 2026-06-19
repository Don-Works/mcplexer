package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	_ "modernc.org/sqlite"
)

type dataQueryItem struct {
	ID      string
	Ordinal int
	Kind    string
	Payload json.RawMessage
	Text    string
}

func (d *DB) SearchDataCollection(
	ctx context.Context, s store.DataSearch,
) ([]store.DataHit, error) {
	if strings.TrimSpace(s.Query) == "" {
		return nil, errors.New("SearchDataCollection: query required")
	}
	c, err := d.GetDataCollection(ctx, s.WorkspaceID, s.Name)
	if err != nil {
		return nil, err
	}
	limit := s.Limit
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	rows, err := d.q.QueryContext(ctx, `
		SELECT i.id, i.ordinal, i.kind, i.payload_json, i.text, f.rank
		FROM data_workbench_items_fts f
		JOIN data_workbench_items i ON i.id = f.item_id
		WHERE data_workbench_items_fts MATCH ? AND f.collection_id = ?
		ORDER BY f.rank LIMIT ?`,
		escapeFTS5Query(s.Query), c.ID, limit)
	if err != nil {
		return nil, rewriteFTS5Error(err, s.Query)
	}
	defer func() { _ = rows.Close() }()

	var hits []store.DataHit
	for rows.Next() {
		var h store.DataHit
		var payload string
		if err := rows.Scan(&h.ID, &h.Ordinal, &h.Kind, &payload, &h.Text, &h.Score); err != nil {
			return nil, err
		}
		h.Payload = []byte(payload)
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

func (d *DB) QueryDataCollection(
	ctx context.Context, q store.DataQuery,
) ([]map[string]any, error) {
	if err := validateWorkbenchSQL(q.SQL); err != nil {
		return nil, err
	}
	c, err := d.GetDataCollection(ctx, q.WorkspaceID, q.Name)
	if err != nil {
		return nil, err
	}
	items, err := d.loadDataItems(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	limit := q.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return runWorkbenchQuery(ctx, c.Name, items, q.SQL, limit)
}

func (d *DB) loadDataItems(ctx context.Context, collectionID string) ([]dataQueryItem, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, ordinal, kind, payload_json, text
		FROM data_workbench_items
		WHERE collection_id = ? ORDER BY ordinal`, collectionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []dataQueryItem
	for rows.Next() {
		var it dataQueryItem
		var payload string
		if err := rows.Scan(&it.ID, &it.Ordinal, &it.Kind, &payload, &it.Text); err != nil {
			return nil, err
		}
		it.Payload = []byte(payload)
		out = append(out, it)
	}
	return out, rows.Err()
}

func runWorkbenchQuery(
	ctx context.Context, name string, items []dataQueryItem, sqlText string, limit int,
) ([]map[string]any, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	cols := inferQueryColumns(items)
	if err := createWorkbenchTempTable(ctx, db, cols); err != nil {
		return nil, err
	}
	if err := insertWorkbenchTempRows(ctx, db, cols, items); err != nil {
		return nil, err
	}
	if isSQLIdent(name) && name != "data" {
		if _, err := db.ExecContext(ctx,
			`CREATE VIEW `+quoteIdent(name)+` AS SELECT * FROM data`); err != nil {
			return nil, err
		}
	}
	return selectWorkbenchRows(ctx, db, sqlText, limit)
}

func validateWorkbenchSQL(sqlText string) error {
	s := strings.TrimSpace(sqlText)
	lower := strings.ToLower(s)
	if s == "" {
		return errors.New("query SQL is required")
	}
	if strings.Contains(s, ";") {
		return errors.New("query must be a single SELECT without semicolons")
	}
	if !strings.HasPrefix(lower, "select ") && !strings.HasPrefix(lower, "with ") {
		return errors.New("query must start with SELECT or WITH")
	}
	deny := regexp.MustCompile(`(?i)\b(attach|detach|pragma|insert|update|delete|drop|alter|create|replace|vacuum|reindex)\b`)
	if deny.FindString(s) != "" {
		return errors.New("query may only read the scratch collection")
	}
	return nil
}

func inferQueryColumns(items []dataQueryItem) []string {
	seen := map[string]bool{"_ordinal": true, "_payload": true, "_text": true}
	var cols []string
	for _, it := range items {
		var obj map[string]any
		if err := json.Unmarshal(it.Payload, &obj); err != nil {
			continue
		}
		for k := range obj {
			if k == "" || seen[k] {
				continue
			}
			seen[k] = true
			cols = append(cols, k)
		}
	}
	sort.Strings(cols)
	return append([]string{"_ordinal", "_payload", "_text"}, cols...)
}

func createWorkbenchTempTable(ctx context.Context, db *sql.DB, cols []string) error {
	parts := make([]string, 0, len(cols))
	for _, c := range cols {
		typ := "TEXT"
		if c == "_ordinal" {
			typ = "INTEGER"
		}
		parts = append(parts, quoteIdent(c)+" "+typ)
	}
	_, err := db.ExecContext(ctx, `CREATE TABLE data (`+strings.Join(parts, ", ")+`)`)
	return err
}

func insertWorkbenchTempRows(
	ctx context.Context, db *sql.DB, cols []string, items []dataQueryItem,
) error {
	marks := strings.TrimRight(strings.Repeat("?,", len(cols)), ",")
	stmt, err := db.PrepareContext(ctx,
		`INSERT INTO data (`+quoteIdentList(cols)+`) VALUES (`+marks+`)`)
	if err != nil {
		return err
	}
	defer func() { _ = stmt.Close() }()

	for _, it := range items {
		obj := map[string]any{}
		_ = json.Unmarshal(it.Payload, &obj)
		vals := make([]any, 0, len(cols))
		for _, col := range cols {
			switch col {
			case "_ordinal":
				vals = append(vals, it.Ordinal)
			case "_payload":
				vals = append(vals, string(it.Payload))
			case "_text":
				vals = append(vals, it.Text)
			default:
				vals = append(vals, obj[col])
			}
		}
		if _, err := stmt.ExecContext(ctx, vals...); err != nil {
			return err
		}
	}
	return nil
}

func selectWorkbenchRows(
	ctx context.Context, db *sql.DB, sqlText string, limit int,
) ([]map[string]any, error) {
	rows, err := db.QueryContext(ctx, `SELECT * FROM (`+sqlText+`) LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query scratch collection: %w", err)
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		ptrs := make([]any, len(cols))
		vals := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(cols))
		for i, col := range cols {
			row[col] = sqlValue(vals[i])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func sqlValue(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

func quoteIdentList(cols []string) string {
	out := make([]string, len(cols))
	for i, c := range cols {
		out[i] = quoteIdent(c)
	}
	return strings.Join(out, ", ")
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func isSQLIdent(s string) bool {
	if s == "" {
		return false
	}
	for i, r := range s {
		if r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			continue
		}
		if i > 0 && r >= '0' && r <= '9' {
			continue
		}
		return false
	}
	return true
}
