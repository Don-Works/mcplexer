package api

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/skills"
	"github.com/don-works/mcplexer/internal/store"
)

// skillEnvelope wraps a *store.SkillRegistryEntry with the W4 typed
// extras pulled out for first-class consumption by the dashboard and
// any other API client. The struct is intentionally a value-type
// projection rather than a struct embed so the JSON ordering stays
// deterministic and `manifest_extra` always appears as a top-level
// key, even when the underlying entry's MetadataJSON is empty.
type skillEnvelope struct {
	*store.SkillRegistryEntry
	ManifestExtra skills.ManifestExtra `json:"manifest_extra"`
}

// wrap projects a store entry into the API envelope shape. Nil-safe
// (returns the zero envelope) so list/search handlers can use it
// without defensive checks.
func wrapSkillEntry(e *store.SkillRegistryEntry) skillEnvelope {
	if e == nil {
		return skillEnvelope{}
	}
	return skillEnvelope{
		SkillRegistryEntry: e,
		ManifestExtra:      skillregistry.ExtraFromEntry(e),
	}
}

// wrapSkillEntries is the slice version, used by list/versions handlers.
func wrapSkillEntries(in []store.SkillRegistryEntry) []skillEnvelope {
	out := make([]skillEnvelope, len(in))
	for i := range in {
		// Take a stable pointer per element (rangevar capture).
		entry := in[i]
		out[i] = wrapSkillEntry(&entry)
	}
	return out
}

// skillRegistryHandler exposes the agent-facing skills registry over the
// dashboard's REST surface so humans can browse, inspect versions, and
// manage tags from the web UI. The MCP tool surface (mcpx__skill_*) is
// the agent's path; this is the human's. They share the same Registry
// service so writes show up immediately in both.
type skillRegistryHandler struct {
	store    store.Store
	registry *skillregistry.Registry
}

func (h *skillRegistryHandler) list(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	scope := h.parseScope(r)
	heads, err := h.registry.ListHeads(r.Context(), scope, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list skills")
		return
	}
	if heads == nil {
		heads = []store.SkillRegistryEntry{}
	}
	writeJSON(w, http.StatusOK, wrapSkillEntries(heads))
}

// parseScope reads ?scope=all|global|workspace and ?workspace_id=…
// from the request. Default = all (admin view).
func (h *skillRegistryHandler) parseScope(r *http.Request) store.SkillScope {
	q := r.URL.Query()
	mode := q.Get("scope")
	switch mode {
	case "global":
		return store.SkillScope{}
	case "workspace":
		if id := q.Get("workspace_id"); id != "" {
			return store.SkillScope{WorkspaceIDs: []string{id}}
		}
		return store.SkillScope{}
	default:
		return skillregistry.AdminScope()
	}
}

func (h *skillRegistryHandler) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	versionStr := r.URL.Query().Get("version")
	includeBundle := r.URL.Query().Get("include_bundle") == "1" || r.URL.Query().Get("include_bundle") == "true"
	ref, err := parseGetVersionRef(versionStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entry, err := h.registry.Get(r.Context(), skillregistry.AdminScope(), name, ref)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "skill not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get skill")
		return
	}
	if includeBundle && entry.BundleSHA256 != "" {
		bundle, _, fetchErr := h.registry.FetchBundle(r.Context(), skillregistry.AdminScope(), name, ref)
		if fetchErr == nil && len(bundle) > 0 {
			writeJSON(w, http.StatusOK, struct {
				skillEnvelope
				BundleB64 string `json:"bundle_b64"`
			}{wrapSkillEntry(entry), base64.StdEncoding.EncodeToString(bundle)})
			return
		}
	}
	writeJSON(w, http.StatusOK, wrapSkillEntry(entry))
}

// parseGetVersionRef turns the ?version= query value into a VersionRef.
// Empty / "latest" → Latest, "stable" → Stable, integer → explicit.
func parseGetVersionRef(s string) (skillregistry.VersionRef, error) {
	if s == "" || s == "latest" {
		return skillregistry.VersionRef{Latest: true}, nil
	}
	if s == "stable" {
		return skillregistry.VersionRef{Stable: true}, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return skillregistry.VersionRef{}, errors.New("invalid version")
	}
	return skillregistry.VersionRef{Version: n}, nil
}

func (h *skillRegistryHandler) versions(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	versions, err := h.registry.ListVersions(r.Context(), skillregistry.AdminScope(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list versions")
		return
	}
	if versions == nil {
		versions = []store.SkillRegistryEntry{}
	}
	writeJSON(w, http.StatusOK, wrapSkillEntries(versions))
}

func (h *skillRegistryHandler) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "q is required")
		return
	}
	limit := 5
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	hits, err := h.registry.Search(r.Context(), skillregistry.AdminScope(), q, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}
	if hits == nil {
		hits = []skillregistry.SearchHit{}
	}
	writeJSON(w, http.StatusOK, hits)
}

