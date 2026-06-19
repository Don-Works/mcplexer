// Package api — tools_handler.go (workers M0.6 followup) exposes a
// flat catalogue of every tool the local workspace currently routes
// to. Used by the workers editor to render a checkbox grid grouped by
// namespace; the editor previously fell back to a JSON textarea
// because no list endpoint existed.
//
// Source of truth is each DownstreamServer's CapabilitiesCache —
// refreshed by /api/v1/downstreams/{id}/discover. This handler does
// NOT trigger a fresh tools/list against any downstream; it surfaces
// what's already known. The UI nudges the user to "Discover" when a
// server's cache is empty.

package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/writeclass"
)

// toolsHandler serves the consolidated tool catalogue. The store is
// the only dependency — we read CapabilitiesCache off each registered
// downstream and project it into the editor-friendly shape.
type toolsHandler struct {
	store store.Store
}

// toolListItem is the editor's per-row projection. write_class mirrors
// the heuristic the runner's tool dispatcher uses so the UI's
// write/read split matches what the runner classifies on dispatch —
// the operator sees the same lens the propose-mode gate uses.
type toolListItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Namespace   string `json:"namespace"`
	WriteClass  bool   `json:"write_class"`
}

// list serves GET /api/v1/tools. Returns every tool advertised by
// every registered downstream, namespaced. Servers without a cached
// catalogue are skipped silently — the user discovers via the
// downstream page.
func (h *toolsHandler) list(w http.ResponseWriter, r *http.Request) {
	servers, err := h.store.ListDownstreamServers(r.Context())
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError, "failed to list downstreams", err.Error())
		return
	}
	out := make([]toolListItem, 0)
	for _, srv := range servers {
		out = append(out, parseDownstreamTools(srv.CapabilitiesCache, srv.ToolNamespace)...)
	}
	if out == nil {
		out = []toolListItem{}
	}
	writeJSON(w, http.StatusOK, out)
}

// parseDownstreamTools projects one server's CapabilitiesCache JSON
// into the editor row shape. Namespaceless names get the configured
// namespace prefix so the UI shows a single qualified identifier.
// Unparseable cache (legacy or in-flight discovery) yields nil so
// other servers still surface.
func parseDownstreamTools(cache json.RawMessage, namespace string) []toolListItem {
	if len(cache) == 0 {
		return nil
	}
	var payload struct {
		Tools []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(cache, &payload); err != nil {
		return nil
	}
	out := make([]toolListItem, 0, len(payload.Tools))
	for _, t := range payload.Tools {
		fullName := t.Name
		if namespace != "" && !strings.Contains(fullName, "__") {
			fullName = namespace + "__" + t.Name
		}
		out = append(out, toolListItem{
			Name:        fullName,
			Description: t.Description,
			Namespace:   namespace,
			WriteClass:  IsWriteClassTool(fullName),
		})
	}
	return out
}

// IsWriteClassTool flags tools that look side-effecting by name. Thin
// wrapper around the shared writeclass.IsWriteClass helper so the UI
// label and the runner's propose-mode gate always agree.
func IsWriteClassTool(name string) bool {
	return writeclass.IsWriteClass(name)
}
