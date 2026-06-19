package brain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
)

var (
	// ErrSlugEmpty is returned when a slug is empty.
	ErrSlugEmpty = errors.New("brain: slug must not be empty")
	// ErrSlugTraversal is returned when a slug contains path traversal
	// characters (/ \ ..) that could escape the brain root.
	ErrSlugTraversal = errors.New("brain: slug contains path traversal characters")
	// ErrSlugDotPrefix is returned when a slug starts with a dot (hidden
	// directory — a VCS/archive marker, never a legitimate workspace).
	ErrSlugDotPrefix = errors.New("brain: slug must not start with a dot")
)

// safeSlug validates that name is a safe single path component — no
// separators (/ or \), no parent traversal (..), no leading dot, and
// non-empty after trimming. It also rejects the special names "." and ".."
// that filepath.Join would treat as self/parent. When valid, it returns the
// trimmed slug unchanged. This is the door guard for every user-controlled
// path component joined into the brain directory tree (workspace id, client
// slug, skill name — anything that becomes a directory component under
// <brainDir>/).
func safeSlug(name string) (string, error) {
	s := strings.TrimSpace(name)
	if s == "" {
		return "", ErrSlugEmpty
	}
	if s == "." || s == ".." {
		return "", ErrSlugTraversal
	}
	if s[0] == '.' {
		return "", ErrSlugDotPrefix
	}
	for _, r := range s {
		switch r {
		case '/', '\\':
			return "", ErrSlugTraversal
		}
	}
	if strings.Contains(s, "..") {
		// A component containing ".." as a substring (e.g. "foo..bar") is
		// not a traversal vector — filepath.Join doesn't collapse embedded
		// "..". But a slug containing a literal ".." anywhere is a strong
		// signal of malicious intent and no legitimate workspace slug needs
		// consecutive dots. Block it outright.
		return "", ErrSlugTraversal
	}
	return s, nil
}

// kindForPath classifies a brain file by its location in the repo layout:
// .../workspaces/<ws>/workspace.md → workspace; .../crm/people/... → person;
// .../memory/... → memory; everything else under a workspace → task.
func kindForPath(path string) string {
	if filepath.Base(path) == workspaceFile {
		return EntityKindWorkspace
	}
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == crmSubdir && parts[i+1] == peopleSubdir {
			return EntityKindPerson
		}
		if parts[i] == memorySubdir {
			return EntityKindMemory
		}
	}
	return EntityKindTask
}

// hashBytes returns the lowercase hex sha256 of b.
func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// isMarkdown reports whether name has a .md extension.
func isMarkdown(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".md")
}

// baseName returns the final path element (the file's base name).
func baseName(path string) string {
	return filepath.Base(path)
}

// isIgnoredPath reports whether path lies under a dot-prefixed directory
// component (e.g. memory/facts/.history/, .git/, .cache/). These hold
// archive/VCS/derived data that must never be indexed — indexing a
// .history archive would record a phantom validation error (its filename
// stem carries a timestamp suffix) on every fact supersession. The
// ReindexAll sweep skips these structurally (non-recursive dir reads); this
// guards the watcher path (which watches subdirs recursively) + IndexFile.
func isIgnoredPath(path string) bool {
	for _, p := range strings.Split(filepath.ToSlash(path), "/") {
		// .mcplexer is the per-repo brain ROOT (M6 — federation), an indexable
		// folder, not an archive/VCS dir. Every OTHER dot-prefixed component
		// (.git/, .history/, .cache/) is ignored.
		if p == RepoBrainDirName {
			continue
		}
		if len(p) > 1 && p[0] == '.' {
			return true
		}
	}
	return false
}

// workspaceFromPath extracts the workspace slug from a brain path of the
// form .../workspaces/<slug>/... Returns "" when the path does not match.
func workspaceFromPath(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "workspaces" {
			return parts[i+1]
		}
	}
	return ""
}
