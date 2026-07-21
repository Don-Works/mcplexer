// Package api — memory_filter.go centralises the querystring → MemoryFilter
// translation used by memory_handler.go + memory_offers_handler.go. Extracted
// to keep memory_handler.go under the 300-line cap.
package api

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// parseMemoryFilter pulls the standard set of list-filter query params off
// the request. Unknown / malformed values fall back to defaults rather than
// erroring — the dashboard treats this surface as best-effort filtering.
func parseMemoryFilter(r *http.Request) (store.MemoryFilter, error) {
	q := r.URL.Query()
	f := store.MemoryFilter{
		Scope:          scopeFromQuery(r),
		Kind:           strings.TrimSpace(q.Get("kind")),
		IncludeInvalid: parseBoolQ(q.Get("include_invalid")),
	}
	if tagsRaw := strings.TrimSpace(q.Get("tags")); tagsRaw != "" {
		parts := strings.Split(tagsRaw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		f.Tags = out
	}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			f.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			f.Offset = n
		}
	}
	return f, nil
}

// scopeFromQuery extracts a store.SkillScope from ?workspace_id=. Unset =
// admin (IncludeAll=true) — the dashboard sees everything by default.
func scopeFromQuery(r *http.Request) store.SkillScope {
	wsID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	if wsID == "" {
		return store.SkillScope{IncludeAll: true}
	}
	return store.SkillScope{WorkspaceIDs: []string{wsID}}
}

// scopeFromWorkspace mirrors scopeFromQuery but takes an explicit pointer
// (so JSON-body search requests can opt-in to narrowing). nil = admin.
func scopeFromWorkspace(wsID *string) store.SkillScope {
	if wsID == nil || strings.TrimSpace(*wsID) == "" {
		return store.SkillScope{IncludeAll: true}
	}
	return store.SkillScope{WorkspaceIDs: []string{*wsID}}
}

// parseBoolQ accepts "1", "true", "yes" (case-insensitive) as true;
// anything else is false. Keeps URLs hand-typable.
func parseBoolQ(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
