// Package api — worker_templates_handler.go exposes the publishable-
// Worker-template surface over HTTP. Templates live in the worker_templates
// table (migration 057); this handler is a thin adapter between the
// workertemplates registry + the admin Service so the dashboard can:
//
//	POST   /api/v1/workers/{id}/publish            → publish a Worker as template
//	GET    /api/v1/worker-templates                → list templates
//	GET    /api/v1/worker-templates/{name}/{ver}   → fetch full body
//	POST   /api/v1/worker-templates/install        → one-click install
package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	workersadmin "github.com/don-works/mcplexer/internal/workers/admin"
	"github.com/don-works/mcplexer/internal/workertemplates"
)

// workerTemplatesHandler holds the registry + admin service. Routes
// register only when both are wired (router-side guard).
type workerTemplatesHandler struct {
	registry *workertemplates.Registry
	svc      *workersadmin.Service
}

// TemplateSummary is the slim card payload returned by the list endpoint.
// We pre-decode the body's parameter / secret counts so the dashboard
// can render badge counts without re-fetching the full body.
type TemplateSummary struct {
	Name              string `json:"name"`
	Version           int    `json:"version"`
	Description       string `json:"description"`
	ModelProviderHint string `json:"model_provider_hint,omitempty"`
	ModelIDHint       string `json:"model_id_hint,omitempty"`
	ParameterCount    int    `json:"parameter_count"`
	SecretSlotCount   int    `json:"secret_slot_count"`
	PublishedAt       string `json:"published_at"`
	Author            string `json:"author,omitempty"`
}

// list serves GET /api/v1/worker-templates?search=&limit=.
func (h *workerTemplatesHandler) list(w http.ResponseWriter, r *http.Request) {
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	heads, err := h.registry.ListHeads(
		r.Context(), workertemplates.AdminScope(), limit,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list templates")
		return
	}
	search := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("search")))
	out := make([]TemplateSummary, 0, len(heads))
	for _, e := range heads {
		if search != "" && !templateMatches(e, search) {
			continue
		}
		out = append(out, summariseTemplate(e))
	}
	writeJSON(w, http.StatusOK, out)
}

// templateMatches is a case-insensitive substring search over name +
// description. Body isn't searched (would force every row's JSON parse).
func templateMatches(e store.WorkerTemplateEntry, query string) bool {
	if strings.Contains(strings.ToLower(e.Name), query) {
		return true
	}
	return strings.Contains(strings.ToLower(e.Description), query)
}

// summariseTemplate decodes just enough of the JSON body to populate
// the per-card counters. Body parse failure degrades silently (counts
// fall to zero) — a malformed template still surfaces in the list so
// the operator can soft-delete it.
func summariseTemplate(e store.WorkerTemplateEntry) TemplateSummary {
	row := TemplateSummary{
		Name:        e.Name,
		Version:     e.Version,
		Description: e.Description,
		PublishedAt: e.PublishedAt.Format("2006-01-02T15:04:05Z"),
		Author:      e.Author,
	}
	if tmpl, err := workertemplates.Unmarshal(e.Body); err == nil {
		row.ModelProviderHint = tmpl.ModelProviderHint
		row.ModelIDHint = tmpl.ModelIDHint
		row.ParameterCount = len(tmpl.ParameterSchema)
		row.SecretSlotCount = len(tmpl.SecretSlots)
	}
	return row
}

// get serves GET /api/v1/worker-templates/{name}/{version}. version may
// be "latest" or a positive int.
func (h *workerTemplatesHandler) get(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	versionStr := r.PathValue("version")
	ref := workertemplates.VersionRef{Latest: true}
	if versionStr != "" && versionStr != "latest" {
		n, err := strconv.Atoi(versionStr)
		if err != nil || n <= 0 {
			writeError(w, http.StatusBadRequest, "invalid version")
			return
		}
		ref = workertemplates.VersionRef{Version: n}
	}
	entry, err := h.registry.Get(r.Context(), workertemplates.AdminScope(), name, ref)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "failed to load template", err.Error())
		return
	}
	tmpl, err := workertemplates.Unmarshal(entry.Body)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "malformed template body", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entry":    entry,
		"template": tmpl,
	})
}

// publish serves POST /api/v1/workers/{id}/publish. Body: {name?,description?}.
func (h *workerTemplatesHandler) publish(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "worker id is required")
		return
	}
	var body struct {
		Name        string `json:"name,omitempty"`
		Description string `json:"description,omitempty"`
	}
	_ = decodeJSON(r, &body) // best-effort
	entry, err := h.svc.PublishAsTemplate(r.Context(), workersadmin.PublishAsTemplateInput{
		WorkerID:    id,
		Name:        body.Name,
		Description: body.Description,
	})
	if err != nil {
		if errors.Is(err, store.ErrWorkerNotFound) || errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "worker not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "publish failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

// install serves POST /api/v1/worker-templates/install.
func (h *workerTemplatesHandler) install(w http.ResponseWriter, r *http.Request) {
	var in workersadmin.InstallFromTemplateInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	worker, err := h.svc.InstallFromTemplate(r.Context(), in)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "template not found")
			return
		}
		writeErrorDetail(w, http.StatusBadRequest, "install failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, worker)
}
