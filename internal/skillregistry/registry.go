// Package skillregistry implements the agent-facing skills registry —
// a per-machine catalog of agentskills.io-format SKILL.md documents that
// any connected agent can search by intent ("I need to do X"), fetch in
// full, contribute new versions of, or iterate on.
//
// # Versioning
//
// Linear monotonic ints per skill name (v1, v2, …). content_hash dedups
// identical re-publishes (returns the existing version). parent_version
// records edit lineage for diff/rollback. The well-known tag @latest is
// derived (never stored); @stable is admin-curated (see SetTag).
//
// # Retrieval
//
// MVP uses the in-process internal/embedding TF-IDF index over
// (name + description + tags + body). The index is rebuilt lazily on the
// next Search() after any successful Publish(). Brute-force cosine over
// up to ~1k entries takes <50ms — plenty for the load this registry
// experiences.
//
// Upgrade path (NOT IMPLEMENTED): swap the embedding.Index for an
// SQLite FTS5 + sqlite-vec hybrid backed by Ollama nomic-embed-text,
// optionally reranked by Haiku 4.5 over the top-10 candidates. Keep the
// Search() interface stable so the swap is local to this file.
//
// # Safety
//
//   - Body capped at MaxBodyBytes (parse.go).
//   - Tag operations are admin-only (CWD-gated by the caller).
//   - Skill body becomes agent context on Get; treat all non-system
//     entries as untrusted prompt-injection vectors. Per-machine threat
//     model: the registry sees only what local agents publish, so the
//     blast radius is the local user. Phase 2 may add an approval gate
//     on first read of agent-authored entries.
//
// # Mesh isolation
//
// The registry is NOT auto-shared across paired libp2p peers. mesh__
// offer_skill / request_skill operate on signed .mcskill bundles
// (InstalledSkill, ADR 0002), not registry entries. Cross-device
// registry sync is out of scope for now.
package skillregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/don-works/mcplexer/internal/embedding"
	"github.com/don-works/mcplexer/internal/store"
)

// ManifestExtraStashKey is the reserved key under which the W4 extras
// JSON is round-tripped inside the SkillRegistryEntry.MetadataJSON
// blob. It exists because the store-level entry struct (in
// internal/store/models.go) is owned by parallel work and cannot grow
// a new field for this milestone. The sqlite layer extracts the value
// under this key into the dedicated `manifest_extra` column on write
// and re-injects it on read — see internal/store/sqlite/skill_registry.go.
//
// Callers fetching an entry should reach for ExtraFromEntry rather than
// parsing MetadataJSON themselves; the helper centralises the key.
const ManifestExtraStashKey = "__manifest_extra"

// Registry is the singleton service exposed by the gateway as the
// mcpx__skill_* tools. Constructed once in serve.go.
//
// The index is keyed by scope: queries from different workspaces see
// different head sets (workspace skills shadow globals of the same
// name), so a single index can't serve everyone. Per-scope indexes
// are built lazily on first use and invalidated by any write.
type Registry struct {
	store    store.SkillRegistryStore
	embedder EmbedProvider

	mu           sync.RWMutex
	indexes      map[string]*scopedIndex
	deleteGuards []DeleteGuard

	// mutationMu serializes the complete publish-validation-insert and
	// guard-delete sequences. Without it, a publish could validate a pin,
	// then race a dependency delete before inserting the dependent row.
	// It is deliberately separate from mu: rendering and guards read registry
	// state and cache invalidation acquires mu.
	mutationMu sync.Mutex
}

type scopedIndex struct {
	idx        *embedding.Index
	entries    []store.SkillRegistryEntry
	vectors    map[string][]float32
	embedModel string
	vectorOK   bool
}

// EmbedProvider is the optional semantic-vector hook used by skill
// search. It intentionally mirrors memory.EmbedProvider without importing
// the memory package into the registry core.
type EmbedProvider interface {
	Embed(ctx context.Context, inputs []string) (vectors [][]float32, model string, err error)
	HasModel() bool
}

