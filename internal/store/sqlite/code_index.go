package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const codeIndexFileCols = `id, workspace_id, path, path_tokens, language, package,
	size_bytes, line_count, mtime_unix, content_hash, doc_summary,
	is_test, skipped_reason, chunk_version, indexed_at`

func codeIndexSymbolColsPrefixed(alias string) string {
	return alias + `.id, ` + alias + `.file_id, ` + alias + `.workspace_id, ` +
		alias + `.name, ` + alias + `.name_tokens, ` + alias + `.kind, ` +
		alias + `.receiver, ` + alias + `.signature, ` + alias + `.doc, ` +
		alias + `.start_line, ` + alias + `.end_line, ` + alias + `.exported`
}

func codeIndexChunkColsPrefixed(alias string) string {
	return alias + `.id, ` + alias + `.workspace_id, ` + alias + `.file_id, ` +
		alias + `.path, ` + alias + `.path_tokens, ` + alias + `.ordinal, ` +
		alias + `.kind, ` + alias + `.symbol_name, ` + alias + `.symbol_tokens, ` +
		alias + `.code_tokens, ` + alias + `.start_line, ` + alias + `.end_line, ` +
		alias + `.content, ` + alias + `.content_hash, ` + alias + `.embed_model, ` +
		alias + `.embed_version, ` + alias + `.indexed_at`
}

const codeIndexBuildCols = `workspace_id, root_path, git_head, dirty_count,
	built_at, duration_ms, file_count, symbol_count, chunk_count, warnings_json`

