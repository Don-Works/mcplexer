// Package api — memory_graph_handler.go serves GET /api/v1/memory/graph,
// a node/edge view of the memory store powering the dashboard's
// memory visualisation page.
//
// Edge construction (no embeddings required — works on every install):
//
//  1. Co-tag — any two memories sharing >= 1 tag get an edge with
//     weight = shared_count / max(tags_a, tags_b). Symmetric Jaccard-ish.
//  2. Wikilink — explicit [[Name]] tokens in content link to a memory
//     whose name matches exactly (case-insensitive). Weight 1.0 (strongest).
//
// Embedding-similarity edges are intentionally *not* computed here — the
// vec0 KNN is a per-query operation, not a graph-wide n² sweep, and
// pre-baking similarity edges across thousands of memories every
// request would dominate the response time. The hook is left for a
// future enhancement once we expose a "k-nearest-neighbours per memory"
// store method.
//
// Caps to keep the viz legible + the response slim:
//   - 5000 nodes max. Above that, sample most-recent + pinned first.
//   - 3 edges per node max (top by weight) → graph stays sparse.
//
// The graph construction itself lives in memory_graph_builder.go so this
// file can stay under the 300-line cap.
package api

import (
	"net/http"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
)

// memoryGraphHandler is the GET /api/v1/memory/graph handler. Reuses the
// memory.Service so visibility scoping stays consistent with the rest of
// the memory surface.
type memoryGraphHandler struct {
	svc *memory.Service
}

func newMemoryGraphHandler(svc *memory.Service) *memoryGraphHandler {
	return &memoryGraphHandler{svc: svc}
}

// graphNode is one node in the response.
type graphNode struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Kind      string   `json:"kind"`
	Tags      []string `json:"tags,omitempty"`
	CreatedAt string   `json:"created_at"`
	Size      int      `json:"size"` // degree (set after edges)
	Pinned    bool     `json:"pinned,omitempty"`
}

// graphEdge is one edge. Reason explains *why* the edge exists so the
// frontend can colour-code (co-tag dim, wikilink bright).
type graphEdge struct {
	Source string  `json:"source"`
	Target string  `json:"target"`
	Weight float64 `json:"weight"`
	Reason string  `json:"reason"` // "co_tag" | "wikilink"
}

// graphResponse is the full payload.
type graphResponse struct {
	Nodes     []graphNode `json:"nodes"`
	Edges     []graphEdge `json:"edges"`
	Truncated bool        `json:"truncated"`
	NodeCap   int         `json:"node_cap"`
}

// maxGraphNodes is the cap on returned nodes. Above this we sample.
const maxGraphNodes = 5000

// maxEdgesPerNode caps fanout so the viz stays legible.
const maxEdgesPerNode = 3

// handleGraph serves GET /api/v1/memory/graph.
//
// Querystring:
//
//	workspace_id  → narrow to one workspace ∪ global. Default = all.
//	include_invalid=1 → include t_valid_end != NULL rows.
func (h *memoryGraphHandler) handleGraph(w http.ResponseWriter, r *http.Request) {
	scope := scopeFromQuery(r)
	includeInvalid := parseBoolQ(r.URL.Query().Get("include_invalid"))

	// Pull all in-scope memories. List honours scope; we ask for a
	// generous limit but cap downstream too.
	f := store.MemoryFilter{
		Scope:          scope,
		IncludeInvalid: includeInvalid,
		Limit:          maxGraphNodes * 2, // headroom for sampling
	}
	rows, err := h.svc.List(r.Context(), f)
	if err != nil {
		writeErrorDetail(w, http.StatusInternalServerError,
			"failed to list memories", err.Error())
		return
	}

	writeJSON(w, http.StatusOK, buildGraph(rows))
}
