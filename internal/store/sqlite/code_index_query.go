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

// codeIndexChunkScoreExpr applies column weights favoring symbol/path tokens over content.
const codeIndexChunkScoreExpr = `-bm25(code_index_chunks_fts, 10.0, 8.0, 5.0, 2.0, 1.0)`

// ListCodeIndexFileStats returns lightweight freshness tuples for incremental builds.
func (d *DB) ListCodeIndexFileStats(
	ctx context.Context, workspaceID string,
) ([]store.CodeIndexFileStat, error) {
	rows, err := d.q.QueryContext(ctx, `
		SELECT path, size_bytes, mtime_unix, content_hash, chunk_version
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
		if err := rows.Scan(&s.Path, &s.SizeBytes, &s.MtimeUnix, &s.ContentHash, &s.ChunkVersion); err != nil {
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
			f.is_test, f.skipped_reason, f.chunk_version, f.indexed_at, `+scoreExpr+`
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

// SearchCodeIndexChunks runs a weighted FTS5 BM25 query over source chunks.
func (d *DB) SearchCodeIndexChunks(
	ctx context.Context, q store.CodeIndexChunkQuery,
) ([]store.CodeIndexChunkHit, error) {
	expr := sanitizeFTS5Query(q.Query)
	if expr == "" {
		return nil, nil
	}
	limit := codeIndexLimit(q.Limit, 20, 100)
	where := `fts.workspace_id = ?`
	args := []any{expr, q.WorkspaceID}
	if k := strings.TrimSpace(q.Kind); k != "" {
		where += ` AND c.kind = ?`
		args = append(args, k)
	}
	args = append(args, limit)

	rows, err := d.q.QueryContext(ctx, `
		SELECT `+codeIndexChunkColsPrefixed("c")+`, `+codeIndexChunkScoreExpr+`
		FROM code_index_chunks_fts fts
		JOIN code_index_chunks c ON c.rowid = fts.rowid
		WHERE code_index_chunks_fts MATCH ? AND `+where+`
		ORDER BY bm25(code_index_chunks_fts, 10.0, 8.0, 5.0, 2.0, 1.0) ASC, c.id ASC
		LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("search code index chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return scanCodeIndexChunkHits(rows)
}

// VectorSearchCodeIndexChunks runs vec0 KNN over chunk embeddings.
func (d *DB) VectorSearchCodeIndexChunks(
	ctx context.Context, workspaceID, embedModel string, embedVersion int,
	vector []float32, k int,
) ([]store.CodeIndexChunkHit, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, fmt.Errorf("VectorSearchCodeIndexChunks: workspace_id required")
	}
	if strings.TrimSpace(embedModel) == "" {
		return nil, fmt.Errorf("VectorSearchCodeIndexChunks: embed_model required")
	}
	if len(vector) != memoryVecDim {
		return nil, fmt.Errorf("VectorSearchCodeIndexChunks: vector dim %d, want %d",
			len(vector), memoryVecDim)
	}
	k = codeIndexLimit(k, 20, 100)
	vecJSON := vectorToJSON(vector)
	rows, err := d.q.QueryContext(ctx, `
		SELECT `+codeIndexChunkColsPrefixed("c")+`, v.distance
		FROM code_index_chunks_vec v
		JOIN code_index_chunks c ON c.id = v.chunk_id
		WHERE v.embedding MATCH ? AND v.k = ?
		  AND c.workspace_id = ?
		  AND c.embed_model = ?
		  AND c.embed_version = ?
		ORDER BY v.distance, c.id`, vecJSON, k, workspaceID, embedModel, embedVersion)
	if err != nil {
		return nil, fmt.Errorf("vector search code index chunks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.CodeIndexChunkHit
	for rows.Next() {
		hit, err := scanCodeIndexChunkVectorHit(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *hit)
	}
	return out, rows.Err()
}

// ListCodeIndexChunksNeedingEmbedding returns chunks with stale embedding meta.
func (d *DB) ListCodeIndexChunksNeedingEmbedding(
	ctx context.Context, workspaceID, embedModel string, embedVersion, limit int,
) ([]store.CodeIndexEmbedTarget, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(embedModel) == "" {
		return nil, fmt.Errorf("ListCodeIndexChunksNeedingEmbedding: workspace_id and embed_model required")
	}
	limit = codeIndexLimit(limit, 200, 500)
	rows, err := d.q.QueryContext(ctx, `
		SELECT id, path, symbol_name, content
		FROM code_index_chunks
		WHERE workspace_id = ?
		  AND (`+codeIndexChunkStaleEmbedSQL+`)
		ORDER BY id ASC
		LIMIT ?`, workspaceID, embedModel, embedVersion, limit)
	if err != nil {
		return nil, fmt.Errorf("list code index chunks needing embedding: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []store.CodeIndexEmbedTarget
	for rows.Next() {
		var (
			id         int64
			path       string
			symbolName string
			content    string
		)
		if err := rows.Scan(&id, &path, &symbolName, &content); err != nil {
			return nil, err
		}
		out = append(out, store.CodeIndexEmbedTarget{
			ChunkID:   id,
			Path:      path,
			EmbedText: codeIndexChunkEmbedText(path, symbolName, content),
		})
	}
	return out, rows.Err()
}

// CountCodeIndexEmbeddingProgress returns pending/total chunk rows for a workspace.
func (d *DB) CountCodeIndexEmbeddingProgress(
	ctx context.Context, workspaceID, embedModel string, embedVersion int,
) (pending, total int, err error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(embedModel) == "" {
		return 0, 0, fmt.Errorf("CountCodeIndexEmbeddingProgress: workspace_id and embed_model required")
	}
	row := d.q.QueryRowContext(ctx, `
		SELECT
		  COALESCE(SUM(CASE WHEN `+codeIndexChunkStaleEmbedSQL+` THEN 1 ELSE 0 END), 0),
		  COUNT(*)
		FROM code_index_chunks
		WHERE workspace_id = ?`, embedModel, embedVersion, workspaceID)
	if err := row.Scan(&pending, &total); err != nil {
		return 0, 0, fmt.Errorf("count code index embedding progress: %w", err)
	}
	return pending, total, nil
}

// UpsertCodeIndexChunkEmbeddings atomically replaces vectors for a batch of chunks.
func (d *DB) UpsertCodeIndexChunkEmbeddings(
	ctx context.Context, workspaceID, embedModel string, embedVersion int,
	rows []store.CodeIndexChunkEmbedding,
) error {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(embedModel) == "" {
		return fmt.Errorf("UpsertCodeIndexChunkEmbeddings: workspace_id and embed_model required")
	}
	if len(rows) == 0 {
		return nil
	}
	for i, row := range rows {
		if row.ChunkID <= 0 {
			return fmt.Errorf("UpsertCodeIndexChunkEmbeddings: row %d: chunk_id required", i)
		}
		if len(row.Vector) != memoryVecDim {
			return fmt.Errorf("UpsertCodeIndexChunkEmbeddings: row %d: vector dim %d, want %d",
				i, len(row.Vector), memoryVecDim)
		}
	}
	return d.withTx(ctx, func(q queryable) error {
		for _, row := range rows {
			var ws string
			err := q.QueryRowContext(ctx, `
				SELECT workspace_id FROM code_index_chunks WHERE id = ?`, row.ChunkID).Scan(&ws)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("UpsertCodeIndexChunkEmbeddings: chunk %d not found", row.ChunkID)
			}
			if err != nil {
				return fmt.Errorf("lookup chunk %d: %w", row.ChunkID, err)
			}
			if ws != workspaceID {
				return fmt.Errorf("UpsertCodeIndexChunkEmbeddings: chunk %d workspace mismatch", row.ChunkID)
			}
			if _, err := q.ExecContext(ctx,
				`DELETE FROM code_index_chunks_vec WHERE chunk_id = ?`, row.ChunkID); err != nil {
				return fmt.Errorf("delete prior vector for chunk %d: %w", row.ChunkID, err)
			}
			if _, err := q.ExecContext(ctx, `
				INSERT INTO code_index_chunks_vec(chunk_id, embedding) VALUES (?, ?)`,
				row.ChunkID, vectorToJSON(row.Vector)); err != nil {
				return fmt.Errorf("insert vector for chunk %d: %w", row.ChunkID, err)
			}
			res, err := q.ExecContext(ctx, `
				UPDATE code_index_chunks
				SET embed_model = ?, embed_version = ?
				WHERE id = ? AND workspace_id = ?`,
				embedModel, embedVersion, row.ChunkID, workspaceID)
			if err != nil {
				return fmt.Errorf("stamp embedding meta for chunk %d: %w", row.ChunkID, err)
			}
			if err := checkRowsAffected(res); err != nil {
				return fmt.Errorf("stamp embedding meta for chunk %d: %w", row.ChunkID, err)
			}
		}
		return nil
	})
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

// CountCodeIndexSymbols returns the total symbol rows for a workspace.
func (d *DB) CountCodeIndexSymbols(ctx context.Context, workspaceID string) (int, error) {
	var n int
	err := d.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM code_index_symbols WHERE workspace_id = ?`, workspaceID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count code index symbols: %w", err)
	}
	return n, nil
}

// CountCodeIndexChunks returns the total chunk rows for a workspace.
func (d *DB) CountCodeIndexChunks(ctx context.Context, workspaceID string) (int, error) {
	var n int
	err := d.q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM code_index_chunks WHERE workspace_id = ?`, workspaceID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count code index chunks: %w", err)
	}
	return n, nil
}

// GetCodeIndexBuild returns the workspace build row.
func (d *DB) GetCodeIndexBuild(
	ctx context.Context, workspaceID string,
) (*store.CodeIndexBuild, error) {
	row := d.q.QueryRowContext(ctx, `SELECT `+codeIndexBuildCols+`
		FROM code_index_builds WHERE workspace_id = ?`, workspaceID)
	return scanCodeIndexBuild(row)
}

const codeIndexChunkStaleEmbedSQL = `
	embed_model = '' OR embed_model != ? OR embed_version != ?`

func codeIndexLimit(limit, def, max int) int {
	if limit <= 0 {
		return def
	}
	if limit > max {
		return max
	}
	return limit
}

func codeIndexChunkEmbedText(path, symbolName, content string) string {
	var b strings.Builder
	b.WriteString(path)
	if symbolName != "" {
		b.WriteString("\n")
		b.WriteString(symbolName)
	}
	if content != "" {
		b.WriteString("\n")
		b.WriteString(content)
	}
	return b.String()
}

func codeIndexChunkSource(path, symbolName, content string) string {
	return codeIndexChunkEmbedText(path, symbolName, content)
}

func scanCodeIndexFile(r scanner) (*store.CodeIndexFile, error) {
	var f store.CodeIndexFile
	var isTest int
	var indexedAt string
	err := r.Scan(&f.ID, &f.WorkspaceID, &f.Path, &f.PathTokens, &f.Language,
		&f.Package, &f.SizeBytes, &f.LineCount, &f.MtimeUnix, &f.ContentHash,
		&f.DocSummary, &isTest, &f.SkippedReason, &f.ChunkVersion, &indexedAt)
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

func scanCodeIndexChunk(r scanner) (*store.CodeIndexChunk, error) {
	var c store.CodeIndexChunk
	var indexedAt string
	err := r.Scan(&c.ID, &c.WorkspaceID, &c.FileID, &c.Path, &c.PathTokens, &c.Ordinal,
		&c.Kind, &c.SymbolName, &c.SymbolTokens, &c.CodeTokens, &c.StartLine, &c.EndLine,
		&c.Content, &c.ContentHash, &c.EmbedModel, &c.EmbedVersion, &indexedAt)
	if err != nil {
		return nil, err
	}
	c.IndexedAt = parseTime(indexedAt)
	return &c, nil
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

func scanCodeIndexChunkHits(rows *sql.Rows) ([]store.CodeIndexChunkHit, error) {
	var out []store.CodeIndexChunkHit
	for rows.Next() {
		hit, err := scanCodeIndexChunkHitFTS(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *hit)
	}
	return out, rows.Err()
}

func scanCodeIndexChunkHitFTS(rows *sql.Rows) (*store.CodeIndexChunkHit, error) {
	var hit store.CodeIndexChunkHit
	var indexedAt string
	if err := rows.Scan(
		&hit.Chunk.ID, &hit.Chunk.WorkspaceID, &hit.Chunk.FileID, &hit.Chunk.Path,
		&hit.Chunk.PathTokens, &hit.Chunk.Ordinal, &hit.Chunk.Kind, &hit.Chunk.SymbolName,
		&hit.Chunk.SymbolTokens, &hit.Chunk.CodeTokens, &hit.Chunk.StartLine, &hit.Chunk.EndLine,
		&hit.Chunk.Content, &hit.Chunk.ContentHash, &hit.Chunk.EmbedModel, &hit.Chunk.EmbedVersion,
		&indexedAt, &hit.Score); err != nil {
		return nil, err
	}
	hit.Chunk.IndexedAt = parseTime(indexedAt)
	hit.Path = hit.Chunk.Path
	hit.Source = codeIndexChunkSource(hit.Chunk.Path, hit.Chunk.SymbolName, hit.Chunk.Content)
	return &hit, nil
}

func scanCodeIndexChunkVectorHit(rows *sql.Rows) (*store.CodeIndexChunkHit, error) {
	var hit store.CodeIndexChunkHit
	var indexedAt string
	var dist float64
	if err := rows.Scan(
		&hit.Chunk.ID, &hit.Chunk.WorkspaceID, &hit.Chunk.FileID, &hit.Chunk.Path,
		&hit.Chunk.PathTokens, &hit.Chunk.Ordinal, &hit.Chunk.Kind, &hit.Chunk.SymbolName,
		&hit.Chunk.SymbolTokens, &hit.Chunk.CodeTokens, &hit.Chunk.StartLine, &hit.Chunk.EndLine,
		&hit.Chunk.Content, &hit.Chunk.ContentHash, &hit.Chunk.EmbedModel, &hit.Chunk.EmbedVersion,
		&indexedAt, &dist); err != nil {
		return nil, err
	}
	hit.Chunk.IndexedAt = parseTime(indexedAt)
	hit.Path = hit.Chunk.Path
	hit.Score = dist
	hit.Source = codeIndexChunkSource(hit.Chunk.Path, hit.Chunk.SymbolName, hit.Chunk.Content)
	return &hit, nil
}

func scanCodeIndexFileHit(r scanner) (*store.CodeIndexFileHit, error) {
	var hit store.CodeIndexFileHit
	var isTest int
	var indexedAt string
	err := r.Scan(
		&hit.File.ID, &hit.File.WorkspaceID, &hit.File.Path, &hit.File.PathTokens,
		&hit.File.Language, &hit.File.Package, &hit.File.SizeBytes, &hit.File.LineCount,
		&hit.File.MtimeUnix, &hit.File.ContentHash, &hit.File.DocSummary,
		&isTest, &hit.File.SkippedReason, &hit.File.ChunkVersion, &indexedAt, &hit.Score)
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
		&builtAt, &b.DurationMS, &b.FileCount, &b.SymbolCount, &b.ChunkCount, &b.WarningsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	b.BuiltAt = parseTime(builtAt)
	return &b, nil
}