// UpsertCodeIndexedFiles upserts each file (preserving row id on conflict) and
// fully replaces its symbols, edges, and chunks inside one transaction.
func (d *DB) UpsertCodeIndexedFiles(
	ctx context.Context, workspaceID string, files []store.IndexedFile,
) error {
	if len(files) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("upsert code index files: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	q := &DB{db: d.db, q: tx}
	for _, f := range files {
		if err := q.upsertOneCodeIndexFile(ctx, workspaceID, f); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) upsertOneCodeIndexFile(
	ctx context.Context, workspaceID string, f store.IndexedFile,
) error {
	fileID, err := d.upsertCodeIndexFileRow(ctx, workspaceID, f.File)
	if err != nil {
		return err
	}
	if err := d.replaceCodeIndexFileChildren(ctx, workspaceID, fileID, f); err != nil {
		return fmt.Errorf("replace children for %q: %w", f.File.Path, err)
	}
	return nil
}

func (d *DB) upsertCodeIndexFileRow(
	ctx context.Context, workspaceID string, file store.CodeIndexFile,
) (int64, error) {
	if file.IndexedAt.IsZero() {
		file.IndexedAt = time.Now().UTC()
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO code_index_files (
			workspace_id, path, path_tokens, language, package,
			size_bytes, line_count, mtime_unix, content_hash, doc_summary,
			is_test, skipped_reason, chunk_version, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id, path) DO UPDATE SET
			path_tokens = excluded.path_tokens,
			language = excluded.language,
			package = excluded.package,
			size_bytes = excluded.size_bytes,
			line_count = excluded.line_count,
			mtime_unix = excluded.mtime_unix,
			content_hash = excluded.content_hash,
			doc_summary = excluded.doc_summary,
			is_test = excluded.is_test,
			skipped_reason = excluded.skipped_reason,
			chunk_version = excluded.chunk_version,
			indexed_at = excluded.indexed_at`,
		workspaceID, file.Path, file.PathTokens, file.Language, file.Package,
		file.SizeBytes, file.LineCount, file.MtimeUnix, file.ContentHash,
		file.DocSummary, boolToInt(file.IsTest), file.SkippedReason,
		file.ChunkVersion, formatTime(file.IndexedAt))
	if err != nil {
		return 0, fmt.Errorf("upsert code index file %q: %w", file.Path, err)
	}
	var fileID int64
	err = d.q.QueryRowContext(ctx, `
		SELECT id FROM code_index_files
		WHERE workspace_id = ? AND path = ?`,
		workspaceID, file.Path).Scan(&fileID)
	if err != nil {
		return 0, fmt.Errorf("resolve code index file id %q: %w", file.Path, err)
	}
	return fileID, nil
}

func (d *DB) replaceCodeIndexFileChildren(
	ctx context.Context, workspaceID string, fileID int64, f store.IndexedFile,
) error {
	if err := d.deleteCodeIndexChunksForFile(ctx, fileID); err != nil {
		return err
	}
	if _, err := d.q.ExecContext(ctx,
		`DELETE FROM code_index_symbols WHERE file_id = ?`, fileID); err != nil {
		return fmt.Errorf("delete symbols: %w", err)
	}
	if _, err := d.q.ExecContext(ctx,
		`DELETE FROM code_index_edges WHERE from_file_id = ?`, fileID); err != nil {
		return fmt.Errorf("delete edges: %w", err)
	}
	for _, sym := range f.Symbols {
		if err := d.insertCodeIndexSymbol(ctx, workspaceID, fileID, sym); err != nil {
			return err
		}
	}
	for _, edge := range f.Edges {
		if err := d.insertCodeIndexEdge(ctx, workspaceID, fileID, edge); err != nil {
			return err
		}
	}
	for _, chunk := range f.Chunks {
		if err := d.insertCodeIndexChunk(ctx, workspaceID, fileID, f.File.Path, chunk); err != nil {
			return err
		}
	}
	return nil
}

func (d *DB) deleteCodeIndexChunksForFile(ctx context.Context, fileID int64) error {
	rows, err := d.q.QueryContext(ctx,
		`SELECT id FROM code_index_chunks WHERE file_id = ?`, fileID)
	if err != nil {
		return fmt.Errorf("list chunk ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var chunkIDs []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return err
		}
		chunkIDs = append(chunkIDs, id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range chunkIDs {
		if _, err := d.q.ExecContext(ctx,
			`DELETE FROM code_index_chunks_vec WHERE chunk_id = ?`, id); err != nil {
			return fmt.Errorf("delete chunk vec %d: %w", id, err)
		}
	}
	if _, err := d.q.ExecContext(ctx,
		`DELETE FROM code_index_chunks WHERE file_id = ?`, fileID); err != nil {
		return fmt.Errorf("delete chunks: %w", err)
	}
	return nil
}

func (d *DB) insertCodeIndexSymbol(
	ctx context.Context, workspaceID string, fileID int64, sym store.CodeIndexSymbol,
) error {
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO code_index_symbols (
			workspace_id, file_id, name, name_tokens, kind, receiver,
			signature, doc, start_line, end_line, exported)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, fileID, sym.Name, sym.NameTokens, sym.Kind, sym.Receiver,
		sym.Signature, sym.Doc, sym.StartLine, sym.EndLine, boolToInt(sym.Exported))
	if err != nil {
		return fmt.Errorf("insert code index symbol %q: %w", sym.Name, err)
	}
	return nil
}

func (d *DB) insertCodeIndexEdge(
	ctx context.Context, workspaceID string, fileID int64, edge store.CodeIndexEdge,
) error {
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO code_index_edges (
			workspace_id, from_file_id, kind, to_path, to_module)
		VALUES (?, ?, ?, ?, ?)`,
		workspaceID, fileID, edge.Kind, edge.ToPath, edge.ToModule)
	if err != nil {
		return fmt.Errorf("insert code index edge: %w", err)
	}
	return nil
}

func (d *DB) insertCodeIndexChunk(
	ctx context.Context, workspaceID string, fileID int64, path string, chunk store.CodeIndexChunk,
) error {
	if chunk.IndexedAt.IsZero() {
		chunk.IndexedAt = time.Now().UTC()
	}
	if chunk.Path == "" {
		chunk.Path = path
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO code_index_chunks (
			workspace_id, file_id, path, path_tokens, ordinal, kind,
			symbol_name, symbol_tokens, code_tokens, start_line, end_line,
			content, content_hash, embed_model, embed_version, indexed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		workspaceID, fileID, chunk.Path, chunk.PathTokens, chunk.Ordinal, chunk.Kind,
		chunk.SymbolName, chunk.SymbolTokens, chunk.CodeTokens, chunk.StartLine, chunk.EndLine,
		chunk.Content, chunk.ContentHash, chunk.EmbedModel, chunk.EmbedVersion,
		formatTime(chunk.IndexedAt))
	if err != nil {
		return fmt.Errorf("insert code index chunk ord=%d: %w", chunk.Ordinal, err)
	}
	return nil
}

// DeleteCodeIndexFiles removes files and their child symbols/edges/chunks for a workspace.
func (d *DB) DeleteCodeIndexFiles(
	ctx context.Context, workspaceID string, paths []string,
) error {
	if len(paths) == 0 {
		return nil
	}
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("delete code index files: begin tx: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	q := &DB{db: d.db, q: tx}
	for _, path := range paths {
		if err := q.deleteOneCodeIndexFile(ctx, workspaceID, path); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (d *DB) deleteOneCodeIndexFile(ctx context.Context, workspaceID, path string) error {
	var fileID int64
	err := d.q.QueryRowContext(ctx, `
		SELECT id FROM code_index_files
		WHERE workspace_id = ? AND path = ?`, workspaceID, path).Scan(&fileID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup code index file %q: %w", path, err)
	}
	if err := d.deleteCodeIndexChunksForFile(ctx, fileID); err != nil {
		return fmt.Errorf("delete chunks for %q: %w", path, err)
	}
	if _, err := d.q.ExecContext(ctx,
		`DELETE FROM code_index_symbols WHERE file_id = ?`, fileID); err != nil {
		return fmt.Errorf("delete symbols for %q: %w", path, err)
	}
	if _, err := d.q.ExecContext(ctx,
		`DELETE FROM code_index_edges WHERE from_file_id = ?`, fileID); err != nil {
		return fmt.Errorf("delete edges for %q: %w", path, err)
	}
	if _, err := d.q.ExecContext(ctx, `
		DELETE FROM code_index_files WHERE id = ?`, fileID); err != nil {
		return fmt.Errorf("delete file %q: %w", path, err)
	}
	return nil
}

// PutCodeIndexBuild upserts the per-workspace build freshness row.
func (d *DB) PutCodeIndexBuild(ctx context.Context, b *store.CodeIndexBuild) error {
	if b == nil {
		return errors.New("PutCodeIndexBuild: nil build")
	}
	if b.BuiltAt.IsZero() {
		b.BuiltAt = time.Now().UTC()
	}
	if b.WarningsJSON == "" {
		b.WarningsJSON = "[]"
	}
	_, err := d.q.ExecContext(ctx, `
		INSERT INTO code_index_builds (`+codeIndexBuildCols+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(workspace_id) DO UPDATE SET
			root_path = excluded.root_path,
			git_head = excluded.git_head,
			dirty_count = excluded.dirty_count,
			built_at = excluded.built_at,
			duration_ms = excluded.duration_ms,
			file_count = excluded.file_count,
			symbol_count = excluded.symbol_count,
			chunk_count = excluded.chunk_count,
			warnings_json = excluded.warnings_json`,
		b.WorkspaceID, b.RootPath, b.GitHead, b.DirtyCount,
		formatTime(b.BuiltAt), b.DurationMS, b.FileCount, b.SymbolCount,
		b.ChunkCount, b.WarningsJSON)
	if err != nil {
		return fmt.Errorf("put code index build: %w", err)
	}
	return nil
}
