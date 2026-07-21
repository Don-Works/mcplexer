package skillregistry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/skills"
	"github.com/don-works/mcplexer/internal/store"
)

const (
	// CompositionRendererVersion changes only when the deterministic render
	// contract changes. Materialized skills persist it to force a refresh.
	CompositionRendererVersion = 1
	MaxCompositionDepth        = 8
	MaxCompositionEdges        = 32
	MaxExpandedBodyBytes       = 64 * 1024
)

// CompositionEdge records one resolved include in deterministic traversal
// order. It deliberately omits workspace IDs while retaining the requested
// scope rule and every integrity pin needed to reproduce the expansion.
type CompositionEdge struct {
	FromName    string `json:"from_name"`
	FromVersion int    `json:"from_version"`
	IncludeID   string `json:"include_id"`
	ToName      string `json:"to_name"`
	ToVersion   int    `json:"to_version"`
	Scope       string `json:"scope"`
	ContentHash string `json:"content_hash"`
	Section     string `json:"section,omitempty"`
	Depth       int    `json:"depth"`
}

// RenderedSkill is the runtime form of a raw registry entry. Body preserves
// the root SKILL.md frontmatter and expands only explicit markdown markers.
type RenderedSkill struct {
	Body       string            `json:"body"`
	SHA256     string            `json:"expanded_sha256"`
	Provenance []CompositionEdge `json:"provenance,omitempty"`
}

type compositionKey struct {
	workspace string
	name      string
	version   int
	section   string
}

type compositionState struct {
	registry *Registry
	edges    []CompositionEdge
	stack    []compositionKey
	active   map[compositionKey]int
}

// Render resolves a root entry using normal registry visibility, then renders
// all of its dependencies through exact scope/version/hash lookups.
func (r *Registry) Render(
	ctx context.Context, scope store.SkillScope, name string, ref VersionRef,
) (*RenderedSkill, error) {
	entry, err := r.Get(ctx, scope, name, ref)
	if err != nil {
		return nil, err
	}
	return r.RenderEntry(ctx, entry)
}

// RenderEntry deterministically expands one raw entry. Registry.Get, export,
// diff, version hashes, and bundle validation remain raw; runtime consumers
// opt into this method explicitly.
func (r *Registry) RenderEntry(ctx context.Context, entry *store.SkillRegistryEntry) (*RenderedSkill, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	if entry == nil {
		return nil, errors.New("composition: entry is nil")
	}
	parsed, err := Parse(entry.Body, entry.Name)
	if err != nil {
		return nil, fmt.Errorf("composition parse %s: %w", entry.Name, err)
	}
	if parsed.ContentHash != entry.ContentHash {
		return nil, fmt.Errorf("composition %s@v%d: stored content hash %s does not match body %s",
			entry.Name, entry.Version, entry.ContentHash, parsed.ContentHash)
	}
	// Marker syntax is reserved only when the root explicitly opts into
	// composition. Legacy/plain skills may contain literal examples or
	// unmatched section comments; keep their runtime body byte-identical.
	// Recursive renderNode calls still parse dependency sections even when
	// that dependency has no includes of its own.
	if len(parsed.Extra.Includes) == 0 {
		return &RenderedSkill{Body: entry.Body, SHA256: parsed.ContentHash}, nil
	}

	state := &compositionState{
		registry: r,
		active:   make(map[compositionKey]int),
	}
	body, err := state.renderNode(ctx, entry, "", 0)
	if err != nil {
		return nil, err
	}
	if len(body) > MaxExpandedBodyBytes {
		return nil, fmt.Errorf("composition %s@v%d expands to %d bytes (maximum %d)",
			entry.Name, entry.Version, len(body), MaxExpandedBodyBytes)
	}
	sum := sha256.Sum256([]byte(body))
	return &RenderedSkill{
		Body:       body,
		SHA256:     hex.EncodeToString(sum[:]),
		Provenance: append([]CompositionEdge(nil), state.edges...),
	}, nil
}

func (s *compositionState) renderNode(
	ctx context.Context, entry *store.SkillRegistryEntry, section string, depth int,
) (string, error) {
	if depth > MaxCompositionDepth {
		return "", fmt.Errorf("composition depth exceeds %d at %s@v%d", MaxCompositionDepth, entry.Name, entry.Version)
	}
	key := compositionKey{
		workspace: compositionWorkspaceKey(entry.WorkspaceID),
		name:      entry.Name,
		version:   entry.Version,
		section:   section,
	}
	if at, exists := s.active[key]; exists {
		chain := append(append([]compositionKey(nil), s.stack[at:]...), key)
		parts := make([]string, 0, len(chain))
		for _, item := range chain {
			label := fmt.Sprintf("%s/%s@v%d", compositionWorkspaceLabel(item.workspace), item.name, item.version)
			if item.section != "" {
				label += "#" + item.section
			}
			parts = append(parts, label)
		}
		return "", fmt.Errorf("composition cycle: %s", strings.Join(parts, " -> "))
	}
	s.active[key] = len(s.stack)
	s.stack = append(s.stack, key)
	defer func() {
		delete(s.active, key)
		s.stack = s.stack[:len(s.stack)-1]
	}()

	parsed, err := Parse(entry.Body, entry.Name)
	if err != nil {
		return "", fmt.Errorf("composition parse %s@v%d: %w", entry.Name, entry.Version, err)
	}
	if parsed.ContentHash != entry.ContentHash {
		return "", fmt.Errorf("composition %s@v%d: stored content hash %s does not match body %s",
			entry.Name, entry.Version, entry.ContentHash, parsed.ContentHash)
	}
	prefix, markdown, err := splitSkillDocument(entry.Body)
	if err != nil {
		return "", fmt.Errorf("composition split %s@v%d: %w", entry.Name, entry.Version, err)
	}
	selected, err := selectCompositionSection(markdown, section)
	if err != nil {
		return "", fmt.Errorf("composition %s@v%d: %w", entry.Name, entry.Version, err)
	}

	byID := make(map[string]skills.SkillInclude, len(parsed.Extra.Includes))
	for _, include := range parsed.Extra.Includes {
		byID[include.ID] = include
	}
	used := make(map[string]bool, len(byID))
	expanded, err := s.expandMarkers(ctx, entry, selected, byID, used, depth)
	if err != nil {
		return "", err
	}
	if section == "" {
		for _, include := range parsed.Extra.Includes {
			if !used[include.ID] {
				return "", fmt.Errorf("composition %s@v%d: include %q is declared but has no marker",
					entry.Name, entry.Version, include.ID)
			}
		}
	}
	// Only the root retains its frontmatter. Included skills contribute
	// markdown fragments, never their own identity or composition metadata.
	if depth > 0 {
		prefix = ""
	}
	result := prefix + expanded
	if len(result) > MaxExpandedBodyBytes {
		return "", fmt.Errorf("composition %s@v%d expands to %d bytes (maximum %d)",
			entry.Name, entry.Version, len(result), MaxExpandedBodyBytes)
	}
	return result, nil
}

