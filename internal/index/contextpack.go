package index

import (
	"context"
	"encoding/json"
	"math"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	ctxSymbolLimit       = 40
	ctxFileLimit         = 20
	ctxAssembleCap       = 15
	ctxChurnDays         = 30
	ctxCommitDays        = 90
	ctxMaxSymbols        = 5
	ctxMaxExported       = 3
	ctxMaxCommits        = 2
	ctxFileFTSCoeff      = 0.3
	ctxChurnCoeff        = 0.15
	ctxGraphCoeff        = 0.3
	ctxChunkCoeff        = 1.15
	ctxChunkLimit        = 40
	ctxMaxSnippets       = 2
	ctxMaxPkgDirFiles    = 8
	ctxMaxGraphNeighbors = 50
)

// ctxCand is an in-progress context-pack candidate before assembly.
type ctxCand struct {
	path          string
	score         float64
	why           []string
	matchedSyms   []store.CodeIndexSymbol
	matchedChunks []CodeSnippet
}

// contextPack ranks files for a query and assembles a token-budgeted pack
// (plan §7.6). git is used for the churn boost + recent commits; build supplies
// BuiltAt.
func (s *Service) contextPack(ctx context.Context, req ContextRequest, git *gitRunner, builtAt time.Time) (*ContextPack, error) {
	cands := map[string]*ctxCand{}
	s.rankBySymbols(ctx, req, cands)
	s.rankByFiles(ctx, req, cands)
	s.rankByChunks(ctx, req, cands)
	applyChurn(ctx, git, cands)
	filePaths, _ := s.filePathSet(ctx, req.WorkspaceID)
	s.applyGraphProximity(ctx, req.WorkspaceID, cands, filePaths)
	ordered := orderCandidates(cands)
	if len(ordered) > ctxAssembleCap {
		ordered = ordered[:ctxAssembleCap]
	}
	pack := &ContextPack{Query: req.Query, BudgetTokens: req.BudgetTokens, BuiltAt: builtAt}
	s.fillBudget(ctx, req, git, ordered, filePaths, pack)
	return pack, nil
}

// rankByChunks is the highest-signal context source: it searches actual code
// slices (lexical, plus semantic when the local backfill is ready), groups
// them by file, and carries bounded citations into the final pack.
func (s *Service) rankByChunks(ctx context.Context, req ContextRequest, cands map[string]*ctxCand) {
	result, err := s.searchChunks(ctx, req.WorkspaceID, req.Query, "", ctxChunkLimit)
	if err != nil {
		return
	}
	maxScore := map[string]float64{}
	count := map[string]int{}
	for _, hit := range result.Hits {
		cand := candFor(cands, hit.Path)
		if len(cand.matchedChunks) < ctxMaxSnippets {
			cand.matchedChunks = append(cand.matchedChunks, hit.CodeSnippet)
		}
		if hit.Score > maxScore[hit.Path] {
			maxScore[hit.Path] = hit.Score
		}
		count[hit.Path]++
	}
	for file, score := range maxScore {
		cand := cands[file]
		cand.score += ctxChunkCoeff*score + 0.05*math.Log(1+float64(count[file]))
		cand.why = append(cand.why, "source code matches query")
	}
}

// rankBySymbols contributes max_symbol_score + 0.1·ln(1+hits) per file.
func (s *Service) rankBySymbols(ctx context.Context, req ContextRequest, cands map[string]*ctxCand) {
	hits, err := s.store.SearchCodeIndexSymbols(ctx, store.CodeIndexSymbolQuery{
		WorkspaceID: req.WorkspaceID, Query: req.Query, Limit: ctxSymbolLimit,
	})
	if err != nil {
		return
	}
	scores := make([]float64, len(hits))
	for i, h := range hits {
		scores[i] = h.Score
	}
	norm := normalizeScores(scores)
	maxSym := map[string]float64{}
	count := map[string]int{}
	for i, h := range hits {
		c := candFor(cands, h.Path)
		c.matchedSyms = append(c.matchedSyms, h.Symbol)
		if norm[i] > maxSym[h.Path] {
			maxSym[h.Path] = norm[i]
		}
		count[h.Path]++
	}
	for p, m := range maxSym {
		c := cands[p]
		c.score += m + 0.1*math.Log(1+float64(count[p]))
		c.why = append(c.why, "matches query symbols")
	}
}