// DeleteGuard can veto a registry deletion before state changes. Composition
// layers use this seam to protect versions pinned by includes. Guards must be
// read-only and should return a descriptive error when a reference blocks the
// deletion. version=0 means every active version in the exact scope.
type DeleteGuard func(ctx context.Context, workspaceID *string, name string, version int) error

// New returns a Registry backed by s. The index is built lazily on the
// first Search() — startup stays cheap.
func New(s store.SkillRegistryStore) *Registry {
	registry := &Registry{store: s, indexes: map[string]*scopedIndex{}}
	registry.AddDeleteGuard(registry.guardCompositionReferences)
	return registry
}

// SetEmbedder wires an optional vector embedder. Nil or HasModel=false
// leaves search on the local TF-IDF fallback. Changing the embedder
// invalidates cached per-scope search indexes so vectors refresh on the
// next query.
func (r *Registry) SetEmbedder(embedder EmbedProvider) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.embedder = embedder
	r.indexes = map[string]*scopedIndex{}
	r.mu.Unlock()
}

// AddDeleteGuard registers a pre-delete integrity check. Guards run in
// registration order against a snapshot, outside the Registry mutex.
func (r *Registry) AddDeleteGuard(guard DeleteGuard) {
	if r == nil || guard == nil {
		return
	}
	r.mu.Lock()
	r.deleteGuards = append(r.deleteGuards, guard)
	r.mu.Unlock()
}

// GlobalScope is the convenience scope for "global rows only".
func GlobalScope() store.SkillScope { return store.SkillScope{} }

// AdminScope is the bypass scope for admin operations.
func AdminScope() store.SkillScope { return store.SkillScope{IncludeAll: true} }

// PublishResult describes the outcome of a Publish call.
type PublishResult struct {
	Name         string `json:"name"`
	Version      int    `json:"version"`
	ContentHash  string `json:"content_hash"`
	Action       string `json:"action"` // "created" | "deduped"
	BundleSize   int    `json:"bundle_size,omitempty"`
	BundleSHA256 string `json:"bundle_sha256,omitempty"`
}

// PublishOptions carries the agent's publish request.
type PublishOptions struct {
	Name             string
	Body             string
	ParentVersion    *int
	Author           string // free-form, agent-supplied identity hint
	CreatedByAgentID string // session id from gateway

	// WorkspaceID nil = global. The MCP tool maps "scope: workspace" to
	// the session's resolved workspace, "scope: global" to nil, and
	// "scope: auto" picks workspace if the session has one else global.
	WorkspaceID *string

	// SourcePath, when non-empty, marks this as a "path" source skill —
	// the SKILL.md is the inline body but assets (scripts/, reference/)
	// live on disk at this path.
	SourcePath string

	// SourceTypeOverride lets importers tag the row as "git" (or other
	// future types) when SourcePath alone isn't enough to express
	// provenance. Empty string defaults to "inline" or "path" based on
	// SourcePath.
	SourceTypeOverride string

	// MetadataExtras are merged into the parsed-frontmatter metadata
	// blob before the row is written. Used by the git importer to
	// record (url, ref, commit) without modifying the user's SKILL.md.
	MetadataExtras map[string]any

	// Description overrides the frontmatter description. Optional.
	Description string

	// Bundle, when non-empty, is the tar.gz of the full skill directory.
	// Publish validates that the SKILL.md inside the tar.gz exactly
	// equals Body (so the search index and skill_get text response are
	// always consistent with what skill_install would extract). When
	// present, SourceType becomes "bundle" unless explicitly overridden.
	// Capped at MaxBundleBytes — larger inputs are rejected.
	Bundle []byte
}

// MaxBundleBytes is the cap on the tar.gz payload at publish time.
// Lines up with the user's 25 MB choice; the p2p layer's
// MaxSkillBundleBytes (100 MB) sits above it so paired peers can
// still receive bundles published with a future, more generous cap.
const MaxBundleBytes = 25 * 1024 * 1024

