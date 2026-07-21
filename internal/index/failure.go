package index

import (
	"context"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

var (
	reFailPathLine = regexp.MustCompile(`([\w./~-]+\.(?:go|ts|tsx|js|jsx)):(\d+)`)
	reGoFrame      = regexp.MustCompile(`(?m)^\s+([\w./-]+\.go):(\d+)`)
	reJestFrame    = regexp.MustCompile(`\(([\w./-]+\.(?:ts|tsx|js|jsx)):(\d+):\d+\)`)
	reAtFrame      = regexp.MustCompile(`(?m)\bat .*\((.+?):(\d+):\d+\)`)
	reGoTestFail   = regexp.MustCompile(`(?m)^\s*--- FAIL: (Test[\w/]+)`)
	reGoPkgFail    = regexp.MustCompile(`(?m)^FAIL\s+([\w./-]+)`)
	reIdent        = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]{3,}`)
)

// failureStopwords are high-frequency test/diagnostic words excluded from the
// identifier harvest so they don't drown out real symbol names.
var failureStopwords = map[string]bool{
	"test": true, "fail": true, "error": true, "panic": true, "expected": true,
	"actual": true, "want": true, "true": true, "false": true, "nil": true,
	"func": true, "return": true, "package": true, "import": true, "string": true,
	"value": true, "result": true, "context": true, "should": true, "assert": true,
	"received": true, "undefined": true, "cannot": true, "runtime": true, "goroutine": true,
}

// failureScorer accumulates per-file evidence scores from a pasted failure.
type failureScorer struct {
	st          store.CodeIndexStore
	ws, root    string
	filePaths   map[string]bool
	scores      map[string]*FailureCandidate
	frameHits   map[string]int
	suffixScans int
}

// maxSuffixScans bounds the O(files) suffix-resolution fallback so hostile
// input full of unresolvable path mentions cannot make mapping quadratic.
const maxSuffixScans = 50

// mapFailure parses failure text and returns the top-`limit` candidate files.
func (s *Service) mapFailure(ctx context.Context, ws, root, text string, limit int) ([]FailureCandidate, error) {
	filePaths, err := s.filePathSet(ctx, ws)
	if err != nil {
		return nil, err
	}
	fs := &failureScorer{
		st: s.store, ws: ws, root: root, filePaths: filePaths,
		scores: map[string]*FailureCandidate{}, frameHits: map[string]int{},
	}
	fs.pathMentions(text)
	fs.stackFrames(text)
	fs.goTestFailures(ctx, text)
	fs.goPackageFails(text)
	fs.identifierHarvest(ctx, text)
	return fs.top(limit), nil
}

// add applies a score delta + reason to a file, but only when the (normalized)
// path is actually in the index — candidates must be real files to open.
// Mentions that miss exactly are suffix-resolved: tools report paths relative
// to their own working dir (vitest emits src/… from inside web/), so a unique
// indexed path ending in the mention still counts.
func (fs *failureScorer) add(p string, delta float64, reason string) {
	p = normalizeRel(p, fs.root)
	if p == "" {
		return
	}
	if !fs.filePaths[p] {
		p = fs.resolveSuffix(p)
	}
	if p == "" {
		return
	}
	c := fs.scores[p]
	if c == nil {
		c = &FailureCandidate{Path: p}
		fs.scores[p] = c
	}
	c.Score += delta
	if !containsStr(c.Reasons, reason) {
		c.Reasons = append(c.Reasons, reason)
	}
}

// resolveSuffix maps a mention to the unique indexed path that ends with it
// ("" when zero or several match, or the scan budget is spent).
func (fs *failureScorer) resolveSuffix(p string) string {
	if fs.suffixScans >= maxSuffixScans || strings.Contains(p, "..") {
		return ""
	}
	fs.suffixScans++
	match := ""
	for indexed := range fs.filePaths {
		if strings.HasSuffix(indexed, "/"+p) {
			if match != "" {
				return "" // ambiguous
			}
			match = indexed
		}
	}
	return match
}

// pathMentions scores explicit `file.go:line` mentions (+3.0, once per file).
func (fs *failureScorer) pathMentions(text string) {
	seen := map[string]bool{}
	for _, m := range reFailPathLine.FindAllStringSubmatch(text, -1) {
		p := normalizeRel(m[1], fs.root)
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		fs.add(m[1], 3.0, "path mentioned at line "+m[2])
	}
}

// stackFrames scores Go and jest/vitest stack frames (+2.0 each, capped 3/file).
func (fs *failureScorer) stackFrames(text string) {
	for _, re := range []*regexp.Regexp{reGoFrame, reJestFrame, reAtFrame} {
		for _, m := range re.FindAllStringSubmatch(text, -1) {
			p := normalizeRel(m[1], fs.root)
			if p == "" || !fs.filePaths[p] || fs.frameHits[p] >= 3 {
				continue
			}
			fs.frameHits[p]++
			fs.add(m[1], 2.0, "stack frame at line "+m[2])
		}
	}
}

// goTestFailures maps `--- FAIL: TestX` lines to the test's file (+2.0) and the
// source files that test owns (+1.5). Names are deduped and capped so pasted
// output with thousands of FAIL lines costs at most 20 store lookups.
func (fs *failureScorer) goTestFailures(ctx context.Context, text string) {
	seen := map[string]bool{}
	var names []string
	for _, m := range reGoTestFail.FindAllStringSubmatch(text, -1) {
		name := strings.SplitN(m[1], "/", 2)[0]
		if seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
		if len(names) >= 20 {
			break
		}
	}
	for _, name := range names {
		hits, err := fs.st.SearchCodeIndexSymbols(ctx, store.CodeIndexSymbolQuery{
			WorkspaceID: fs.ws, Query: tokenString(name), Limit: 5,
		})
		if err != nil {
			continue
		}
		for _, h := range hits {
			if h.Symbol.Name != name {
				continue
			}
			fs.add(h.Path, 2.0, "failing test "+name)
			if src := goReverseOwner(h.Path, fs.filePaths); src != "" {
				fs.add(src, 1.5, "owned by failing test "+name)
			}
			break
		}
	}
}

// goReverseOwner returns the same-dir source file a Go _test.go file tests
// (foo_test.go -> foo.go), or "" when absent.
func goReverseOwner(testFile string, filePaths map[string]bool) string {
	if !strings.HasSuffix(testFile, "_test.go") {
		return ""
	}
	src := strings.TrimSuffix(testFile, "_test.go") + ".go"
	if filePaths[src] {
		return src
	}
	return ""
}

// goPackageFails scores files in a directory named by a `FAIL <pkg>` line
// (+0.5). The package import path is matched by suffix against indexed dirs.
func (fs *failureScorer) goPackageFails(text string) {
	dirs := indexedDirs(fs.filePaths)
	for _, m := range reGoPkgFail.FindAllStringSubmatch(text, -1) {
		pkg := m[1]
		for dir := range dirs {
			if pkg == dir || strings.HasSuffix(pkg, "/"+dir) {
				for _, f := range dirs[dir] {
					fs.add(f, 0.5, "in failing package "+path.Base(dir))
				}
			}
		}
	}
}

// identifierHarvest scores symbol matches for the most frequent identifiers in
// the text (+1.0 × normalized BM25).
func (fs *failureScorer) identifierHarvest(ctx context.Context, text string) {
	type hit struct {
		path  string
		name  string
		score float64
	}
	var hits []hit
	var scores []float64
	for _, tok := range topIdentifiers(text, 10) {
		res, err := fs.st.SearchCodeIndexSymbols(ctx, store.CodeIndexSymbolQuery{
			WorkspaceID: fs.ws, Query: tok, Limit: 10,
		})
		if err != nil {
			continue
		}
		for _, h := range res {
			hits = append(hits, hit{h.Path, h.Symbol.Name, h.Score})
			scores = append(scores, h.Score)
		}
	}
	norm := normalizeScores(scores)
	for i, h := range hits {
		fs.add(h.path, 1.0*norm[i], "symbol match: "+h.name)
	}
}

// topIdentifiers returns up to n distinct non-stopword identifiers, most
// frequent first.
func topIdentifiers(text string, n int) []string {
	freq := map[string]int{}
	for _, m := range reIdent.FindAllString(text, -1) {
		low := strings.ToLower(m)
		if failureStopwords[low] {
			continue
		}
		freq[m]++
	}
	toks := make([]string, 0, len(freq))
	for t := range freq {
		toks = append(toks, t)
	}
	sortStable(toks, func(a, b string) bool {
		if freq[a] != freq[b] {
			return freq[a] > freq[b]
		}
		return a < b
	})
	if len(toks) > n {
		toks = toks[:n]
	}
	return toks
}

// top returns the highest-scoring candidates, best first, capped at limit.
func (fs *failureScorer) top(limit int) []FailureCandidate {
	out := make([]FailureCandidate, 0, len(fs.scores))
	for _, c := range fs.scores {
		if c.Score > 0 {
			out = append(out, *c)
		}
	}
	sortStable(out, func(a, b FailureCandidate) bool {
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.Path < b.Path
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// normalizeRel makes a path root-relative with forward slashes, stripping a
// leading "./" and, for an absolute path under root, the root prefix.
func normalizeRel(p, root string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	if filepath.IsAbs(p) && root != "" {
		if rel, err := filepath.Rel(root, p); err == nil && !strings.HasPrefix(rel, "..") {
			p = rel
		}
	}
	return filepath.ToSlash(p)
}

// indexedDirs groups indexed file paths by their directory.
func indexedDirs(filePaths map[string]bool) map[string][]string {
	dirs := map[string][]string{}
	for p := range filePaths {
		d := path.Dir(p)
		dirs[d] = append(dirs[d], p)
	}
	return dirs
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