// rankByFiles contributes 0.3·file_fts_score per file.
func (s *Service) rankByFiles(ctx context.Context, req ContextRequest, cands map[string]*ctxCand) {
	hits, err := s.store.SearchCodeIndexFiles(ctx, req.WorkspaceID, req.Query, ctxFileLimit)
	if err != nil {
		return
	}
	scores := make([]float64, len(hits))
	for i, h := range hits {
		scores[i] = h.Score
	}
	norm := normalizeScores(scores)
	for i, h := range hits {
		c := candFor(cands, h.File.Path)
		c.score += ctxFileFTSCoeff * norm[i]
		if norm[i] > 0.5 {
			c.why = append(c.why, "file text matches query")
		}
	}
}

// applyChurn adds a recency boost of 0.15·min(1, commits/5) per file.
func applyChurn(ctx context.Context, git *gitRunner, cands map[string]*ctxCand) {
	churn, _ := git.churnCounts(ctx, ctxChurnDays)
	if len(churn) == 0 {
		return
	}
	for p, c := range cands {
		if n := churn[p]; n > 0 {
			c.score += ctxChurnCoeff * math.Min(1, float64(n)/5)
			c.why = append(c.why, "recently changed")
		}
	}
}

// applyGraphProximity pulls in 1-hop import neighbors of the top-3 files at
// 0.3·parent_score, so directly-related files ride along. Neighbors are
// filtered to indexed files — Go import edges target package DIRECTORIES,
// which must never become pack entries.
func (s *Service) applyGraphProximity(ctx context.Context, ws string, cands map[string]*ctxCand, filePaths map[string]bool) {
	top := orderCandidates(cands)
	if len(top) > 3 {
		top = top[:3]
	}
	for _, parent := range top {
		for _, nb := range s.neighbors(ctx, ws, parent.path, filePaths) {
			if !filePaths[nb] {
				continue
			}
			if _, exists := cands[nb]; exists {
				continue
			}
			c := candFor(cands, nb)
			c.score += ctxGraphCoeff * parent.score
			c.why = append(c.why, "imports/imported by "+parent.path)
		}
	}
}

// neighbors returns the 1-hop import + importer file paths of a file. Go import
// edges that target a package directory expand to indexed .go files in that
// package (bounded, sorted) instead of leaking the directory path.
func (s *Service) neighbors(ctx context.Context, ws, file string, filePaths map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	add := func(paths ...string) {
		for _, p := range paths {
			if p == "" || !filePaths[p] || seen[p] {
				continue
			}
			seen[p] = true
			out = append(out, p)
			if len(out) >= ctxMaxGraphNeighbors {
				return
			}
		}
	}
	imports, _ := s.store.ListCodeIndexEdges(ctx, store.CodeIndexEdgeFilter{WorkspaceID: ws, FromPath: file, Limit: 50})
	for _, e := range imports {
		if e.ToPath == "" {
			continue
		}
		if filePaths[e.ToPath] {
			add(e.ToPath)
			continue
		}
		add(expandGoPackageDir(filePaths, e.ToPath)...)
	}
	importers, _ := s.importerEdges(ctx, ws, file, 50)
	for _, e := range importers {
		add(e.FromPath)
	}
	return out
}

// expandGoPackageDir returns indexed .go files declared directly in pkgDir.
func expandGoPackageDir(filePaths map[string]bool, pkgDir string) []string {
	var matches []string
	for p := range filePaths {
		if !strings.HasSuffix(p, ".go") || path.Dir(p) != pkgDir {
			continue
		}
		matches = append(matches, p)
	}
	sort.Strings(matches)
	if len(matches) > ctxMaxPkgDirFiles {
		matches = matches[:ctxMaxPkgDirFiles]
	}
	return matches
}

