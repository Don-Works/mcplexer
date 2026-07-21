// Package pathguard provides runtime, symlink-aware filesystem boundaries.
package pathguard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Scope bounds concrete filesystem paths to one canonical root. Claims are
// optional write sub-scopes; an empty claim set permits writes anywhere under
// Root while a non-empty set narrows writes to the declared paths.
type Scope struct {
	root       string
	workingDir string
	claims     []string
}

// New resolves root, workingDir and claims against the live filesystem. It
// follows existing symlinks and resolves the nearest existing ancestor for a
// not-yet-created path, so a symlinked parent cannot smuggle a target outside
// root. Root and workingDir must already exist.
func New(root, workingDir string, claims []string) (Scope, error) {
	root, err := canonicalExisting(root)
	if err != nil {
		return Scope{}, fmt.Errorf("canonicalize root: %w", err)
	}
	if workingDir == "" {
		workingDir = root
	}
	workingDir, err = canonicalExisting(workingDir)
	if err != nil {
		return Scope{}, fmt.Errorf("canonicalize working directory: %w", err)
	}
	if !within(root, workingDir) {
		return Scope{}, fmt.Errorf("working directory %q is outside root %q", workingDir, root)
	}

	s := Scope{root: root, workingDir: workingDir}
	seen := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		resolved, resolveErr := s.Resolve(claim)
		if resolveErr != nil {
			return Scope{}, fmt.Errorf("resolve claim %q: %w", claim, resolveErr)
		}
		if _, ok := seen[resolved]; ok {
			continue
		}
		seen[resolved] = struct{}{}
		s.claims = append(s.claims, resolved)
	}
	return s, nil
}

// Root returns the canonical isolation root.
func (s Scope) Root() string { return s.root }

// WorkingDir returns the canonical working directory inside Root.
func (s Scope) WorkingDir() string { return s.workingDir }

// Claims returns a defensive copy of the canonical write claims.
func (s Scope) Claims() []string { return append([]string(nil), s.claims...) }

// Resolve turns a concrete absolute or working-directory-relative path into a
// canonical absolute path and rejects escapes. It deliberately refuses shell
// expansion and globs: callers must pass the actual runtime path, not source
// text that another layer might reinterpret later.
func (s Scope) Resolve(input string) (string, error) {
	if s.root == "" || s.workingDir == "" {
		return "", errors.New("filesystem scope is not initialized")
	}
	if input == "" {
		return "", errors.New("path is empty")
	}
	if strings.ContainsRune(input, '\x00') {
		return "", errors.New("path contains NUL")
	}
	if strings.HasPrefix(input, "~") || strings.Contains(input, "${") || strings.Contains(input, "$HOME") {
		return "", fmt.Errorf("path %q contains unresolved expansion", input)
	}
	if strings.ContainsAny(input, "*?[") {
		return "", fmt.Errorf("path %q contains an unresolved glob", input)
	}

	candidate := input
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(s.workingDir, candidate)
	}
	resolved, err := canonicalExistingOrParent(candidate)
	if err != nil {
		return "", err
	}
	if !within(s.root, resolved) {
		return "", fmt.Errorf("path %q resolves outside isolation root %q", input, s.root)
	}
	return resolved, nil
}

// Relative resolves a concrete workspace-relative path, then returns its
// canonical path relative to Root for use with os.Root. Absolute paths are
// intentionally rejected even when they happen to be inside Root so tool
// contracts cannot drift between local and remote path semantics.
func (s Scope) Relative(input string) (string, error) {
	if filepath.IsAbs(input) {
		return "", errors.New("path must be workspace-relative")
	}
	resolved, err := s.Resolve(input)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(s.root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("path escapes filesystem scope")
	}
	if rel == "" {
		rel = "."
	}
	return filepath.Clean(rel), nil
}

// LexicalRelative returns the workspace-relative path beneath Root without
// following symlinks. Exact file tools compare it with Relative to detect and
// reject write/edit through symlink aliases.
func (s Scope) LexicalRelative(input string) (string, error) {
	if s.root == "" || s.workingDir == "" {
		return "", errors.New("filesystem scope is not initialized")
	}
	if input == "" || filepath.IsAbs(input) {
		return "", errors.New("path must be non-empty and workspace-relative")
	}
	if strings.ContainsRune(input, '\x00') || strings.HasPrefix(input, "~") || strings.Contains(input, "${") || strings.Contains(input, "$HOME") || strings.ContainsAny(input, "*?[") {
		return "", errors.New("path contains unsupported expansion or glob")
	}
	candidate := filepath.Clean(filepath.Join(s.workingDir, input))
	if !within(s.root, candidate) {
		return "", errors.New("path escapes filesystem scope")
	}
	rel, err := filepath.Rel(s.root, candidate)
	if err != nil {
		return "", err
	}
	return filepath.Clean(rel), nil
}

// AllowsWriteRelative combines canonical resolution with claim enforcement.
// The returned path is suitable for the same os.Root used by Relative.
func (s Scope) AllowsWriteRelative(input string) (string, error) {
	if filepath.IsAbs(input) {
		return "", errors.New("path must be workspace-relative")
	}
	resolved, err := s.Resolve(input)
	if err != nil {
		return "", err
	}
	if !s.AllowsWrite(resolved) {
		return "", fmt.Errorf("write path %q is outside declared touches_files", input)
	}
	rel, err := filepath.Rel(s.root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("path escapes filesystem scope")
	}
	return filepath.Clean(rel), nil
}

// AllowsWrite reports whether path is within a declared write claim. With no
// claims, every path inside Root is writable. Call Resolve before this method.
func (s Scope) AllowsWrite(path string) bool {
	if !within(s.root, path) {
		return false
	}
	if len(s.claims) == 0 {
		return true
	}
	for _, claim := range s.claims {
		if within(claim, path) {
			return true
		}
	}
	return false
}

func canonicalExisting(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%q is not a directory", path)
	}
	return filepath.EvalSymlinks(abs)
}

func canonicalExistingOrParent(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	cursor := abs
	var suffix []string
	for {
		_, statErr := os.Lstat(cursor)
		switch {
		case statErr == nil:
			resolved, evalErr := filepath.EvalSymlinks(cursor)
			if evalErr != nil {
				return "", fmt.Errorf("resolve %q: %w", cursor, evalErr)
			}
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return filepath.Clean(resolved), nil
		case !os.IsNotExist(statErr):
			return "", fmt.Errorf("inspect %q: %w", cursor, statErr)
		}
		parent := filepath.Dir(cursor)
		if parent == cursor {
			return "", fmt.Errorf("no existing ancestor for %q", path)
		}
		suffix = append(suffix, filepath.Base(cursor))
		cursor = parent
	}
}

func within(root, candidate string) bool {
	if root == "" || candidate == "" {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