// Publish parses, validates, and stores a SKILL.md. Idempotent on
// content_hash — re-publishing identical content returns the existing
// version with action="deduped". Otherwise inserts a new version and
// returns action="created".
//
// After a successful create, the in-memory search index is invalidated
// so the next Search() rebuilds with the new row.
func (r *Registry) Publish(ctx context.Context, opts PublishOptions) (*PublishResult, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	entry, err := r.buildEntryForPublish(opts)
	if err != nil {
		return nil, err
	}
	if _, err := r.RenderEntry(ctx, entry); err != nil {
		return nil, fmt.Errorf("publish composition validation: %w", err)
	}
	dedup, err := r.store.PublishSkillRegistryEntry(ctx, entry)
	if err != nil {
		return nil, fmt.Errorf("publish: %w", err)
	}

	action := "created"
	if dedup {
		action = "deduped"
	} else {
		r.invalidate()
	}
	return &PublishResult{
		Name:         entry.Name,
		Version:      entry.Version,
		ContentHash:  entry.ContentHash,
		Action:       action,
		BundleSize:   len(entry.Bundle),
		BundleSHA256: entry.BundleSHA256,
	}, nil
}

// buildEntryForPublish parses the SKILL.md body and assembles the
// store.SkillRegistryEntry that PublishSkillRegistryEntry will insert.
// When opts.Bundle is non-empty, validates the tar.gz and stamps the
// row's source_type as "bundle" (unless explicitly overridden).
func (r *Registry) buildEntryForPublish(opts PublishOptions) (*store.SkillRegistryEntry, error) {
	sourceType := "inline"
	if opts.SourcePath != "" {
		sourceType = "path"
	}
	if len(opts.Bundle) > 0 {
		sourceType = "bundle"
	}
	if opts.SourceTypeOverride != "" {
		sourceType = opts.SourceTypeOverride
	}
	parsed, err := Parse(opts.Body, opts.Name)
	if err != nil {
		return nil, err
	}
	desc := parsed.Description
	if strings.TrimSpace(opts.Description) != "" {
		desc = opts.Description
	}
	metaJSON := mergeMetadata(parsed.MetadataJSON, opts.MetadataExtras)
	metaJSON, err = stashManifestExtra(metaJSON, parsed.Extra)
	if err != nil {
		return nil, err
	}
	entry := &store.SkillRegistryEntry{
		Name:             parsed.Name,
		ContentHash:      parsed.ContentHash,
		Description:      desc,
		Body:             parsed.Body,
		MetadataJSON:     metaJSON,
		TagsJSON:         parsed.TagsJSON,
		Author:           opts.Author,
		ParentVersion:    opts.ParentVersion,
		CreatedByAgentID: opts.CreatedByAgentID,
		WorkspaceID:      opts.WorkspaceID,
		SourceType:       sourceType,
		SourcePath:       opts.SourcePath,
	}
	if len(opts.Bundle) > 0 {
		sha, err := ValidateBundle(opts.Bundle, parsed.Body)
		if err != nil {
			return nil, fmt.Errorf("bundle: %w", err)
		}
		entry.Bundle = opts.Bundle
		entry.BundleSHA256 = sha
	}
	return entry, nil
}

// VersionRef is "latest", "stable", or an explicit int.
type VersionRef struct {
	Latest  bool
	Stable  bool
	Version int
}

// String renders a ref for log / tool-result text. "latest" when nothing
// is set, matching ParseVersionRef's default.
func (r VersionRef) String() string {
	switch {
	case r.Stable:
		return "stable"
	case r.Version > 0:
		return fmt.Sprintf("v%d", r.Version)
	default:
		return "latest"
	}
}

// ParseVersionRef accepts an int (>0), the literal "latest" / "stable",
// or empty (= latest).
func ParseVersionRef(v any) (VersionRef, error) {
	switch x := v.(type) {
	case nil:
		return VersionRef{Latest: true}, nil
	case string:
		s := strings.ToLower(strings.TrimSpace(x))
		switch s {
		case "", "latest", "@latest":
			return VersionRef{Latest: true}, nil
		case "stable", "@stable":
			return VersionRef{Stable: true}, nil
		}
		s = strings.TrimPrefix(s, "v")
		if n, err := strconv.Atoi(s); err == nil {
			if n <= 0 {
				return VersionRef{}, fmt.Errorf("invalid version ref %d: must be positive", n)
			}
			return VersionRef{Version: n}, nil
		}
		return VersionRef{}, fmt.Errorf("invalid version ref %q: expected int, \"latest\", or \"stable\"", x)
	case float64:
		n := int(x)
		if float64(n) != x || n <= 0 {
			return VersionRef{}, fmt.Errorf("invalid version ref %v: must be a positive integer", x)
		}
		return VersionRef{Version: n}, nil
	case int:
		if x <= 0 {
			return VersionRef{}, fmt.Errorf("invalid version ref %d: must be positive", x)
		}
		return VersionRef{Version: x}, nil
	default:
		return VersionRef{}, fmt.Errorf("unsupported version ref type %T", v)
	}
}

