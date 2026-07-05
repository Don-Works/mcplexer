package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// codeIndexScoreExpr negates BM25 so higher Score means a better match
// (raw bm25 is lower-is-better).
const codeIndexScoreExpr = `-bm25(%s)`

// ListCodeIndexFileStats returns lightweight freshness tuples for incremental builds.
func (d *DB) ListCodeIndexFileStats(
	ctx context.Context, workspaceID string,
) ([]store.CodeIndexFileStat, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT path, size_bytes, mtime_unix, content_hash
		FROM code_index_files
		WHERE workspace_id = ?
		ORDER BY path`, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("list code index file stats: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.CodeIndexFileStat
	for rows.Next() {
		var s store.CodeIndexFileStat
		if err := rows.Scan(&s.Path, &s.SizeBytes, &s.MtimeUnix, &s.ContentHash); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetCodeIndexFile returns one indexed file row.
func (d *DB) GetCodeIndexFile(
	ctx context.Context, workspaceID, path string,
) (*store.CodeIndexFile, error) {
	row := d.q.QueryRowContext(ctx, `SELECT `+codeIndexFileCols+`
		FROM code_index_files
		WHERE workspace_id = ? AND path = ?`, workspaceID, path)
	return scanCodeIndexFile(row)
}

// ListCodeIndexSymbolsByPath returns symbols for one file in source order.
func (d *DB) ListCodeIndexSymbolsByPath(
	ctx context.Context, workspaceID, path string,
) ([]store.CodeIndexSymbol, error) {
	rows, err := d.q.QueryContext(ctx, `SELECT `+codeIndexSymbolColsPrefixed("s")+`
		FROM code_index_symbols s
		JOIN code_index_files f ON f.id = s.file_id
		WHERE f.workspace_id = ? AND f.path = ?
		ORDER BY s.start_line ASC`, workspaceID, path)
	if err != nil {
		return nil, fmt.Errorf("list code index symbols: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.CodeIndexSymbol
	for rows.Next() {
		sym, err := scanCodeIndexSymbol(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sym)
	}
	return out, rows.Err()
}

// SearchCodeIndexSymbols runs an FTS5 BM25 query over indexed symbols.
func (d *DB) SearchCodeIndexSymbols(
	ctx context.Context, q store.CodeIndexSymbolQuery,
) ([]store.CodeIndexSymbolHit, error) {
	expr := sanitizeFTS5Query(q.Query)
	if expr == "" {
		return nil, nil
	}
	limit := codeIndexLimit(q.Limit, 20, 100)
	where := `fts.workspace_id = ?`
	args := []any{expr, q.WorkspaceID}
	if k := strings.TrimSpace(q.Kind); k != "" {
		where += ` AND s.kind = ?`
		args = append(args, k)
	}
	if q.ExportedOnly {
		where += ` AND s.exported = 1`
	}
	args = append(args, limit)

	scoreExpr := fmt.Sprintf(codeIndexScoreExpr, "code_index_symbols_fts")
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+codeIndexSymbolColsPrefixed("s")+`, f.path, `+scoreExpr+`
		FROM code_index_symbols_fts fts
		JOIN code_index_symbols s ON s.rowid = fts.rowid
		JOIN code_index_files f ON f.id = s.file_id
		WHERE code_index_symbols_fts MATCH ? AND `+where+`
		ORDER BY bm25(code_index_symbols_fts) ASC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("search code index symbols: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanCodeIndexSymbolHits(rows)
}

// SearchCodeIndexFiles runs an FTS5 BM25 query over indexed files.
func (d *DB) SearchCodeIndexFiles(
	ctx context.Context, workspaceID, query string, limit int,
) ([]store.CodeIndexFileHit, error) {
	expr := sanitizeFTS5Query(query)
	if expr == "" {
		return nil, nil
	}
	limit = codeIndexLimit(limit, 20, 100)
	scoreExpr := fmt.Sprintf(codeIndexScoreExpr, "code_index_files_fts")
	rows, err := d.q.QueryContext(ctx, `
		SELECT f.id, f.workspace_id, f.path, f.path_tokens, f.language, f.package,
			f.size_bytes, f.line_count, f.mtime_unix, f.content_hash, f.doc_summary,
			f.is_test, f.skipped_reason, f.indexed_at, `+scoreExpr+`
		FROM code_index_files_fts fts
		JOIN code_index_files f ON f.rowid = fts.rowid
		WHERE code_index_files_fts MATCH ? AND fts.workspace_id = ?
		ORDER BY bm25(code_index_files_fts) ASC
		LIMIT ?`, expr, workspaceID, limit)
	if err != nil {
		return nil, fmt.Errorf("search code index files: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.CodeIndexFileHit
	for rows.Next() {
		hit, err := scanCodeIndexFileHit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *hit)
	}
	return out, rows.Err()
}

// ListCodeIndexEdges returns import edges scoped by from-path or to-path.
func (d *DB) ListCodeIndexEdges(
	ctx context.Context, f store.CodeIndexEdgeFilter,
) ([]store.CodeIndexEdgeHit, error) {
	limit := codeIndexLimit(f.Limit, 50, 200)
	var q string
	var args []any

	switch {
	case strings.TrimSpace(f.FromPath) != "":
		q = `
			SELECT ff.path, e.kind, e.to_path, e.to_module
			FROM code_index_edges e
			JOIN code_index_files ff ON ff.id = e.from_file_id
			WHERE e.workspace_id = ? AND ff.path = ?
			ORDER BY e.to_path LIMIT ?`
		args = []any{f.WorkspaceID, f.FromPath, limit}
	case strings.TrimSpace(f.ToPath) != "":
		q = `
			SELECT ff.path, e.kind, e.to_path, e.to_module
			FROM code_index_edges e
			JOIN code_index_files ff ON ff.id = e.from_file_id
			WHERE e.workspace_id = ? AND e.to_path = ?
			ORDER BY ff.path LIMIT ?`
		args = []any{f.WorkspaceID, f.ToPath, limit}
	default:
		return nil, errors.New("ListCodeIndexEdges: from_path or to_path required")
	}

	rows, err := d.q.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list code index edges: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.CodeIndexEdgeHit
	for rows.Next() {
		var h store.CodeIndexEdgeHit
		if err := rows.Scan(&h.FromPath, &h.Kind, &h.ToPath, &h.ToModule); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// GetCodeIndexBuild returns the workspace build row.
func (d *DB) GetCodeIndexBuild(
	ctx context.Context, workspaceID string,
) (*store.CodeIndexBuild, error) {
	row := d.q.QueryRowContext(ctx, `SELECT `+codeIndexBuildCols+`
		FROM code_index_builds WHERE workspace_id = ?`, workspaceID)
	return scanCodeIndexBuild(row)
}

func codeIndexLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

func scanCodeIndexFile(r scanner) (*store.CodeIndexFile, error) {
	var f store.CodeIndexFile
	var isTest int
	var indexedAt string
	err := r.Scan(&f.ID, &f.WorkspaceID, &f.Path, &f.PathTokens, &f.Language,
		&f.Package, &f.SizeBytes, &f.LineCount, &f.MtimeUnix, &f.ContentHash,
		&f.DocSummary, &isTest, &f.SkippedReason, &indexedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	f.IsTest = isTest != 0
	f.IndexedAt = parseTime(indexedAt)
	return &f, nil
}

func scanCodeIndexSymbol(r scanner) (*store.CodeIndexSymbol, error) {
	var s store.CodeIndexSymbol
	var exported int
	err := r.Scan(&s.ID, &s.FileID, &s.WorkspaceID, &s.Name, &s.NameTokens,
		&s.Kind, &s.Receiver, &s.Signature, &s.Doc, &s.StartLine, &s.EndLine, &exported)
	if err != nil {
		return nil, err
	}
	s.Exported = exported != 0
	return &s, nil
}

func scanCodeIndexSymbolHits(rows *sql.Rows) ([]store.CodeIndexSymbolHit, error) {
	var out []store.CodeIndexSymbolHit
	for rows.Next() {
		var hit store.CodeIndexSymbolHit
		var exported int
		if err := rows.Scan(
			&hit.Symbol.ID, &hit.Symbol.FileID, &hit.Symbol.WorkspaceID,
			&hit.Symbol.Name, &hit.Symbol.NameTokens, &hit.Symbol.Kind,
			&hit.Symbol.Receiver, &hit.Symbol.Signature, &hit.Symbol.Doc,
			&hit.Symbol.StartLine, &hit.Symbol.EndLine, &exported,
			&hit.Path, &hit.Score); err != nil {
			return nil, err
		}
		hit.Symbol.Exported = exported != 0
		out = append(out, hit)
	}
	return out, rows.Err()
}

func scanCodeIndexFileHit(r scanner) (*store.CodeIndexFileHit, error) {
	var hit store.CodeIndexFileHit
	var isTest int
	var indexedAt string
	err := r.Scan(
		&hit.File.ID, &hit.File.WorkspaceID, &hit.File.Path, &hit.File.PathTokens,
		&hit.File.Language, &hit.File.Package, &hit.File.SizeBytes, &hit.File.LineCount,
		&hit.File.MtimeUnix, &hit.File.ContentHash, &hit.File.DocSummary,
		&isTest, &hit.File.SkippedReason, &indexedAt, &hit.Score)
	if err != nil {
		return nil, err
	}
	hit.File.IsTest = isTest != 0
	hit.File.IndexedAt = parseTime(indexedAt)
	return &hit, nil
}

func scanCodeIndexBuild(r scanner) (*store.CodeIndexBuild, error) {
	var b store.CodeIndexBuild
	var builtAt string
	err := r.Scan(&b.WorkspaceID, &b.RootPath, &b.GitHead, &b.DirtyCount,
		&builtAt, &b.DurationMS, &b.FileCount, &b.SymbolCount, &b.WarningsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.BuiltAt = parseTime(builtAt)
	return &b, nil
}