package gateway

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/skillregistry"
)

func (h *handler) loadSkillPublishSource(
	ctx context.Context, sourcePath string,
) (*skillregistry.LocalSkillPayload, error) {
	resolved, err := h.resolveSkillPublishSourcePath(ctx, sourcePath)
	if err != nil {
		return nil, err
	}
	payload, err := skillregistry.PrepareLocalSkill(resolved)
	if err != nil {
		return nil, err
	}
	return payload, nil
}

func (h *handler) resolveSkillPublishSourcePath(ctx context.Context, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("source_path is required")
	}
	p := skillregistry.ExpandUserHome(raw)
	if !filepath.IsAbs(p) {
		base := h.routingClientRoot(ctx)
		if base == "" {
			return "", fmt.Errorf("source_path %q is relative, but this session has no client root", raw)
		}
		p = filepath.Join(base, p)
	}
	abs := filepath.Clean(p)
	allowed := h.skillPublishSourceRoots(ctx)
	if !pathWithinAnyRoot(abs, allowed) {
		return "", fmt.Errorf(
			"source_path %q must be under the current client/workspace root or ~/.claude/skills / ~/.codex/skills",
			raw,
		)
	}
	if eval, err := filepath.EvalSymlinks(abs); err == nil && !pathWithinAnyRoot(eval, allowed) {
		return "", fmt.Errorf("source_path %q resolves outside allowed skill source roots", raw)
	}
	return abs, nil
}

func (h *handler) skillPublishSourceRoots(ctx context.Context) []string {
	roots := []string{}
	roots = appendSkillSourceRoot(roots, h.routingClientRoot(ctx))
	for _, ws := range h.routingWorkspaceAncestors(ctx) {
		roots = appendSkillSourceRoot(roots, ws.RootPath)
	}
	roots = appendSkillSourceRoot(roots, skillregistry.ExpandUserHome("~/.claude/skills"))
	roots = appendSkillSourceRoot(roots, skillregistry.ExpandUserHome("~/.codex/skills"))
	return roots
}

func appendSkillSourceRoot(roots []string, raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == string(filepath.Separator) {
		return roots
	}
	abs, err := filepath.Abs(raw)
	if err != nil {
		return roots
	}
	abs = filepath.Clean(abs)
	roots = appendUniqueRoot(roots, abs)
	if eval, err := filepath.EvalSymlinks(abs); err == nil {
		roots = appendUniqueRoot(roots, filepath.Clean(eval))
	}
	return roots
}

func appendUniqueRoot(roots []string, root string) []string {
	for _, existing := range roots {
		if existing == root {
			return roots
		}
	}
	return append(roots, root)
}

func pathWithinAnyRoot(path string, roots []string) bool {
	path = filepath.Clean(path)
	for _, root := range roots {
		if isPathAncestor(filepath.Clean(root), path) {
			return true
		}
	}
	return false
}