// Get resolves a (scope, name, ref) to one entry. Workspace skills
// shadow globals of the same name when scope.WorkspaceIDs lists them.
func (r *Registry) Get(ctx context.Context, scope store.SkillScope, name string, ref VersionRef) (*store.SkillRegistryEntry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	switch {
	case ref.Latest:
		return r.store.GetSkillRegistryHead(ctx, scope, name)
	case ref.Stable:
		t, err := r.store.GetSkillRegistryTag(ctx, name, "@stable")
		if err != nil {
			return nil, err
		}
		// @stable points at one specific version; resolve it honouring
		// scope visibility + workspace shadowing.
		if e, err := r.resolveVersionInScope(ctx, scope, name, t.Version); err == nil {
			return e, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		// The pinned version is no longer visible (e.g. soft-deleted with
		// no tag cleanup). Self-heal by falling back to the head rather
		// than leaving @stable as a dangling pointer forever.
		return r.store.GetSkillRegistryHead(ctx, scope, name)
	case ref.Version > 0:
		return r.resolveVersionInScope(ctx, scope, name, ref.Version)
	default:
		return r.store.GetSkillRegistryHead(ctx, scope, name)
	}
}

// resolveVersionInScope returns the (name, version) row visible in scope,
// applying the same workspace-shadowing rule as GetSkillRegistryHead: the
// first matching WorkspaceIDs entry wins, followed by global. Honours
// scope.IncludeAll (admin) by delegating to a
// scope-aware version listing rather than only probing global + the
// explicit WorkspaceIDs — that closes the blind spot where AdminScope()
// (IncludeAll:true, empty WorkspaceIDs) could only ever see global rows.
func (r *Registry) resolveVersionInScope(
	ctx context.Context, scope store.SkillScope, name string, version int,
) (*store.SkillRegistryEntry, error) {
	versions, err := r.store.ListSkillRegistryVersions(ctx, scope, name, false)
	if err != nil {
		return nil, err
	}
	var best *store.SkillRegistryEntry
	bestRank := int(^uint(0) >> 1)
	bestTie := ""
	for i := range versions {
		e := versions[i]
		if e.Version != version {
			continue
		}
		rank, tie, visible := versionScopeRank(scope, e.WorkspaceID)
		if visible && (best == nil || rank < bestRank || (rank == bestRank && tie < bestTie)) {
			row := e
			best = &row
			bestRank, bestTie = rank, tie
		}
	}
	if best == nil {
		return nil, store.ErrNotFound
	}
	return best, nil
}

func versionScopeRank(scope store.SkillScope, workspaceID *string) (int, string, bool) {
	if scope.IncludeAll {
		if workspaceID == nil {
			return 1, "", true
		}
		return 0, *workspaceID, true
	}
	if workspaceID == nil {
		return len(scope.WorkspaceIDs), "", true
	}
	for rank, candidate := range scope.WorkspaceIDs {
		if candidate == *workspaceID {
			return rank, candidate, true
		}
	}
	return 0, "", false
}

// SearchHit is one ranked match.
type SearchHit struct {
	Name        string  `json:"name"`
	Version     int     `json:"version"`
	Description string  `json:"description"`
	Score       float64 `json:"score"`
	Scope       string  `json:"scope,omitempty"` // "global" or workspace_id
}

func scopeLabel(workspaceID *string) string {
	if workspaceID == nil {
		return "global"
	}
	return *workspaceID
}

// mergeMetadata folds extras into the JSON-encoded metadata map,
// returning a re-encoded blob. Existing keys in the metadata are
// preserved unless extras names them; extras win on conflict so
// importers can override stale frontmatter values (e.g. git_commit).
//
// Falls back to the original blob if anything fails to round-trip —
// metadata is best-effort, never blocking on a malformed map.
func mergeMetadata(metaJSON json.RawMessage, extras map[string]any) json.RawMessage {
	if len(extras) == 0 {
		return metaJSON
	}
	merged := map[string]any{}
	if len(metaJSON) > 0 {
		_ = json.Unmarshal(metaJSON, &merged)
		if merged == nil {
			merged = map[string]any{}
		}
	}
	for k, v := range extras {
		merged[k] = v
	}
	out, err := json.Marshal(merged)
	if err != nil {
		return metaJSON
	}
	return out
}

// Search returns up to limit ranked matches for the natural-language
// query, scoped to the given visibility set. Only head versions are
// indexed. Returns an empty slice (not an error) when nothing matches.
func (r *Registry) Search(ctx context.Context, scope store.SkillScope, query string, limit int) ([]SearchHit, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}

	idx, err := r.ensureIndex(ctx, scope)
	if err != nil {
		return nil, err
	}
	if idx == nil || len(idx.entries) == 0 {
		return nil, nil
	}

	results := r.searchIndex(ctx, idx, query, limit)
	out := make([]SearchHit, 0, len(results))
	byID := make(map[string]store.SkillRegistryEntry, len(idx.entries))
	for _, e := range idx.entries {
		byID[e.ID] = e
	}
	for _, hit := range results {
		e, ok := byID[hit.ID]
		if !ok {
			continue
		}
		out = append(out, SearchHit{
			Name:        e.Name,
			Version:     e.Version,
			Description: e.Description,
			Score:       hit.Score,
			Scope:       scopeLabel(e.WorkspaceID),
		})
	}
	return out, nil
}

