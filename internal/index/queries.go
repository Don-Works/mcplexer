package index

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// Symbols searches the symbol map, projecting store hits into wire SymbolHits.
func (s *Service) Symbols(ctx context.Context, req SymbolsRequest) ([]SymbolHit, error) {
	if err := s.ensureBuilt(ctx, req.WorkspaceID, req.Root); err != nil {
		return nil, err
	}
	req.WorkspaceID = indexIDForRoot(req.Root)
	hits, err := s.store.SearchCodeIndexSymbols(ctx, store.CodeIndexSymbolQuery{
		WorkspaceID:  req.WorkspaceID,
		Query:        req.Query,
		Kind:         req.Kind,
		ExportedOnly: req.ExportedOnly,
		Limit:        clampLimit(req.Limit, 20, 100),
	})
	if err != nil {
		return nil, err
	}
	out := make([]SymbolHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, toSymbolHit(h.Symbol, h.Path, h.Score))
	}
	return out, nil
}

// Deps walks the file-level import graph in the requested direction(s).
func (s *Service) Deps(ctx context.Context, req DepsRequest) (*DepsResult, error) {
	if err := s.ensureBuilt(ctx, req.WorkspaceID, req.Root); err != nil {
		return nil, err
	}
	req.WorkspaceID = indexIDForRoot(req.Root)
	dir := req.Direction
	if dir == "" {
		dir = "imports"
	}
	if dir != "imports" && dir != "importers" && dir != "both" {
		return nil, fmt.Errorf("index: unknown direction %q (want imports, importers, or both)", req.Direction)
	}
	limit := clampLimit(req.Limit, 50, 500)
	// Both lists stay non-nil so single-direction responses marshal the unused
	// direction as [] rather than null.
	res := &DepsResult{File: req.File, Imports: []DepEntry{}, Importers: []DepEntry{}}
	if dir == "imports" || dir == "both" {
		edges, err := s.store.ListCodeIndexEdges(ctx, store.CodeIndexEdgeFilter{
			WorkspaceID: req.WorkspaceID, FromPath: req.File, Limit: limit,
		})
		if err != nil {
			return nil, err
		}
		res.Imports = importEntries(edges)
	}
	if dir == "importers" || dir == "both" {
		edges, err := s.importerEdges(ctx, req.WorkspaceID, req.File, limit)
		if err != nil {
			return nil, err
		}
		res.Importers = importerEntries(edges)
	}
	return res, nil
}

// importerEdges finds edges targeting the file directly (TS resolves imports
// to files) and, for Go, edges targeting the file's package directory —
// Go import edges point at package dirs, never individual files.
func (s *Service) importerEdges(ctx context.Context, workspaceID, file string, limit int) ([]store.CodeIndexEdgeHit, error) {
	edges, err := s.store.ListCodeIndexEdges(ctx, store.CodeIndexEdgeFilter{
		WorkspaceID: workspaceID, ToPath: file, Limit: limit,
	})
	if err != nil {
		return nil, err
	}
	pkgDir := path.Dir(file)
	if !strings.HasSuffix(file, ".go") || pkgDir == "." || len(edges) >= limit {
		return edges, nil
	}
	dirEdges, err := s.store.ListCodeIndexEdges(ctx, store.CodeIndexEdgeFilter{
		WorkspaceID: workspaceID, ToPath: pkgDir, Limit: limit - len(edges),
	})
	if err != nil {
		return nil, err
	}
	return append(edges, dirEdges...), nil
}

// importEntries projects imports-of edges: the endpoint is the imported file
// (ToPath) or, when external, the raw module specifier.
func importEntries(edges []store.CodeIndexEdgeHit) []DepEntry {
	out := make([]DepEntry, 0, len(edges))
	for _, e := range edges {
		out = append(out, DepEntry{Path: e.ToPath, External: e.ToPath == "", Module: e.ToModule})
	}
	return out
}

// importerEntries projects importers-of edges: the endpoint is the importing
// file (FromPath), always internal.
func importerEntries(edges []store.CodeIndexEdgeHit) []DepEntry {
	out := make([]DepEntry, 0, len(edges))
	for _, e := range edges {
		out = append(out, DepEntry{Path: e.FromPath})
	}
	return out
}

