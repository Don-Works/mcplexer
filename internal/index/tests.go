package index

import (
	"context"
	"path"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// isTestPath reports whether a root-relative path is a test file by naming
// convention (Go _test.go; TS/JS .test./.spec./__tests__/).
func isTestPath(p string) bool {
	if strings.HasSuffix(p, "_test.go") {
		return true
	}
	if strings.Contains(p, ".test.") || strings.Contains(p, ".spec.") {
		return true
	}
	return strings.Contains(p, "/__tests__/") || strings.HasPrefix(p, "__tests__/")
}

// goExcludedTestBases are shared/helper test files that should not be claimed as
// "medium" owners of a same-package source file (P6).
func isExcludedGoTestBase(base string) bool {
	if base == "main_test.go" {
		return true
	}
	for _, marker := range []string{"helper", "shared", "setup", "fixture"} {
		if strings.Contains(base, marker) {
			return true
		}
	}
	return false
}

// maxMediumOwners caps the same-package/medium blast radius: a big Go package
// has dozens of _test.go siblings and listing them all buries the direct
// matches (and bloats context packs).
const maxMediumOwners = 10

// ownerTests returns the tests that own a source file, deduped and preferring
// higher confidence. filePaths is the set of indexed root-relative paths.
// Medium-confidence owners are ranked by name affinity to the source file and
// capped at maxMediumOwners.
func ownerTests(ctx context.Context, st store.CodeIndexStore, ws, file string, filePaths map[string]bool) []TestOwner {
	owners := map[string]TestOwner{}
	switch languageForPath(file) {
	case "go":
		goOwners(file, filePaths, owners)
	case "typescript", "javascript":
		tsOwners(file, filePaths, owners)
		importEdgeOwners(ctx, st, ws, file, owners)
	}
	return capMediums(file, sortedOwners(owners))
}

// capMediums keeps every high-confidence owner and the maxMediumOwners medium
// owners whose basenames share the most tokens with the source file.
func capMediums(file string, owners []TestOwner) []TestOwner {
	var high, medium []TestOwner
	for _, o := range owners {
		if o.Confidence == "high" {
			high = append(high, o)
		} else {
			medium = append(medium, o)
		}
	}
	if len(medium) <= maxMediumOwners {
		return owners
	}
	want := map[string]bool{}
	for _, tok := range splitIdent(path.Base(file)) {
		want[tok] = true
	}
	affinity := func(o TestOwner) int {
		n := 0
		for _, tok := range splitIdent(path.Base(o.Path)) {
			if want[tok] {
				n++
			}
		}
		return n
	}
	sortStable(medium, func(a, b TestOwner) bool {
		if av, bv := affinity(a), affinity(b); av != bv {
			return av > bv
		}
		return a.Path < b.Path
	})
	return append(high, medium[:maxMediumOwners]...)
}

// goOwners applies the Go naming heuristic: base_test.go is a high-confidence
// direct match; other non-excluded *_test.go in the same dir are medium.
func goOwners(file string, filePaths map[string]bool, owners map[string]TestOwner) {
	dir := path.Dir(file)
	base := strings.TrimSuffix(path.Base(file), ".go")
	direct := path.Join(dir, base+"_test.go")
	for p := range filePaths {
		if path.Dir(p) != dir || !strings.HasSuffix(p, "_test.go") {
			continue
		}
		if p == direct {
			addOwner(owners, TestOwner{p, "high", "direct name match (base_test.go)"})
			continue
		}
		if !isExcludedGoTestBase(path.Base(p)) {
			addOwner(owners, TestOwner{p, "medium", "same-package test"})
		}
	}
}

// tsOwners applies the TS/JS naming heuristic: a same-basename .test./.spec.
// file is high in the same dir, medium under __tests__/ (basename must match,
// P6) or elsewhere.
func tsOwners(file string, filePaths map[string]bool, owners map[string]TestOwner) {
	dir := path.Dir(file)
	base := tsBaseName(file)
	for p := range filePaths {
		if !isTestPath(p) || tsBaseName(p) != base {
			continue
		}
		switch {
		case path.Dir(p) == dir:
			addOwner(owners, TestOwner{p, "high", "same-dir name match"})
		case strings.Contains(p, "/__tests__/") || strings.HasPrefix(p, "__tests__/"):
			addOwner(owners, TestOwner{p, "medium", "__tests__ name match"})
		default:
			addOwner(owners, TestOwner{p, "medium", "name match"})
		}
	}
}

// importEdgeOwners adds test files that import the source file (medium).
func importEdgeOwners(ctx context.Context, st store.CodeIndexStore, ws, file string, owners map[string]TestOwner) {
	edges, err := st.ListCodeIndexEdges(ctx, store.CodeIndexEdgeFilter{WorkspaceID: ws, ToPath: file, Limit: 200})
	if err != nil {
		return
	}
	for _, e := range edges {
		if isTestPath(e.FromPath) {
			addOwner(owners, TestOwner{e.FromPath, "medium", "imports this file"})
		}
	}
}

// tsBaseName strips the extension and any .test/.spec infix from a TS/JS path's
// base name, so "Foo.test.tsx" and "Foo.tsx" share the base "Foo".
func tsBaseName(p string) string {
	b := path.Base(p)
	for _, ext := range []string{".tsx", ".ts", ".jsx", ".js", ".mjs", ".cjs"} {
		b = strings.TrimSuffix(b, ext)
	}
	b = strings.TrimSuffix(b, ".test")
	b = strings.TrimSuffix(b, ".spec")
	return b
}

// addOwner inserts an owner, upgrading a stored medium entry to high.
func addOwner(owners map[string]TestOwner, o TestOwner) {
	if cur, ok := owners[o.Path]; ok && cur.Confidence == "high" {
		return
	}
	owners[o.Path] = o
}

// sortedOwners returns owners ordered high-before-medium, then by path.
func sortedOwners(owners map[string]TestOwner) []TestOwner {
	out := make([]TestOwner, 0, len(owners))
	for _, o := range owners {
		out = append(out, o)
	}
	sortStable(out, func(a, b TestOwner) bool {
		if a.Confidence != b.Confidence {
			return a.Confidence == "high"
		}
		return a.Path < b.Path
	})
	return out
}