// ListHeads returns one row per skill name (head version) visible in scope.
func (r *Registry) ListHeads(ctx context.Context, scope store.SkillScope, limit int) ([]store.SkillRegistryEntry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	return r.store.ListSkillRegistryHeads(ctx, scope, limit)
}

// ListScopeHeads returns the latest active row for every distinct scope/name
// pair. It intentionally preserves shadowed rows for admin inventory and audit.
func (r *Registry) ListScopeHeads(
	ctx context.Context, scope store.SkillScope, limit int,
) ([]store.SkillRegistryEntry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	return r.store.ListSkillRegistryScopeHeads(ctx, scope, limit)
}

// ListVersions returns every version of name in scope, descending.
func (r *Registry) ListVersions(ctx context.Context, scope store.SkillScope, name string) ([]store.SkillRegistryEntry, error) {
	if r == nil || r.store == nil {
		return nil, errors.New("skillregistry: not initialised")
	}
	return r.store.ListSkillRegistryVersions(ctx, scope, name, false)
}

// SoftDelete soft-deletes one (or all) version(s) of (workspace, name).
// version=0 drops every active row for that scope+name.
func (r *Registry) SoftDelete(ctx context.Context, workspaceID *string, name string, version int) error {
	if r == nil || r.store == nil {
		return errors.New("skillregistry: not initialised")
	}
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	if err := r.runDeleteGuards(ctx, workspaceID, name, version); err != nil {
		return fmt.Errorf("delete blocked: %w", err)
	}
	if err := r.store.SoftDeleteSkillRegistryEntry(ctx, workspaceID, name, version); err != nil {
		return err
	}
	r.reconcileStableTag(ctx, name, version)
	r.invalidate()
	return nil
}

func (r *Registry) runDeleteGuards(
	ctx context.Context, workspaceID *string, name string, version int,
) error {
	r.mu.RLock()
	guards := append([]DeleteGuard(nil), r.deleteGuards...)
	r.mu.RUnlock()
	for _, guard := range guards {
		if err := guard(ctx, workspaceID, name, version); err != nil {
			return err
		}
	}
	return nil
}