// TestsFor returns the tests that own a source file (§7.4 heuristics).
func (s *Service) TestsFor(ctx context.Context, workspaceID, root, file string) (*TestsForResult, error) {
	if err := s.ensureBuilt(ctx, workspaceID, root); err != nil {
		return nil, err
	}
	workspaceID = indexIDForRoot(root)
	filePaths, err := s.filePathSet(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	owners := ownerTests(ctx, s.store, workspaceID, file, filePaths)
	if owners == nil {
		owners = []TestOwner{}
	}
	return &TestsForResult{File: file, Tests: owners}, nil
}

// Summary assembles the heuristic one-file orientation card.
func (s *Service) Summary(ctx context.Context, workspaceID, root, file string) (*FileSummary, error) {
	if err := s.ensureBuilt(ctx, workspaceID, root); err != nil {
		return nil, err
	}
	workspaceID = indexIDForRoot(root)
	f, err := s.store.GetCodeIndexFile(ctx, workspaceID, file)
	if err != nil {
		return nil, err
	}
	syms, err := s.store.ListCodeIndexSymbolsByPath(ctx, workspaceID, file)
	if err != nil {
		return nil, err
	}
	sum := &FileSummary{
		Path: f.Path, Language: f.Language, Package: f.Package, DocSummary: f.DocSummary,
		LineCount: f.LineCount, SizeBytes: f.SizeBytes, IsTest: f.IsTest,
	}
	for _, sym := range syms {
		if sym.Exported {
			sum.ExportedSymbols = append(sum.ExportedSymbols, toSymbolHit(sym, file, 0))
		}
	}
	sum.ImportCount = s.edgeCount(ctx, store.CodeIndexEdgeFilter{WorkspaceID: workspaceID, FromPath: file, Limit: 1000})
	importers, _ := s.importerEdges(ctx, workspaceID, file, 1000)
	sum.ImporterCount = len(importers)
	filePaths, _ := s.filePathSet(ctx, workspaceID)
	for _, o := range ownerTests(ctx, s.store, workspaceID, file, filePaths) {
		sum.Tests = append(sum.Tests, o.Path)
	}
	return sum, nil
}

// edgeCount returns how many edges match a filter (0 on error).
func (s *Service) edgeCount(ctx context.Context, f store.CodeIndexEdgeFilter) int {
	edges, err := s.store.ListCodeIndexEdges(ctx, f)
	if err != nil {
		return 0
	}
	return len(edges)
}

// RecentChanges reads git log for commits + per-file churn (live; no build).
func (s *Service) RecentChanges(ctx context.Context, req RecentChangesRequest) (*RecentChangesResult, error) {
	if err := validateRoot(req.Root); err != nil {
		return nil, err
	}
	if err := requireDir(req.Root); err != nil {
		return nil, err
	}
	days := clampLimit(req.Days, 14, 3650)
	limit := clampLimit(req.Limit, 20, 100)
	git := newGitRunner(req.Root, s.logger)
	res := &RecentChangesResult{Commits: []CommitRef{}, ChurnByFile: map[string]int{}}
	if commits, err := git.recentChanges(ctx, req.Path, days, limit); err == nil {
		res.Commits = commits
	}
	churn, _ := git.churnCounts(ctx, days)
	res.ChurnByFile = filterChurn(churn, req.Path)
	return res, nil
}

// filterChurn restricts churn counts to entries under path (all when empty).
func filterChurn(churn map[string]int, path string) map[string]int {
	if path == "" {
		return churn
	}
	out := map[string]int{}
	for p, n := range churn {
		if p == path || strings.HasPrefix(p, strings.TrimSuffix(path, "/")+"/") {
			out[p] = n
		}
	}
	return out
}

// MapFailure ranks candidate files for a pasted failure (§7.5).
func (s *Service) MapFailure(ctx context.Context, workspaceID, root, text string, limit int) ([]FailureCandidate, error) {
	if err := s.ensureBuilt(ctx, workspaceID, root); err != nil {
		return nil, err
	}
	workspaceID = indexIDForRoot(root)
	return s.mapFailure(ctx, workspaceID, root, text, clampLimit(limit, 10, 100))
}

// ContextPack returns the token-budgeted context pack, auto-refreshing when
// tracked source state changes and including ranked citation-ready snippets.
func (s *Service) ContextPack(ctx context.Context, req ContextRequest) (*ContextPack, error) {
	if err := s.ensureBuilt(ctx, req.WorkspaceID, req.Root); err != nil {
		return nil, err
	}
	req.WorkspaceID = indexIDForRoot(req.Root)
	s.startEmbeddingBackfill(req.WorkspaceID)
	req.BudgetTokens = clampLimit(req.BudgetTokens, 4000, 16000)
	build, err := s.store.GetCodeIndexBuild(ctx, req.WorkspaceID)
	if err != nil {
		return nil, err
	}
	git := newGitRunner(req.Root, s.logger)
	stale := contextStale(ctx, git, build, s.store, req.WorkspaceID, req.Root)
	if stale {
		if _, berr := s.Build(ctx, BuildRequest{WorkspaceID: req.WorkspaceID, Root: req.Root}); berr == nil {
			if refreshed, gerr := s.store.GetCodeIndexBuild(ctx, req.WorkspaceID); gerr == nil {
				build = refreshed
			}
			stale = false
		}
	}
	pack, err := s.contextPack(ctx, req, git, build.BuiltAt)
	if err != nil {
		return nil, err
	}
	pack.Stale = stale
	pack.Embeddings = s.embeddingStatus(ctx, req.WorkspaceID)
	if pack.Files == nil {
		pack.Files = []ContextFile{}
	}
	return pack, nil
}

// Status reports the freshness verdict. A never-built workspace returns
// ErrNotBuilt (the handler surfaces a "run index__build" hint).
func (s *Service) Status(ctx context.Context, workspaceID, root string) (*Status, error) {
	if err := validateRoot(root); err != nil {
		return nil, err
	}
	if err := requireDir(root); err != nil {
		return nil, err
	}
	workspaceID = indexIDForRoot(root)
	build, err := s.store.GetCodeIndexBuild(ctx, workspaceID)
	if err != nil {
		if isNotFound(err) {
			return nil, ErrNotBuilt
		}
		return nil, err
	}
	git := newGitRunner(root, s.logger)
	s.startEmbeddingBackfill(workspaceID)
	head, _ := git.head(ctx)
	dirty, _ := git.dirtyCount(ctx)
	return &Status{
		IndexID:     workspaceID,
		Built:       true,
		BuiltAt:     build.BuiltAt,
		GitHead:     build.GitHead,
		CurrentHead: head,
		Stale:       contextStale(ctx, git, build, s.store, workspaceID, root),
		DirtyFiles:  dirty,
		FileCount:   build.FileCount,
		SymbolCount: build.SymbolCount,
		ChunkCount:  build.ChunkCount,
		DurationMS:  build.DurationMS,
		Warnings:    parseWarnings(build.WarningsJSON),
		Embeddings:  s.embeddingStatus(ctx, workspaceID),
	}, nil
}
