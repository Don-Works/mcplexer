package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	maxFiles       = 50000
	maxFileBytes   = 1 << 20 // 1 MiB
	batchSize      = 100
	wallGuard      = 120 * time.Second
	mtimeGraceSecs = 2
	maxWarnings    = 50
)

// buildRun carries the mutable state of one incremental build.
type buildRun struct {
	svc         *Service
	req         BuildRequest
	git         *gitRunner
	existing    map[string]store.CodeIndexFileStat
	storedPaths map[string]bool
	enumSet     map[string]bool
	goMod       string
	lastIndexed int64
	batch       []store.IndexedFile
	res         *BuildResult
	symbolTotal int
	chunkTotal  int
	incomplete  bool
	tsImports   int
	tsAliases   int
	deadline    time.Time
}

// runBuild executes the incremental build described in plan §7.1 (as amended by
// P3): enumerate, diff against stored stats, extract changed files, batch-write,
// prune removals, and persist the build row.
func (s *Service) runBuild(ctx context.Context, req BuildRequest) (*BuildResult, error) {
	br := &buildRun{
		svc: s, req: req, git: newGitRunner(req.Root, s.logger),
		res: &BuildResult{}, deadline: time.Now().Add(wallGuard),
		enumSet: map[string]bool{},
	}
	br.res.IndexID = req.WorkspaceID
	start := time.Now()
	files, _, err := enumerate(ctx, req.Root, br.git, req.Paths)
	if err != nil {
		return nil, fmt.Errorf("index: enumerate %s: %w", req.Root, err)
	}
	if len(files) > maxFiles {
		return nil, fmt.Errorf("index: %d files exceeds cap %d — pass paths to narrow the build", len(files), maxFiles)
	}
	if err := br.loadState(ctx); err != nil {
		return nil, err
	}
	br.goMod = goModule(req.Root)
	for _, rel := range files {
		br.enumSet[rel] = true
	}
	if err := br.processAll(ctx, files); err != nil {
		return nil, err
	}
	br.pruneRemoved(ctx)
	return br.finish(ctx, start)
}

// loadState loads the prior build row (for the mtime grace window + running
// totals) and per-file freshness stats. Force still retains those stats so
// replacement can subtract old symbol/chunk counts before re-extracting.
func (br *buildRun) loadState(ctx context.Context) error {
	if prev, err := br.svc.store.GetCodeIndexBuild(ctx, br.req.WorkspaceID); err == nil {
		br.lastIndexed = prev.BuiltAt.Unix()
		br.symbolTotal = prev.SymbolCount
	}
	stats, err := br.svc.store.ListCodeIndexFileStats(ctx, br.req.WorkspaceID)
	if err != nil {
		return fmt.Errorf("index: load file stats: %w", err)
	}
	br.existing = make(map[string]store.CodeIndexFileStat, len(stats))
	br.storedPaths = make(map[string]bool, len(stats))
	for _, st := range stats {
		br.storedPaths[st.Path] = true
		br.chunkTotal += st.ChunkCount
		br.existing[st.Path] = st
	}
	return nil
}

// processAll walks the enumerated files, honoring the ctx deadline and the
// 120s wall guard (partial persist + warning on exceed).
func (br *buildRun) processAll(ctx context.Context, files []string) error {
	for _, rel := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(br.deadline) {
			br.incomplete = true
			br.warn("build hit the 120s wall guard; index is partial — rerun index__build")
			break
		}
		if err := br.processFile(ctx, rel); err != nil {
			return err
		}
		if len(br.batch) >= batchSize {
			if err := br.flush(ctx); err != nil {
				return err
			}
		}
	}
	return br.flush(ctx)
}

// processFile classifies one file as unchanged, skipped (too large / binary),
// or (re)indexed, updating counters and the write batch accordingly.
func (br *buildRun) processFile(ctx context.Context, rel string) error {
	if !ShouldIndexPath(rel) {
		return nil
	}
	info, err := os.Lstat(filepath.Join(br.req.Root, rel))
	if err != nil {
		br.incomplete = true
		br.warn(fmt.Sprintf("%s: file vanished or could not be stated during build", rel))
		return nil
	}
	if !info.Mode().IsRegular() {
		return nil // symlink/non-regular entries are intentionally never indexed
	}
	size, mtime := int(info.Size()), info.ModTime().Unix()
	prev, known := br.existing[rel]
	needsRechunk := known && prev.ChunkVersion != chunkSchemaVersion
	if known && !br.req.Force && !needsRechunk && prev.SizeBytes == size && prev.MtimeUnix == mtime && prev.MtimeUnix <= br.lastIndexed-mtimeGraceSecs {
		br.res.FilesUnchanged++
		return nil
	}
	if size > maxFileBytes {
		br.addSkipped(ctx, rel, size, mtime, "", "file exceeds 1 MiB")
		return nil
	}
	data, err := os.ReadFile(filepath.Join(br.req.Root, rel))
	if err != nil {
		br.incomplete = true
		br.warn(fmt.Sprintf("%s: could not read source during build", rel))
		return nil
	}
	if sniffBinary(data) {
		br.addSkipped(ctx, rel, size, mtime, "", "binary file")
		return nil
	}
	if likelyGeneratedSource(data) {
		br.addSkipped(ctx, rel, size, mtime, hashBytes(data), "generated or minified source")
		return nil
	}
	hash := hashBytes(data)
	if known && !br.req.Force && !needsRechunk && prev.ContentHash == hash {
		br.res.FilesUnchanged++ // content identical; leave the stored row as-is
		return nil
	}
	br.addIndexed(ctx, rel, size, mtime, hash, data, known)
	return nil
}

