package api

import (
	"context"
	"math"
	"net/http"
	"strconv"

	"github.com/don-works/mcplexer/internal/memory"
	"github.com/don-works/mcplexer/internal/store"
)

// auditSearchHandler backs GET /audit/search and GET /audit/capabilities.
// mem is optional — when it carries a real embedder, the candidate pool
// the store ranked by TF-IDF is re-ranked by query-time embedding cosine
// (mode "vector"). Without an embedder it returns the store's tfidf/fts
// result verbatim — graceful degradation, never a hard dependency.
type auditSearchHandler struct {
	store store.AuditStore
	mem   *memory.Service // optional
}

const (
	auditSearchDefaultLimit = 50
	auditSearchMaxLimit     = 200
)

func (h *auditSearchHandler) search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.AuditFilter{}
	parseAuditFilter(q, &filter)

	limit := auditSearchDefaultLimit
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > auditSearchMaxLimit {
		limit = auditSearchMaxLimit
	}
	filter.Limit = limit

	records, mode, err := h.store.SearchAuditRecords(r.Context(), filter, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to search audit records")
		return
	}
	if records == nil {
		records = []store.AuditRecord{}
	}

	// Optional vector tier: rerank the candidate pool by query-time
	// embedding cosine. Only attempted when an embedder is wired and the
	// query has content. Any failure falls through to the store result.
	if h.mem != nil && h.mem.HasEmbedder() && filter.Q != "" && len(records) > 1 {
		if reranked, ok := h.vectorRerank(r.Context(), filter.Q, records); ok {
			records = reranked
			mode = "vector"
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  records,
		"total": len(records),
		"mode":  mode,
		"query": filter.Q,
	})
}

// vectorRerank embeds the query + each candidate's searchable text in one
// batch and reorders candidates by cosine similarity. Returns ok=false on
// any failure (no embedder, network error, empty vectors) so the caller
// keeps the store's ranking.
func (h *auditSearchHandler) vectorRerank(
	ctx context.Context, query string, records []store.AuditRecord,
) ([]store.AuditRecord, bool) {
	qVec, _, err := h.mem.EmbedQuery(ctx, query)
	if err != nil || len(qVec) == 0 {
		return nil, false
	}
	docVecs, _, err := h.mem.EmbedDocs(ctx, auditDocTexts(records))
	if err != nil || len(docVecs) != len(records) {
		return nil, false
	}
	type scored struct {
		rec   store.AuditRecord
		score float64
	}
	out := make([]scored, len(records))
	for i, rec := range records {
		out[i] = scored{rec: rec, score: cosineDense(qVec, docVecs[i])}
	}
	// Stable insertion sort by score desc — N is bounded by limit (<=200).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].score > out[j-1].score; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	res := make([]store.AuditRecord, len(out))
	for i := range out {
		res[i] = out[i].rec
	}
	return res, true
}

// auditDocTexts composes the per-record searchable text for embedding,
// mirroring the store's TF-IDF text surface.
func auditDocTexts(records []store.AuditRecord) []string {
	texts := make([]string, len(records))
	for i, r := range records {
		texts[i] = r.ToolName + " " + r.ErrorMessage + " " + r.Subpath + " " +
			r.WorkspaceName + " " + string(r.ParamsRedacted)
	}
	return texts
}

// cosineDense computes cosine similarity between two equal-length dense
// vectors. Returns 0 when either is zero-norm or lengths differ.
func cosineDense(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// capabilities reports which search tiers + alert features are live.
func (h *auditSearchHandler) capabilities(w http.ResponseWriter, _ *http.Request) {
	vector := h.mem != nil && h.mem.HasEmbedder()
	writeJSON(w, http.StatusOK, map[string]any{
		"search": map[string]any{
			"fts":    true,
			"tfidf":  true,
			"vector": vector,
		},
		"alerts":         true,
		"saved_searches": true,
	})
}
