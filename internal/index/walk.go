package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
)

// Index exclusion policy (plan §7.1 / R8): dependency trees, build outputs,
// caches, generated/minified bundles, source maps, and lock/checksum manifests
// are never enumerated, chunked, or embedded — even when committed to git.
// Dotfile directories and testdata/ are excluded too. Source paths whose
// components only resemble denied names stay indexable (e.g.
// internal/index/build.go, pkg/outlier/out.go); matching is on whole path
// components, case-insensitive for directory names.
//
// ShouldIndexPath is the single gate — git ls-files, WalkDir, and future
// chunk/embedding code must all consult it after normalizeIndexPath.

// denyDirs are directory base names never descended into or indexed.
var denyDirs = map[string]struct{}{
	// JS/TS
	"node_modules": {}, "dist": {}, "build": {}, "out": {}, ".next": {},
	"coverage": {}, ".turbo": {}, ".parcel-cache": {},
	// Go / general
	".git": {}, "vendor": {}, "bin": {},
	// Rust / Maven / Gradle
	"target": {}, ".gradle": {},
	// Python
	"__pycache__": {}, ".venv": {}, "venv": {}, ".tox": {},
	".mypy_cache": {}, ".pytest_cache": {}, ".ruff_cache": {},
	// Ruby / iOS / IaC / caches
	".bundle": {}, "Pods": {}, ".terraform": {}, ".cache": {},
	// Agent/tool scratch
	".claude": {}, ".impeccable": {}, ".playwright-mcp": {},
}

// denyFileNames are exact filenames that add no symbols (locks, checksums).
var denyFileNames = map[string]struct{}{
	"package-lock.json": {}, "npm-shrinkwrap.json": {}, "yarn.lock": {},
	"pnpm-lock.yaml": {}, "bun.lockb": {}, "go.sum": {}, "Cargo.lock": {},
	"poetry.lock": {}, "Pipfile.lock": {}, "composer.lock": {},
	"Gemfile.lock": {}, "flake.lock": {},
	"checksums.txt": {}, "checksum.txt": {}, "CHECKSUMS": {},
	"SHA256SUMS": {}, "MD5SUMS": {}, "sha256sum.txt": {}, "md5sum.txt": {},
}

// denyFileSuffixes are generated/minified artifact suffixes.
var denyFileSuffixes = []string{".min.js", ".min.css", ".map", ".lock"}

// normalizeIndexPath canonicalizes a root-relative path for policy checks:
// forward slashes, no leading ./ or /, path.Clean, and rejection of .. escapes.
func normalizeIndexPath(rel string) string {
	rel = strings.TrimSpace(rel)
	rel = strings.ReplaceAll(rel, "\\", "/")
	for strings.HasPrefix(rel, "./") {
		rel = strings.TrimPrefix(rel, "./")
	}
	rel = strings.TrimPrefix(rel, "/")
	rel = path.Clean(rel)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") || strings.Contains(rel, "/../") {
		return ""
	}
	return rel
}

// ShouldIndexPath reports whether rel may be enumerated, chunked, or embedded.
// Empty or unnormalizable paths are rejected.
func ShouldIndexPath(rel string) bool {
	norm := normalizeIndexPath(rel)
	if norm == "" {
		return false
	}
	return !isDenied(norm)
}

// isDeniedFileName reports whether a bare filename is index noise.
func isDeniedFileName(base string) bool {
	if _, ok := denyFileNames[base]; ok {
		return true
	}
	for name := range denyFileNames {
		if strings.EqualFold(base, name) {
			return true
		}
	}
	for _, suf := range denyFileSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

// isDeniedDirName reports whether a directory base name must be skipped during
// WalkDir pruning. Mirrors isDeniedPathComponent for non-file components.
func isDeniedDirName(name string) bool {
	return isDeniedPathComponent(name, false)
}

// isDeniedPathComponent checks one path component against the exclusion policy.
func isDeniedPathComponent(name string, isFile bool) bool {
	if name == "" || name == "." || name == ".." {
		return true
	}
	if isFile {
		if isDeniedFileName(name) {
			return true
		}
	} else if strings.HasPrefix(name, ".") {
		return true
	}
	if name == "testdata" {
		return true
	}
	for dir := range denyDirs {
		if strings.EqualFold(name, dir) {
			return true
		}
	}
	return false
}

// isDenied reports whether a normalized root-relative file path is excluded.
func isDenied(rel string) bool {
	rel = normalizeIndexPath(rel)
	if rel == "" {
		return true
	}
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if p == "" {
			continue
		}
		if isDeniedPathComponent(p, i == len(parts)-1) {
			return true
		}
	}
	return false
}

// enumerate lists candidate root-relative file paths. It prefers git (honoring
// .gitignore); when git is unavailable or the dir is not a repo it falls back
// to filepath.WalkDir. The returned bool reports whether git was used. The
// denylist and the optional prefix filter apply to both paths.
func enumerate(ctx context.Context, root string, git *gitRunner, prefixes []string) ([]string, bool, error) {
	if git.available() {
		if files, err := git.lsFiles(ctx); err == nil {
			return filterPaths(files, prefixes), true, nil
		}
	}
	files, err := walkDir(root, prefixes)
	return files, false, err
}

// walkDir enumerates non-symlink files under root, pruning denied directories.
func walkDir(root string, prefixes []string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && isDeniedDirName(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return nil
		}
		if !ShouldIndexPath(rel) {
			return nil
		}
		out = append(out, normalizeIndexPath(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return filterPaths(out, prefixes), nil
}

// filterPaths drops denied paths and, when prefixes is non-empty, keeps only
// paths under one of the given root-relative prefixes.
func filterPaths(paths, prefixes []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !ShouldIndexPath(p) {
			continue
		}
		p = normalizeIndexPath(p)
		if !matchesPrefixes(p, prefixes) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// matchesPrefixes reports whether rel is exactly, or lies under, one of the
// prefixes. An empty prefix list matches everything.
func matchesPrefixes(rel string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	rel = normalizeIndexPath(rel)
	for _, pfx := range prefixes {
		pfx = strings.TrimSuffix(normalizeIndexPath(pfx), "/")
		if pfx == "" || rel == pfx || strings.HasPrefix(rel, pfx+"/") {
			return true
		}
	}
	return false
}

// sniffBinary reports whether data looks binary (a NUL byte within the first
// binarySniffLimit bytes).
const binarySniffLimit = 8 << 10

func sniffBinary(data []byte) bool {
	n := len(data)
	if n > binarySniffLimit {
		n = binarySniffLimit
	}
	for i := 0; i < n; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// hashBytes returns the lowercase hex sha256 of data.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}