type publishSkillRequest struct {
	Name          string  `json:"name"`
	Body          string  `json:"body"`
	ParentVersion *int    `json:"parent_version,omitempty"`
	Author        string  `json:"author,omitempty"`
	Scope         string  `json:"scope,omitempty"`        // "global" | "workspace"
	WorkspaceID   *string `json:"workspace_id,omitempty"` // required when scope=workspace
	BundleB64     string  `json:"bundle_b64,omitempty"`   // base64 tar.gz; ≤25 MiB after decode
}

func (h *skillRegistryHandler) publish(w http.ResponseWriter, r *http.Request) {
	var req publishSkillRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" || req.Body == "" {
		writeError(w, http.StatusBadRequest, "name and body are required")
		return
	}
	author := req.Author
	if author == "" {
		author = "dashboard"
	}
	var workspaceID *string
	if req.Scope == "workspace" {
		if req.WorkspaceID == nil || *req.WorkspaceID == "" {
			writeError(w, http.StatusBadRequest, "scope=workspace requires workspace_id")
			return
		}
		workspaceID = req.WorkspaceID
	}
	var bundle []byte
	if req.BundleB64 != "" {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(req.BundleB64))
		if err != nil {
			writeErrorDetail(w, http.StatusBadRequest, "invalid bundle_b64", err.Error())
			return
		}
		bundle = decoded
	}
	res, err := h.registry.Publish(r.Context(), skillregistry.PublishOptions{
		Name:          req.Name,
		Body:          req.Body,
		ParentVersion: req.ParentVersion,
		Author:        author,
		WorkspaceID:   workspaceID,
		Bundle:        bundle,
	})
	if err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "publish failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *skillRegistryHandler) delete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	version := 0
	if v := r.URL.Query().Get("version"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			version = n
		}
	}
	if err := h.registry.SoftDelete(r.Context(), nil, name, version); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "skill not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *skillRegistryHandler) bundleFileIndex(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	ref, err := parseGetVersionRef(r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entries, err := h.registry.BundleFileIndex(r.Context(), skillregistry.AdminScope(), name, ref)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, skillregistry.ErrBundleNotPresent) {
		writeError(w, http.StatusNotFound, "bundle not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to index bundle")
		return
	}
	if entries == nil {
		entries = []skillregistry.BundleFileEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}

func (h *skillRegistryHandler) bundleFileContent(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	ref, err := parseGetVersionRef(r.URL.Query().Get("version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filePath := r.URL.Query().Get("path")
	if filePath == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	fc, err := h.registry.BundleFileContent(r.Context(), skillregistry.AdminScope(), name, ref, filePath)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "file not found")
		return
	}
	if errors.Is(err, skillregistry.ErrBundleNotPresent) {
		writeError(w, http.StatusNotFound, "bundle not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, fc)
}

func (h *skillRegistryHandler) versionDiff(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	oldRef, err := parseGetVersionRef(r.URL.Query().Get("old_version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "old_version: "+err.Error())
		return
	}
	newRef, err := parseGetVersionRef(r.URL.Query().Get("new_version"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "new_version: "+err.Error())
		return
	}
	diff, err := h.registry.DiffVersions(r.Context(), skillregistry.AdminScope(), name, oldRef, newRef)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "skill version not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "diff failed")
		return
	}
	writeJSON(w, http.StatusOK, diff)
}

type setTagRequest struct {
	Tag     string `json:"tag"`
	Version int    `json:"version"`
}

func (h *skillRegistryHandler) setTag(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	var req setTagRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Tag == "" || req.Version <= 0 {
		writeError(w, http.StatusBadRequest, "tag and positive version required")
		return
	}
	if err := h.registry.SetTag(r.Context(), skillregistry.AdminScope(), name, req.Tag, req.Version, "dashboard"); err != nil {
		writeErrorDetail(w, http.StatusBadRequest, "set tag failed", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *skillRegistryHandler) inventory(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	scope := h.parseScope(r)

	var sourceDirs []string
	if dirs := r.URL.Query().Get("source_dirs"); dirs != "" {
		sourceDirs = strings.Split(dirs, ",")
	}

	entries, err := h.registry.Inventory(r.Context(), skillregistry.InventoryOptions{
		Scope:      scope,
		SourceDirs: sourceDirs,
		Query:      q,
		Limit:      limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "inventory failed")
		return
	}
	if entries == nil {
		entries = []skillregistry.InventoryEntry{}
	}
	writeJSON(w, http.StatusOK, entries)
}