func compositionWorkspaceKey(workspaceID *string) string {
	if workspaceID == nil {
		return "g:"
	}
	return "w:" + *workspaceID
}

func compositionWorkspaceLabel(key string) string {
	if key == "g:" {
		return "global"
	}
	return strings.TrimPrefix(key, "w:")
}

func (s *compositionState) expandMarkers(
	ctx context.Context,
	entry *store.SkillRegistryEntry,
	markdown string,
	byID map[string]skills.SkillInclude,
	used map[string]bool,
	depth int,
) (string, error) {
	var out strings.Builder
	fenceChar, fenceLen := byte(0), 0
	for _, line := range splitLinesAfter(markdown) {
		trimmed := strings.TrimSpace(strings.TrimRight(line, "\r\n"))
		if nextChar, nextLen, boundary := advanceFence(trimmed, fenceChar, fenceLen); boundary {
			fenceChar, fenceLen = nextChar, nextLen
			out.WriteString(line)
			continue
		}
		if fenceChar != 0 {
			out.WriteString(line)
			continue
		}
		match := includeMarkerRE.FindStringSubmatch(trimmed)
		if len(match) > 0 && !validCompositionMarkerName(match[1]) {
			match = nil
		}
		if len(match) == 0 {
			if strings.HasPrefix(trimmed, "<!-- mcpx:include") {
				return "", fmt.Errorf("composition %s@v%d: malformed include marker %q",
					entry.Name, entry.Version, trimmed)
			}
			out.WriteString(line)
			continue
		}
		id := match[1]
		include, exists := byID[id]
		if !exists {
			return "", fmt.Errorf("composition %s@v%d: marker references undeclared include %q",
				entry.Name, entry.Version, id)
		}
		if used[id] {
			return "", fmt.Errorf("composition %s@v%d: include marker %q appears more than once",
				entry.Name, entry.Version, id)
		}
		used[id] = true
		if len(s.edges) >= MaxCompositionEdges {
			return "", fmt.Errorf("composition edge count exceeds %d at %s@v%d include %q",
				MaxCompositionEdges, entry.Name, entry.Version, id)
		}

		var workspaceID *string
		if include.Scope == "same" {
			workspaceID = entry.WorkspaceID
		}
		target, err := s.registry.store.GetSkillRegistryEntry(ctx, workspaceID, include.Skill, include.Version)
		if err != nil {
			return "", fmt.Errorf("composition %s@v%d include %q: resolve %s/%s@v%d: %w",
				entry.Name, entry.Version, id, include.Scope, include.Skill, include.Version, err)
		}
		if target.ContentHash != include.ContentHash {
			return "", fmt.Errorf("composition %s@v%d include %q: content hash mismatch for %s@v%d: pinned %s, found %s",
				entry.Name, entry.Version, id, include.Skill, include.Version,
				include.ContentHash, target.ContentHash)
		}
		if !compositionTargetIsInlineText(target) {
			return "", fmt.Errorf(
				"composition %s@v%d include %q targets %s@v%d with bundle/sidecar assets (source_type=%q); v1 composition merges prose only, so flatten/copy the prose or publish a text-only inline fragment",
				entry.Name, entry.Version, id, target.Name, target.Version, target.SourceType,
			)
		}
		s.edges = append(s.edges, CompositionEdge{
			FromName:    entry.Name,
			FromVersion: entry.Version,
			IncludeID:   id,
			ToName:      target.Name,
			ToVersion:   target.Version,
			Scope:       include.Scope,
			ContentHash: target.ContentHash,
			Section:     include.Section,
			Depth:       depth + 1,
		})
		fragment, err := s.renderNode(ctx, target, include.Section, depth+1)
		if err != nil {
			return "", err
		}
		out.WriteString(fragment)
		if strings.HasSuffix(line, "\n") && !strings.HasSuffix(fragment, "\n") {
			out.WriteByte('\n')
		}
	}
	return out.String(), nil
}

// compositionTargetIsInlineText deliberately permits only entries whose body
// is the complete artifact. V1 expansion has no asset namespace or merge
// contract, so accepting bundle, path, git, or unknown source forms would
// silently produce runnable prose with missing scripts or references.
func compositionTargetIsInlineText(entry *store.SkillRegistryEntry) bool {
	if strings.TrimSpace(entry.BundleSHA256) != "" || strings.TrimSpace(entry.SourcePath) != "" {
		return false
	}
	sourceType := strings.TrimSpace(entry.SourceType)
	return sourceType == "" || sourceType == "inline"
}
