package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type auditHandler struct {
	store store.AuditStore
}

// validAuditSorts is the allowlist for the ?sort param. An unknown value
// is dropped (the store defaults to time_desc).
var validAuditSorts = map[string]bool{
	"time_desc": true, "time_asc": true,
	"latency_desc": true, "latency_asc": true,
}

func (h *auditHandler) query(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.AuditFilter{Limit: 50, Offset: 0}
	parseAuditFilter(q, &filter)

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
	if v := q.Get("sort"); v != "" && validAuditSorts[v] {
		filter.Sort = v
	}
	if v := q.Get("cursor"); v != "" {
		if ts, id, ok := decodeAuditCursor(v); ok {
			filter.CursorTs = &ts
			filter.CursorID = id
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

	// next_cursor: the keyset token of the last row, but only when a full
	// page was returned (a short page means no more rows). Empty for
	// latency_* sorts where keyset paging isn't supported.
	nextCursor := ""
	if len(records) == filter.Limit && isKeysetSort(filter.Sort) {
		last := records[len(records)-1]
		nextCursor = encodeAuditCursor(last.Timestamp, last.ID)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":        records,
		"total":       total,
		"limit":       filter.Limit,
		"offset":      filter.Offset,
		"next_cursor": nextCursor,
	})
}

// parseAuditFilter reads every exact-match + free-text filter param into f.
// Shared by /audit and /audit/search so both honour the identical filter
// surface. Does NOT touch limit/offset/sort/cursor — those are
// endpoint-specific.
func parseAuditFilter(q queryGetter, f *store.AuditFilter) {
	str := func(key string) *string {
		if v := q.Get(key); v != "" {
			return &v
		}
		return nil
	}
	f.ID = str("id")
	f.WorkspaceID = str("workspace_id")
	f.ToolName = str("tool_name")
	f.Status = str("status")
	f.SessionID = str("session_id")
	f.ExecutionID = str("execution_id")
	f.ActorKind = str("actor_kind")
	f.ActorID = str("actor_id")
	f.DownstreamServerID = str("downstream_server_id")
	f.RouteRuleID = str("route_rule_id")
	f.ClientType = str("client_type")
	f.ErrorCode = str("error_code")
	f.Tier = str("tier")
	if v := q.Get("cache_hit"); v != "" {
		b := v == "true"
		if v == "true" || v == "false" {
			f.CacheHit = &b
		}
	}
	if v := q.Get("min_latency_ms"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			f.MinLatencyMs = &n
		}
	}
	if v := q.Get("after"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.After = &t
		}
	}
	if v := q.Get("before"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.Before = &t
		}
	}
	f.Q = q.Get("q")
}

// queryGetter is the subset of url.Values the filter parser needs — lets
// tests pass a stub without constructing a full request.
type queryGetter interface{ Get(string) string }

func isKeysetSort(sort string) bool {
	return sort == "" || sort == "time_desc" || sort == "time_asc"
}

// encodeAuditCursor builds the opaque keyset token "<RFC3339Nano>|<id>".
func encodeAuditCursor(ts time.Time, id string) string {
	return ts.UTC().Format(time.RFC3339Nano) + "|" + id
}

// decodeAuditCursor parses the opaque keyset token. Returns ok=false on a
// malformed token so the handler silently ignores it (page from the top).
func decodeAuditCursor(s string) (time.Time, string, bool) {
	idx := strings.LastIndex(s, "|")
	if idx <= 0 || idx == len(s)-1 {
		return time.Time{}, "", false
	}
	ts, err := time.Parse(time.RFC3339Nano, s[:idx])
	if err != nil {
		return time.Time{}, "", false
	}
	return ts, s[idx+1:], true
}