// addIndexed extracts a changed file and appends its full replacement payload
// to the batch, maintaining the running symbol total.
func (br *buildRun) addIndexed(ctx context.Context, rel string, size int, mtime int64, hash string, data []byte, known bool) {
	ex := extractFile(rel, data)
	if ex.ParseError != "" {
		br.warn(fmt.Sprintf("%s: parse error (%s)", rel, firstLine(ex.ParseError)))
	}
	if known {
		br.symbolTotal -= br.oldSymbolCount(ctx, rel)
		br.chunkTotal -= br.existing[rel].ChunkCount
	}
	br.symbolTotal += len(ex.Symbols)
	br.res.FilesIndexed++
	br.res.SymbolCount += len(ex.Symbols)
	assembled := br.assemble(rel, size, mtime, hash, data, ex)
	remaining := maxWorkspaceChunks - br.chunkTotal
	var workspaceTruncated bool
	assembled.Chunks, workspaceTruncated = fitWorkspaceChunkBudget(assembled.Chunks, remaining)
	if workspaceTruncated {
		// Version 0 is deliberately stale: if another file is later removed,
		// an ordinary incremental build retries this file and fills the newly
		// available capacity instead of leaving a permanent search blind spot.
		assembled.File.ChunkVersion = 0
		br.warn(fmt.Sprintf("%s: source chunks truncated at workspace cap %d", rel, maxWorkspaceChunks))
	}
	br.chunkTotal += len(assembled.Chunks)
	br.batch = append(br.batch, assembled)
}

func fitWorkspaceChunkBudget(chunks []store.CodeIndexChunk, remaining int) ([]store.CodeIndexChunk, bool) {
	if remaining < 0 {
		remaining = 0
	}
	if len(chunks) <= remaining {
		return chunks, false
	}
	return chunks[:remaining], true
}

// addSkipped records a file row for a too-large or binary file with no parse.
func (br *buildRun) addSkipped(ctx context.Context, rel string, size int, mtime int64, hash, reason string) {
	if known, ok := br.existing[rel]; ok && known.ContentHash != "" {
		br.symbolTotal -= br.oldSymbolCount(ctx, rel)
		br.chunkTotal -= known.ChunkCount
	}
	br.res.FilesSkipped++
	f := store.CodeIndexFile{
		WorkspaceID: br.req.WorkspaceID, Path: rel, PathTokens: tokenString(rel),
		SizeBytes: size, MtimeUnix: mtime, ContentHash: hash, IsTest: isTestPath(rel),
		SkippedReason: reason, ChunkVersion: chunkSchemaVersion, IndexedAt: time.Now().UTC(),
	}
	br.batch = append(br.batch, store.IndexedFile{File: f})
}

// assemble folds an Extraction into a store.IndexedFile: file row + tokenized
// symbols + resolved import edges.
func (br *buildRun) assemble(rel string, size int, mtime int64, hash string, data []byte, ex *Extraction) store.IndexedFile {
	f := store.CodeIndexFile{
		WorkspaceID: br.req.WorkspaceID, Path: rel, PathTokens: tokenString(rel),
		Language: ex.Language, Package: ex.Package, SizeBytes: size, LineCount: ex.LineCount,
		MtimeUnix: mtime, ContentHash: hash, DocSummary: ex.DocSummary,
		IsTest: isTestPath(rel), ChunkVersion: chunkSchemaVersion, IndexedAt: time.Now().UTC(),
	}
	syms := make([]store.CodeIndexSymbol, 0, len(ex.Symbols))
	for _, s := range ex.Symbols {
		s.WorkspaceID = br.req.WorkspaceID
		s.NameTokens = tokenString(s.Name)
		syms = append(syms, s)
	}
	chunks, truncated := chunkSource(rel, data, syms)
	if truncated {
		br.warn(fmt.Sprintf("%s: source chunks truncated at per-file cap %d", rel, maxChunksPerFile))
	}
	return store.IndexedFile{File: f, Symbols: syms, Edges: br.edges(rel, ex), Chunks: chunks}
}

