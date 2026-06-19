package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type auditHandler struct {
	store store.AuditStore
}

func (h *auditHandler) query(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.AuditFilter{
		Limit:  50,
		Offset: 0,
	}

	if v := q.Get("id"); v != "" {
		filter.ID = &v
	}
	if v := q.Get("workspace_id"); v != "" {
		filter.WorkspaceID = &v
	}
	if v := q.Get("tool_name"); v != "" {
		filter.ToolName = &v
	}
	if v := q.Get("status"); v != "" {
		filter.Status = &v
	}
	if v := q.Get("session_id"); v != "" {
		filter.SessionID = &v
	}
	if v := q.Get("execution_id"); v != "" {
		filter.ExecutionID = &v
	}
	if v := q.Get("after"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.After = &t
		}
	}
	if v := q.Get("before"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			filter.Before = &t
		}
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 500 {
			filter.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			filter.Offset = n
		}
	}

	records, total, err := h.store.QueryAuditRecords(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to query audit records")
		return
	}

	if records == nil {
		records = []store.AuditRecord{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":   records,
		"total":  total,
		"limit":  filter.Limit,
		"offset": filter.Offset,
	})
}