// reconcileStableTag repairs a dangling @stable pointer after a delete.
// SoftDelete does not touch the (global) tag table, so a @stable tag
// left pointing at a just-deleted version would make Get(@stable) chase
// a soft-deleted row. When the deleted version is the one @stable points
// at (version=0 drops every active version, so any @stable pointer is
// now stale), we re-point @stable at the new head if one survives, or
// drop the tag entirely if the whole skill is gone. Best-effort: tag
// reconciliation must never fail the delete, and Get(@stable) also falls
// back to head as a second backstop.
func (r *Registry) reconcileStableTag(ctx context.Context, name string, version int) {
	t, err := r.store.GetSkillRegistryTag(ctx, name, "@stable")
	if err != nil {
		return // no @stable tag, or lookup failed — nothing to reconcile
	}
	if version != 0 && t.Version != version {
		return // @stable points elsewhere; the deleted version is unrelated
	}
	// The pinned version is gone. Re-point @stable at the surviving head
	// (admin scope so a workspace-shadowed head is found too); if no head
	// survives, the skill is fully deleted — drop the dangling tag.
	head, err := r.store.GetSkillRegistryHead(ctx, AdminScope(), name)
	if err != nil || head == nil {
		_ = r.store.DeleteSkillRegistryTag(ctx, name, "@stable")
		return
	}
	_ = r.store.SetSkillRegistryTag(ctx, &store.SkillRegistryTag{
		Name:    name,
		Tag:     "@stable",
		Version: head.Version,
		SetBy:   t.SetBy,
	})
}

// SetTag points (name, tag) at a specific version. Tries global first
// then each visible workspace until a row is found. Rejects @latest.
func (r *Registry) SetTag(ctx context.Context, scope store.SkillScope, name, tag string, version int, setBy string) error {
	if r == nil || r.store == nil {
		return errors.New("skillregistry: not initialised")
	}
	tag = normalizeTag(tag)
	if tag == "@latest" {
		return errors.New("@latest is derived, not stored")
	}
	// Verify target row exists in some visible scope.
	if _, err := r.store.GetSkillRegistryEntry(ctx, nil, name, version); err == nil {
		// found global
	} else {
		found := false
		for _, wsID := range scope.WorkspaceIDs {
			ws := wsID
			if _, err := r.store.GetSkillRegistryEntry(ctx, &ws, name, version); err == nil {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("verify target: %w", err)
		}
	}
	return r.store.SetSkillRegistryTag(ctx, &store.SkillRegistryTag{
		Name:    name,
		Tag:     tag,
		Version: version,
		SetBy:   setBy,
	})
}

// DeleteTag removes a (name, tag) row.
func (r *Registry) DeleteTag(ctx context.Context, name, tag string) error {
	if r == nil || r.store == nil {
		return errors.New("skillregistry: not initialised")
	}
	return r.store.DeleteSkillRegistryTag(ctx, name, normalizeTag(tag))
}

// Invalidate clears every cached index so the next Search rebuilds.
// Exposed for the seeder and tests.
func (r *Registry) Invalidate() { r.invalidate() }

func (r *Registry) invalidate() {
	r.mu.Lock()
	r.indexes = map[string]*scopedIndex{}
	r.mu.Unlock()
}

func scopeKey(scope store.SkillScope) string {
	if scope.IncludeAll {
		return "*"
	}
	if len(scope.WorkspaceIDs) == 0 {
		return "global"
	}
	// Workspace order is semantic (child before parent), so it must remain
	// part of the cache key rather than being sorted as an unordered set.
	return strings.Join(scope.WorkspaceIDs, "|")
}

func (r *Registry) ensureIndex(ctx context.Context, scope store.SkillScope) (*scopedIndex, error) {
	key := scopeKey(scope)
	r.mu.RLock()
	if cached, ok := r.indexes[key]; ok && cached != nil && cached.idx != nil {
		r.mu.RUnlock()
		return cached, nil
	}
	r.mu.RUnlock()

	heads, err := r.store.ListSkillRegistryHeads(ctx, scope, 0)
	if err != nil {
		return nil, fmt.Errorf("list heads: %w", err)
	}
	docs := make([]embedding.Document, 0, len(heads))
	texts := make([]string, 0, len(heads))
	for _, e := range heads {
		text := indexText(e)
		docs = append(docs, embedding.Document{
			ID:   e.ID,
			Text: text,
		})
		texts = append(texts, text)
	}
	built := embedding.NewIndex(docs)
	scoped := &scopedIndex{idx: built, entries: heads}
	r.populateVectors(ctx, scoped, texts)

	r.mu.Lock()
	r.indexes[key] = scoped
	r.mu.Unlock()
	return scoped, nil
}

func indexText(e store.SkillRegistryEntry) string {
	var tags []string
	if len(e.TagsJSON) > 0 {
		_ = json.Unmarshal(e.TagsJSON, &tags)
	}
	sort.Strings(tags)
	var b strings.Builder
	b.WriteString(e.Name)
	b.WriteByte('\n')
	b.WriteString(e.Description)
	b.WriteByte('\n')
	if len(tags) > 0 {
		b.WriteString(strings.Join(tags, " "))
		b.WriteByte('\n')
	}
	if len(e.MetadataJSON) > 0 {
		b.WriteString(string(e.MetadataJSON))
		b.WriteByte('\n')
	}
	if extra := ExtraFromEntry(&e); !extra.IsZero() {
		if raw, err := json.Marshal(extra); err == nil {
			b.Write(raw)
			b.WriteByte('\n')
		}
	}
	b.WriteString(excerpt(e.Body, 12000))
	return b.String()
}

func excerpt(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
}

func (r *Registry) populateVectors(ctx context.Context, idx *scopedIndex, texts []string) {
	if idx == nil || len(texts) == 0 {
		return
	}
	embedder := r.currentEmbedder()
	if embedder == nil || !embedder.HasModel() {
		return
	}
	vecs, model, err := embedder.Embed(ctx, texts)
	if err != nil || len(vecs) != len(idx.entries) {
		return
	}
	idx.vectors = make(map[string][]float32, len(idx.entries))
	for i, e := range idx.entries {
		if len(vecs[i]) > 0 {
			idx.vectors[e.ID] = vecs[i]
		}
	}
	idx.embedModel = model
	idx.vectorOK = len(idx.vectors) > 0
}

func (r *Registry) currentEmbedder() EmbedProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.embedder
}