// edges resolves an extraction's import specifiers into store edges, tallying
// TS path-alias coverage for the P9 warning.
func (br *buildRun) edges(rel string, ex *Extraction) []store.CodeIndexEdge {
	out := make([]store.CodeIndexEdge, 0, len(ex.Imports))
	for _, spec := range ex.Imports {
		if ex.Language == "go" {
			out = append(out, resolveGoImport(br.goMod, spec, br.req.WorkspaceID))
			continue
		}
		br.tsImports++
		te := resolveTSImport(rel, spec, br.enumSet, br.req.WorkspaceID)
		if te.alias {
			br.tsAliases++
		}
		out = append(out, te.edge)
	}
	return out
}

// oldSymbolCount returns how many symbols the stored version of rel had, so the
// running total stays accurate across re-indexes (bounded to changed files).
func (br *buildRun) oldSymbolCount(ctx context.Context, rel string) int {
	syms, err := br.svc.store.ListCodeIndexSymbolsByPath(ctx, br.req.WorkspaceID, rel)
	if err != nil {
		return 0
	}
	return len(syms)
}

// pruneRemoved deletes rows for stored files that were not re-enumerated.
// A scoped build (req.Paths non-empty) only enumerated in-scope files, so
// out-of-scope rows are never prune candidates.
func (br *buildRun) pruneRemoved(ctx context.Context) {
	var gone []string
	for p := range br.storedPaths {
		if !matchesPrefixes(p, br.req.Paths) {
			continue
		}
		if !br.enumSet[p] {
			gone = append(gone, p)
			br.symbolTotal -= br.oldSymbolCount(ctx, p)
			br.chunkTotal -= br.existing[p].ChunkCount
		}
	}
	if len(gone) == 0 {
		return
	}
	if err := br.svc.store.DeleteCodeIndexFiles(ctx, br.req.WorkspaceID, gone); err != nil {
		br.incomplete = true
		br.warn("failed to prune removed files: " + err.Error())
		return
	}
	br.res.FilesRemoved = len(gone)
}

// flush writes and clears the pending batch.
func (br *buildRun) flush(ctx context.Context) error {
	if len(br.batch) == 0 {
		return nil
	}
	if err := br.svc.store.UpsertCodeIndexedFiles(ctx, br.req.WorkspaceID, br.batch); err != nil {
		return fmt.Errorf("index: upsert batch: %w", err)
	}
	br.batch = br.batch[:0]
	return nil
}

// finish records the build row and returns the result.
func (br *buildRun) finish(ctx context.Context, start time.Time) (*BuildResult, error) {
	if br.tsImports > 0 && br.tsAliases*100 > br.tsImports*30 {
		br.warn("over 30% of TS imports are unresolved path aliases (@/…); alias resolution is a v2 gap")
	}
	head, _ := br.git.head(ctx)
	dirty, _ := br.git.dirtyCount(ctx)
	if br.symbolTotal < 0 {
		br.symbolTotal = 0
	}
	br.res.DurationMS = int(time.Since(start).Milliseconds())
	br.res.GitHead = head
	fileCount := len(br.enumSet)
	symbolCount := br.symbolTotal
	if len(br.req.Paths) > 0 {
		// A scoped build only enumerated part of the workspace; the build row
		// must still report whole-index totals.
		if stats, err := br.svc.store.ListCodeIndexFileStats(ctx, br.req.WorkspaceID); err == nil {
			fileCount = len(stats)
		}
		if n, err := br.svc.store.CountCodeIndexSymbols(ctx, br.req.WorkspaceID); err == nil {
			symbolCount = n
		}
	}
	build := &store.CodeIndexBuild{
		WorkspaceID: br.req.WorkspaceID, RootPath: br.req.Root, GitHead: head, DirtyCount: dirty,
		BuiltAt: time.Now().UTC(), DurationMS: br.res.DurationMS,
		FileCount: fileCount, SymbolCount: symbolCount, ChunkCount: br.chunkTotal,
		Complete:     !br.incomplete,
		WarningsJSON: warningsJSON(br.res.Warnings),
	}
	if n, err := br.svc.store.CountCodeIndexChunks(ctx, br.req.WorkspaceID); err == nil {
		build.ChunkCount = n
		br.res.ChunkCount = n
	}
	if err := br.svc.store.PutCodeIndexBuild(ctx, build); err != nil {
		return nil, fmt.Errorf("index: put build row: %w", err)
	}
	br.res.Complete = build.Complete
	return br.res, nil
}

// warn appends a build warning, capped so a pathological repo can't bloat the
// row.
func (br *buildRun) warn(msg string) {
	if len(br.res.Warnings) < maxWarnings {
		br.res.Warnings = append(br.res.Warnings, msg)
	}
}
