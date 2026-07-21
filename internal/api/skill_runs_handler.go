package api

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// skillRunsHandler exposes the W2 skill_runs telemetry table to the
// dashboard. Sister of skill_registry_handler — same pattern, same
// scope-by-default semantics (admin view).
type skillRunsHandler struct {
	store store.Store
}

// listForSkill returns the most recent runs of one skill across all
// workspaces. Backs the skill detail page's "Recent runs" panel.
//
// GET /api/v1/skills/:name/runs?limit=50
func (h *skillRunsHandler) listForSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	runs, err := h.store.ListSkillRuns(r.Context(), store.SkillRunFilter{
		SkillName: name,
		Limit:     limit,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runs")
		return
	}
	if runs == nil {
		runs = []store.SkillRun{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"runs":      runs,
		"stats":     aggregateSkillRunStats(runs),
		"skill":     name,
		"limit":     limit,
		"generated": time.Now().UTC(),
	})
}

// get returns one skill_run by id (with phases + tools_used intact).
//
// GET /api/v1/skill-runs/:id
func (h *skillRunsHandler) get(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}
	run, err := h.store.GetSkillRun(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "skill run not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get run")
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// skillRunAggregateStats is the rollup the dashboard panel renders at
// the top of the recent-runs list: invocation count, success rate, and
// mean/median duration. Computed in Go (not SQL) so the dashboard can
// scope by any client-side filter without a second query.
type skillRunAggregateStats struct {
	Total         int     `json:"total"`
	Succeeded     int     `json:"succeeded"`
	Failed        int     `json:"failed"`
	Cancelled     int     `json:"cancelled"`
	Running       int     `json:"running"`
	SuccessRate   float64 `json:"success_rate"` // 0..1, over terminal runs
	AvgDurationMs int64   `json:"avg_duration_ms"`
}

// aggregateSkillRunStats computes the rollup. Empty / nil input is OK.
func aggregateSkillRunStats(runs []store.SkillRun) skillRunAggregateStats {
	stats := skillRunAggregateStats{Total: len(runs)}
	var (
		terminal     int
		totalDurMs   int64
		durationRuns int
	)
	for _, r := range runs {
		switch r.Outcome {
		case store.SkillRunOutcomeSuccess:
			stats.Succeeded++
			terminal++
		case store.SkillRunOutcomeFailure:
			stats.Failed++
			terminal++
		case store.SkillRunOutcomeCancelled:
			stats.Cancelled++
			terminal++
		default:
			stats.Running++
		}
		if r.CompletedAt != nil {
			totalDurMs += r.CompletedAt.Sub(r.StartedAt).Milliseconds()
			durationRuns++
		}
	}
	if terminal > 0 {
		stats.SuccessRate = float64(stats.Succeeded) / float64(terminal)
	}
	if durationRuns > 0 {
		stats.AvgDurationMs = totalDurMs / int64(durationRuns)
	}
	return stats
}
