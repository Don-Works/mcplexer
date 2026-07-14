package index

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// contextStale reports whether the workspace on disk has moved past the indexed
// build. Git repos compare HEAD plus a bounded check of dirty paths (not just
// dirty count). Non-git workspaces compare enumerated file stats against the
// index so edits cannot hide behind an unchanged count.
func contextStale(
	ctx context.Context,
	git *gitRunner,
	build *store.CodeIndexBuild,
	st store.CodeIndexStore,
	workspaceID, root string,
) bool {
	if build == nil || !build.Complete {
		return true
	}
	if git.isRepo(ctx) {
		head, _ := git.head(ctx)
		if head != build.GitHead {
			return true
		}
		porcelain, _ := git.statusPorcelain(ctx)
		if strings.TrimSpace(porcelain) == "" {
			// If the prior build captured a dirty tree, becoming clean changes
			// source content even though HEAD is unchanged.
			return build.DirtyCount != 0
		}
		return gitDirtyPathsStale(ctx, st, workspaceID, root, porcelain)
	}
	return nonGitWorkspaceStale(ctx, st, workspaceID, root, build)
}

// gitDirtyPathsStale returns true when any path listed in git status differs
// from the indexed freshness tuple (size/mtime/content hash).
func gitDirtyPathsStale(
	ctx context.Context,
	st store.CodeIndexStore,
	workspaceID, root, porcelain string,
) bool {
	stats, err := st.ListCodeIndexFileStats(ctx, workspaceID)
	if err != nil {
		return true
	}
	byPath := make(map[string]store.CodeIndexFileStat, len(stats))
	for _, s := range stats {
		byPath[s.Path] = s
	}
	for _, rel := range parsePorcelainPaths(porcelain) {
		// A tracked dependency/build artifact can be dirty, but it is outside
		// the index by policy and must not force an endless refresh loop.
		if !ShouldIndexPath(rel) {
			continue
		}
		stored, indexed := byPath[rel]
		if !indexed || fileStatStale(root, rel, stored) {
			return true
		}
	}
	return false
}

// nonGitWorkspaceStale compares the live tree against indexed stats. A never-
// indexed path or any size/mtime/hash drift is stale.
func nonGitWorkspaceStale(
	ctx context.Context,
	st store.CodeIndexStore,
	workspaceID, root string,
	build *store.CodeIndexBuild,
) bool {
	stats, err := st.ListCodeIndexFileStats(ctx, workspaceID)
	if err != nil {
		return true
	}
	byPath := make(map[string]store.CodeIndexFileStat, len(stats))
	for _, s := range stats {
		byPath[s.Path] = s
	}
	files, _, err := enumerate(ctx, root, newGitRunner(root, nil), nil)
	if err != nil {
		return true
	}
	if len(files) != build.FileCount {
		return true
	}
	for _, rel := range files {
		if fileStatStale(root, rel, byPath[rel]) {
			return true
		}
	}
	return false
}

// fileStatStale reports whether rel on disk differs from the stored stat. An
// absent stored stat for a live file is stale; a missing live file is not
// checked here (prune happens on build).
func fileStatStale(root, rel string, stored store.CodeIndexFileStat) bool {
	info, err := os.Lstat(filepath.Join(root, rel))
	if err != nil || !info.Mode().IsRegular() {
		return stored.Path != ""
	}
	size := int(info.Size())
	mtime := info.ModTime().Unix()
	if stored.Path == "" || stored.SizeBytes != size {
		return true
	}
	if stored.ContentHash == "" {
		return stored.MtimeUnix != mtime
	}
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return true
	}
	return stored.ContentHash != hashBytes(data)
}

// parsePorcelainPaths extracts root-relative paths from `git status --porcelain`
// lines. Renames report the post-image path (the segment after " -> ").
func parsePorcelainPaths(porcelain string) []string {
	if strings.ContainsRune(porcelain, '\x00') {
		return parsePorcelainZPaths(porcelain)
	}
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(porcelain, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || len(line) < 3 {
			continue
		}
		rest := strings.TrimSpace(line[2:])
		if i := strings.Index(rest, " -> "); i >= 0 {
			rest = strings.TrimSpace(rest[i+4:])
		}
		rest = filepath.ToSlash(rest)
		if rest == "" || seen[rest] {
			continue
		}
		seen[rest] = true
		out = append(out, rest)
	}
	return out
}

// parsePorcelainZPaths parses `git status --porcelain=v1 -z`. NUL framing is
// unambiguous for spaces, quotes, tabs and newlines. Rename/copy records carry
// a second path; retain both sides so either a newly-added or removed indexed
// path makes the source state stale.
func parsePorcelainZPaths(porcelain string) []string {
	parts := strings.Split(porcelain, "\x00")
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		p = filepath.ToSlash(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for i := 0; i < len(parts); i++ {
		rec := parts[i]
		if len(rec) < 4 {
			continue
		}
		status := rec[:2]
		add(rec[3:])
		if strings.ContainsAny(status, "RC") && i+1 < len(parts) {
			i++
			add(parts[i])
		}
	}
	return out
}
