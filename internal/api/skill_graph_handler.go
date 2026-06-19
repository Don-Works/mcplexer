// Package api — skill_graph_handler.go (W6) serves the composition DAG
// view of the skill registry, derived from W4's produces/consumes
// declarations. Reuses the skillregistry.BuildGraph pure-function so
// the same graph topology is available to CLI tooling.
//
// GET /api/v1/skills/graph?window_days=30
//
// window_days only affects the per-node stats summary (it's reused for
// the StatsLookup), NOT the graph topology — which is purely manifest
// frontmatter and stable across windows.
package api

import (
	"net/http"
	"time"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

// skillGraphHandler backs GET /api/v1/skills/graph. SkillRegistry is the
// source of skills; Store backs the per-node stats lookup. Both required.
type skillGraphHandler struct {
	store    store.Store
	registry *skillregistry.Registry
}

// handleGraph returns the full SkillGraph as JSON.
//
// Scope: we pull head versions across every workspace the *daemon* can
// see (admin view, mirrors /api/v1/skill-registry behaviour). Per-user
// scoping happens at the universal mcpx__skill_search MCP surface, not
// the dashboard.
func (h *skillGraphHandler) handleGraph(w http.ResponseWriter, r *http.Request) {
	windowDays, err := parseWindowDays(r.URL.Query().Get("window_days"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Limit=0 → store default. Dashboard surfaces up to a few hundred
	// skills; the underlying store cap (ListSkillRegistryHeads's
	// implementation) handles anything larger gracefully.
	heads, err := h.registry.ListHeads(r.Context(), store.SkillScope{}, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load skills")
		return
	}
	// Per-node stats: aggregate W2 runs over the window for each skill.
	// We compute lazily on lookup (one ListSkillRuns per skill) — fine
	// for the small skill counts a real install sees. If this becomes a
	// hot path, pre-fetch all runs in one query and bucket in-memory.
	window := time.Duration(windowDays) * 24 * time.Hour
	since := time.Now().Add(-window)
	lookup := func(name string) *skillregistry.SkillStatsSummary {
		runs, lerr := h.store.ListSkillRuns(r.Context(), store.SkillRunFilter{
			SkillName: name,
			Since:     &since,
			Limit:     1000,
		})
		if lerr != nil || len(runs) == 0 {
			return nil
		}
		s := skillregistry.AggregateSkillRuns(runs, skillregistry.StatsOptions{
			Window: window,
			Now:    time.Now(),
		})
		summary := &skillregistry.SkillStatsSummary{
			Invocations:   s.Invocations,
			SuccessRate:   s.SuccessRate,
			P95DurationMs: s.P95DurationMs,
		}
		if s.LastRunAt != nil {
			summary.LastRunAt = s.LastRunAt
		}
		return summary
	}
	graph := skillregistry.BuildGraph(heads, lookup)
	writeJSON(w, http.StatusOK, map[string]any{
		"graph":       graph,
		"window_days": windowDays,
		"generated":   time.Now().UTC(),
	})
}
