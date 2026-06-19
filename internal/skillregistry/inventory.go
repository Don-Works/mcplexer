package skillregistry

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/don-works/mcplexer/internal/store"
)

type SourceKind string

const (
	SourceRegistry  SourceKind = "registry"
	SourceLocalDir  SourceKind = "local-dir"
	SourceInstalled SourceKind = "installed"
)

type InventoryEntry struct {
	Name         string     `json:"name"`
	Description  string     `json:"description"`
	Version      int        `json:"version,omitempty"`
	ContentHash  string     `json:"content_hash"`
	SourceKind   SourceKind `json:"source_kind"`
	SourcePath   string     `json:"source_path,omitempty"`
	RegistryName string     `json:"registry_name,omitempty"`
	Scope        string     `json:"scope,omitempty"`
	Managed      bool       `json:"managed"`
	HasBundle    bool       `json:"has_bundle"`
	BundleSHA256 string     `json:"bundle_sha256,omitempty"`
	ParseError   string     `json:"parse_error,omitempty"`
}

type InventoryOptions struct {
	SourceDirs []string
	Scope      store.SkillScope
	Query      string
	Limit      int
}

const DefaultInventoryLimit = 50
const MaxInventoryLimit = 200

func (r *Registry) Inventory(ctx context.Context, opts InventoryOptions) ([]InventoryEntry, error) {
	if r == nil || r.store == nil {
		return nil, nil
	}
	if opts.Limit <= 0 {
		opts.Limit = DefaultInventoryLimit
	}
	if opts.Limit > MaxInventoryLimit {
		opts.Limit = MaxInventoryLimit
	}

	seen := map[string]*InventoryEntry{}

	heads, err := r.store.ListSkillRegistryHeads(ctx, opts.Scope, 0)
	if err == nil {
		for i := range heads {
			e := &heads[i]
			ie := &InventoryEntry{
				Name:         e.Name,
				Description:  e.Description,
				Version:      e.Version,
				ContentHash:  e.ContentHash,
				SourceKind:   SourceRegistry,
				RegistryName: e.Name,
				Managed:      true,
				HasBundle:    e.BundleSHA256 != "",
				BundleSHA256: e.BundleSHA256,
			}
			if e.WorkspaceID != nil {
				ie.Scope = *e.WorkspaceID
			} else {
				ie.Scope = "global"
			}
			if e.SourcePath != "" {
				ie.SourcePath = e.SourcePath
			}
			seen[strings.ToLower(e.Name)] = ie
		}
	}

	for _, dir := range opts.SourceDirs {
		r.walkLocalDir(ctx, dir, seen)
	}

	out := make([]InventoryEntry, 0, len(seen))
	for _, v := range seen {
		out = append(out, *v)
	}

	if opts.Query != "" {
		out = lexicalFilterInventory(out, opts.Query)
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})

	if len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func (r *Registry) walkLocalDir(ctx context.Context, sourceDir string, seen map[string]*InventoryEntry) {
	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() || isHidden(e.Name()) {
			continue
		}
		dir := filepath.Join(sourceDir, e.Name())
		mdPath := filepath.Join(dir, "SKILL.md")
		if _, statErr := os.Stat(mdPath); statErr != nil {
			continue
		}
		ie := InventoryEntry{
			SourceKind: SourceLocalDir,
			SourcePath: dir,
			Managed:    false,
		}
		body, err := os.ReadFile(mdPath)
		if err != nil {
			ie.Name = e.Name()
			ie.ParseError = err.Error()
			key := "local:" + dir
			seen[key] = &ie
			continue
		}
		parsed, err := Parse(string(body), "")
		if err != nil {
			ie.Name = e.Name()
			ie.ParseError = err.Error()
			key := "local:" + dir
			seen[key] = &ie
			continue
		}
		ie.Name = parsed.Name
		ie.Description = parsed.Description
		ie.ContentHash = parsed.ContentHash
		lc := strings.ToLower(parsed.Name)
		if existing, ok := seen[lc]; ok && existing.SourceKind == SourceRegistry {
			existing.SourcePath = dir
			existing.Managed = true
			continue
		}
		if _, ok := seen[lc]; ok {
			continue
		}
		ie.Scope = "local"
		seen[lc] = &ie
	}
}

var inventoryStopWords = map[string]bool{
	"a": true, "an": true, "the": true, "and": true, "or": true,
	"for": true, "in": true, "on": true, "to": true, "of": true,
	"with": true, "is": true, "use": true, "when": true, "how": true,
}

func lexicalFilterInventory(entries []InventoryEntry, query string) []InventoryEntry {
	terms := tokenizeInventoryQuery(query)
	if len(terms) == 0 {
		return entries
	}
	type scored struct {
		entry InventoryEntry
		score int
	}
	var matches []scored
	for _, e := range entries {
		if e.ParseError != "" {
			continue
		}
		s := inventoryScore(e, terms)
		if s > 0 {
			matches = append(matches, scored{e, s})
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})
	out := make([]InventoryEntry, len(matches))
	for i, m := range matches {
		out[i] = m.entry
	}
	return out
}

func tokenizeInventoryQuery(query string) []string {
	fields := strings.Fields(strings.ToLower(query))
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		if !inventoryStopWords[f] {
			terms = append(terms, f)
		}
	}
	return terms
}

func inventoryScore(e InventoryEntry, terms []string) int {
	name := strings.ToLower(e.Name)
	desc := strings.ToLower(e.Description)
	s := 0
	for _, t := range terms {
		if strings.Contains(name, t) {
			s += 10
		}
		if strings.Contains(desc, t) {
			s += 5
		}
	}
	return s
}

type InventorySearchIndex struct {
	mu      sync.RWMutex
	entries []InventoryEntry
}

func NewInventorySearchIndex() *InventorySearchIndex {
	return &InventorySearchIndex{}
}

func (idx *InventorySearchIndex) Rebuild(entries []InventoryEntry) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	cp := make([]InventoryEntry, len(entries))
	copy(cp, entries)
	idx.entries = cp
}

func (idx *InventorySearchIndex) Search(query string, limit int) []InventoryEntry {
	if limit <= 0 {
		limit = DefaultInventoryLimit
	}
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	if idx.entries == nil {
		return nil
	}
	terms := tokenizeInventoryQuery(query)
	if len(terms) == 0 {
		if len(idx.entries) > limit {
			return idx.entries[:limit]
		}
		return idx.entries
	}
	return lexicalFilterInventory(idx.entries, query)
}
