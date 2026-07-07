package index

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"path/filepath"
	"strings"
)

// denyDirs are directory names never descended into or indexed, on BOTH the
// git and WalkDir paths (a committed vendor/ or node_modules/ would otherwise
// pollute the map — plan §7.1 / R8). "testdata" and any dotfile directory are
// handled separately in isDenied/isDeniedDirName. Covers the common dependency
// and build-output dirs across ecosystems (JS, Go, Rust, Python, JVM, Ruby,
// iOS) so the index stays code-only even when such a dir is committed.
var denyDirs = map[string]struct{}{
	// JS/TS
	"node_modules": {}, "dist": {}, "build": {}, "out": {}, ".next": {},
	"coverage": {}, ".turbo": {}, ".parcel-cache": {},
	// Go / general
	".git": {}, "vendor": {}, "bin": {},
	// Rust / Maven / Gradle (build output)
	"target": {}, ".gradle": {},
	// Python
	"__pycache__": {}, ".venv": {}, "venv": {}, ".tox": {},
	".mypy_cache": {}, ".pytest_cache": {}, ".ruff_cache": {},
	// Ruby / iOS / IaC / caches
	".bundle": {}, "Pods": {}, ".terraform": {}, ".cache": {},
	// Agent/tool scratch
	".claude": {}, ".impeccable": {}, ".playwright-mcp": {},
}

// denyFileNames are exact filenames that are tracked text but pure noise for a
// code index — lock files and checksum manifests. They add no symbols and
// their dependency-name soup pollutes file/context search.
var denyFileNames = map[string]struct{}{
	"package-lock.json": {}, "npm-shrinkwrap.json": {}, "yarn.lock": {},
	"pnpm-lock.yaml": {}, "bun.lockb": {}, "go.sum": {}, "Cargo.lock": {},
	"poetry.lock": {}, "Pipfile.lock": {}, "composer.lock": {},
	"Gemfile.lock": {}, "flake.lock": {},
}

// denyFileSuffixes are extensions/suffixes that are generated or minified —
// noise that shouldn't feed symbol or context search.
var denyFileSuffixes = []string{".min.js", ".min.css", ".map", ".lock"}

// isDeniedFileName reports whether a bare filename is index noise (a lock file,
// checksum manifest, minified bundle, or source map).
func isDeniedFileName(base string) bool {
	if _, ok := denyFileNames[base]; ok {
		return true
	}
	for _, suf := range denyFileSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

// binarySniffLimit is how many leading bytes are scanned for a NUL to classify
// a file as binary.
const binarySniffLimit = 8 << 10

// isDeniedDirName reports whether a directory (by base name) must be skipped:
// an explicit denylist entry, "testdata", or any dotfile directory.
func isDeniedDirName(name string) bool {
	if _, ok := denyDirs[name]; ok {
		return true
	}
	if name == "testdata" {
		return true
	}
	return strings.HasPrefix(name, ".") && name != "." && name != ".."
}

// isDenied reports whether a root-relative file path lies under a denied dir.
// Every path component is checked against the denylist and "testdata"; every
// component except the final filename is additionally rejected when it is a
// dotfile directory.
func isDenied(rel string) bool {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		if p == "" {
			continue
		}
		last := i == len(parts)-1
		if last && isDeniedFileName(p) {
			return true
		}
		if _, ok := denyDirs[p]; ok {
			return true
		}
		if p == "testdata" {
			return true
		}
		if !last && strings.HasPrefix(p, ".") {
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
			return nil // skip unreadable entries rather than aborting the walk
		}
		if d.IsDir() {
			if p != root && isDeniedDirName(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil // never follow or index symlinks (symlink-escape guard R2)
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == "." || isDenied(rel) {
			return nil
		}
		out = append(out, rel)
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
		p = filepath.ToSlash(p)
		if isDenied(p) {
			continue
		}
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
	for _, pfx := range prefixes {
		pfx = strings.TrimSuffix(filepath.ToSlash(pfx), "/")
		if pfx == "" || rel == pfx || strings.HasPrefix(rel, pfx+"/") {
			return true
		}
	}
	return false
}

// sniffBinary reports whether data looks binary (a NUL byte within the first
// binarySniffLimit bytes).
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

// hashBytes returns the lowercase hex sha256 of data — the incremental build's
// content fingerprint.
func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
