// Package api — skill_stats_handler.go (W6) exposes the rolled-up
// reputation view of each skill, backed by W2's skill_runs telemetry.
//
// Single: GET /api/v1/skills/{name}/stats?window_days=30
// Batch:  GET /api/v1/skills/stats?names=foo,bar,baz
//
// The aggregator lives in internal/skillregistry/stats.go — this file
// is the HTTP glue (parameter validation, store fetch, JSON shaping).
package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
)

// maxStatsWindowDays caps the rolling window the API will honour. The
// store-layer query gets `Since = now - window`; very large windows on
// large skill_runs tables would walk the entire history. 365d is the
// sensible-human ceiling.
const maxStatsWindowDays = 365

// maxBatchSkills caps batch-endpoint cardinality. 200 is generous
// (typical install has <30 skills) but defends against unbounded
// fan-out from a malformed dashboard request.
const maxBatchSkills = 200

// skillStatsHandler backs the W6 stats endpoints. Shares the same
// store.Store interface as the rest of the api package; no extra deps.
type skillStatsHandler struct {
	store store.Store
}

// getForSkill returns the rolled-up stats for one skill aggregated
// across every workspace (admin-style view — matches the existing
// /api/v1/skills/{name}/runs handler's scoping).
//
// GET /api/v1/skills/{name}/stats?window_days=30
func (h *skillStatsHandler) getForSkill(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	windowDays, err := parseWindowDays(r.URL.Query().Get("window_days"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	stats, err := h.computeStats(r, name, windowDays)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load runs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"skill":     name,
		"stats":     stats,
		"generated": time.Now().UTC(),
	})
}

// getBatch returns a map[name]SkillStats for the comma-separated list
// of names. Unknown names get a zero-valued SkillStats (not 404'd) so
// the dashboard can render a uniform row of tiles without partial
// failures.
//
// GET /api/v1/skills/stats?names=foo,bar&window_days=30
func (h *skillStatsHandler) getBatch(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("names")
	if strings.TrimSpace(raw) == "" {
		writeError(w, http.StatusBadRequest, "names is required (comma-separated)")
		return
	}
	names := dedupeNames(strings.Split(raw, ","))
	if len(names) == 0 {
		writeError(w, http.StatusBadRequest, "names is empty after trimming")
		return
	}
	if len(names) > maxBatchSkills {
		writeError(w, http.StatusBadRequest, "too many names; max "+strconv.Itoa(maxBatchSkills))
		return
	}
	windowDays, err := parseWindowDays(r.URL.Query().Get("window_days"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	out := make(map[string]skillregistry.SkillStats, len(names))
	for _, n := range names {
		stats, err := h.computeStats(r, n, windowDays)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to load runs for "+n)
			return
		}
		out[n] = stats
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stats":       out,
		"window_days": windowDays,
		"generated":   time.Now().UTC(),
	})
}

// computeStats is the shared "fetch runs from store, aggregate" path.
// Centralised so the single- and batch-endpoint code stays small.
func (h *skillStatsHandler) computeStats(
	r *http.Request, name string, windowDays int,
) (skillregistry.SkillStats, error) {
	window := time.Duration(windowDays) * 24 * time.Hour
	since := time.Now().Add(-window)
	// Limit=0 → store default (50). The aggregator only needs runs
	// inside the window, but the store doesn't paginate by default —
	// we pull a generous slice and trust the cap. 1000 is enough for
	// any skill short of pathological logging; the dashboard tile only
	// surfaces an order-of-magnitude figure anyway.
	runs, err := h.store.ListSkillRuns(r.Context(), store.SkillRunFilter{
		SkillName: name,
		Since:     &since,
		Limit:     1000,
	})
	if err != nil {
		return skillregistry.SkillStats{}, err
	}
	return skillregistry.AggregateSkillRuns(runs, skillregistry.StatsOptions{
		Window: window,
		Now:    time.Now(),
	}), nil
}

// parseWindowDays returns the rolling-window length in whole days,
// defaulting to 30 when the query param is absent. Rejects non-numeric,
// non-positive, and over-365 inputs.
func parseWindowDays(raw string) (int, error) {
	if raw == "" {
		return 30, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errInvalidWindowDays
	}
	if n <= 0 {
		return 0, errInvalidWindowDays
	}
	if n > maxStatsWindowDays {
		return 0, errWindowTooLarge
	}
	return n, nil
}

// dedupeNames trims + lower-cases (no, skills are case-sensitive — just
// trims) + drops blanks + dedupes while preserving order. Caller passes
// the raw comma split; we tidy.
func dedupeNames(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, raw := range in {
		n := strings.TrimSpace(raw)
		if n == "" {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

// Sentinel errors for parseWindowDays. Kept as plain errors (not types)
// because the surface is HTTP and the messages are user-facing.
var (
	errInvalidWindowDays = &validationError{msg: "window_days must be a positive integer"}
	errWindowTooLarge    = &validationError{msg: "window_days exceeds maximum of 365"}
)

type validationError struct{ msg string }

func (e *validationError) Error() string { return e.msg }