func (r *Registry) searchIndex(
	ctx context.Context, idx *scopedIndex, query string, limit int,
) []embedding.SearchResult {
	lexical := idx.idx.Search(query, limit*3)
	if !idx.vectorOK {
		return capResults(lexical, limit)
	}
	embedder := r.currentEmbedder()
	if embedder == nil || !embedder.HasModel() {
		return capResults(lexical, limit)
	}
	vecs, _, err := embedder.Embed(ctx, []string{query})
	if err != nil || len(vecs) == 0 || len(vecs[0]) == 0 {
		return capResults(lexical, limit)
	}
	return fuseSkillResults(lexical, idx.vectors, vecs[0], limit)
}

func capResults(in []embedding.SearchResult, limit int) []embedding.SearchResult {
	if limit > 0 && len(in) > limit {
		return in[:limit]
	}
	return in
}

func fuseSkillResults(
	lexical []embedding.SearchResult,
	vectors map[string][]float32,
	query []float32,
	limit int,
) []embedding.SearchResult {
	scores := make(map[string]float64, len(lexical)+len(vectors))
	for _, hit := range lexical {
		scores[hit.ID] += 0.55 * hit.Score
	}
	for id, vec := range vectors {
		if sim := cosineFloat32(query, vec); sim > 0 {
			scores[id] += 0.45 * sim
		}
	}
	out := make([]embedding.SearchResult, 0, len(scores))
	for id, score := range scores {
		if score > 0 {
			out = append(out, embedding.SearchResult{ID: id, Score: score})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score == out[j].Score {
			return out[i].ID < out[j].ID
		}
		return out[i].Score > out[j].Score
	})
	return capResults(out, limit)
}

func cosineFloat32(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		av, bv := float64(a[i]), float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

func normalizeTag(tag string) string {
	t := strings.TrimSpace(strings.ToLower(tag))
	if t == "" {
		return ""
	}
	if !strings.HasPrefix(t, "@") {
		t = "@" + t
	}
	return t
}