// fillBudget greedily includes candidates by score until budget_tokens is hit,
// always including the top-ranked file even if it alone exceeds the budget.
func (s *Service) fillBudget(ctx context.Context, req ContextRequest, git *gitRunner, ordered []*ctxCand, filePaths map[string]bool, pack *ContextPack) {
	for i, c := range ordered {
		cf := s.buildContextFile(ctx, req.WorkspaceID, git, c, filePaths)
		cost := estimateTokens(renderJSON(cf))
		if i > 0 && pack.UsedTokens+cost > req.BudgetTokens {
			continue
		}
		pack.Files = append(pack.Files, cf)
		pack.UsedTokens += cost
	}
}

// buildContextFile assembles the orientation payload for one candidate.
func (s *Service) buildContextFile(ctx context.Context, ws string, git *gitRunner, c *ctxCand, filePaths map[string]bool) ContextFile {
	cf := ContextFile{Path: c.path, Score: round3(c.score), Why: dedupStrings(c.why)}
	if f, err := s.store.GetCodeIndexFile(ctx, ws, c.path); err == nil {
		cf.Summary = f.DocSummary
	}
	cf.Symbols = s.contextSymbols(ctx, ws, c)
	cf.Snippets = append(cf.Snippets, c.matchedChunks...)
	for _, o := range ownerTests(ctx, s.store, ws, c.path, filePaths) {
		cf.Tests = append(cf.Tests, o.Path)
	}
	if commits, err := git.recentChanges(ctx, c.path, ctxCommitDays, ctxMaxCommits); err == nil {
		cf.RecentCommits = commits
	}
	return cf
}

// contextSymbols returns up to 5 matched symbols plus up to 3 exported symbols
// (deduped) for a candidate file.
func (s *Service) contextSymbols(ctx context.Context, ws string, c *ctxCand) []SymbolHit {
	seen := map[string]bool{}
	var out []SymbolHit
	for i, sym := range c.matchedSyms {
		if i >= ctxMaxSymbols {
			break
		}
		seen[sym.Name] = true
		out = append(out, toSymbolHit(sym, c.path, 0))
	}
	syms, _ := s.store.ListCodeIndexSymbolsByPath(ctx, ws, c.path)
	added := 0
	for _, sym := range syms {
		if added >= ctxMaxExported {
			break
		}
		if sym.Exported && !seen[sym.Name] {
			out = append(out, toSymbolHit(sym, c.path, 0))
			seen[sym.Name] = true
			added++
		}
	}
	return out
}

// candFor returns (creating on first use) the candidate for a path.
func candFor(cands map[string]*ctxCand, path string) *ctxCand {
	c := cands[path]
	if c == nil {
		c = &ctxCand{path: path}
		cands[path] = c
	}
	return c
}

// orderCandidates returns candidates sorted by score desc, then path.
func orderCandidates(cands map[string]*ctxCand) []*ctxCand {
	out := make([]*ctxCand, 0, len(cands))
	for _, c := range cands {
		out = append(out, c)
	}
	sortStable(out, func(a, b *ctxCand) bool {
		if a.score != b.score {
			return a.score > b.score
		}
		return a.path < b.path
	})
	return out
}

// normalizeScores maps store relevance scores (negated BM25: higher = better)
// to [0,1] with the best hit at 1.0 and the worst at 0.0. All-equal inputs
// map to 1.0.
func normalizeScores(scores []float64) []float64 {
	out := make([]float64, len(scores))
	if len(scores) == 0 {
		return out
	}
	min, max := scores[0], scores[0]
	for _, s := range scores {
		if s < min {
			min = s
		}
		if s > max {
			max = s
		}
	}
	span := max - min
	for i, s := range scores {
		if span == 0 {
			out[i] = 1.0
		} else {
			out[i] = (s - min) / span
		}
	}
	return out
}

// renderJSON marshals v for the token-budget estimate (empty string on error).
func renderJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return ""
	}
	return string(b)
}

func round3(f float64) float64 { return math.Round(f*1000) / 1000 }

// dedupStrings removes duplicate strings preserving first-seen order.
func dedupStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
