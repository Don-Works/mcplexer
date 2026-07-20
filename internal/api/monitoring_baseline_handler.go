// monitoring_baseline_handler.go — the read surface for learned baselines.
//
// A learned rule nobody can interrogate is a rule nobody trusts. These
// endpoints answer the two questions an operator actually asks about automatic
// alerting: "what does the system think normal looks like?" and, far more
// often, "why is there no alert for THIS job?"
//
// Rejections are served alongside promotions for exactly that reason — a
// candidate the learner declined, with the statistics and the reason it
// declined on, is the more useful half of the surface.
package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/don-works/mcplexer/internal/store"
)

type monitoringBaselineHandler struct {
	store store.MonitoringBaselineStore // nil = learner not supported by this store
}

// list serves the learned baselines for a workspace, or for one source when
// source_id is supplied. Promoted rules sort first, then the strongest
// near-misses, so "what fires" and "what nearly fires" are on one page.
func (h *monitoringBaselineHandler) list(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusNotImplemented, "baseline learning not available on this daemon")
		return
	}
	limit, ok := baselineLimitParam(w, r)
	if !ok {
		return
	}
	baselines, err := h.fetch(r, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	promoted := 0
	for _, b := range baselines {
		if b.RuleID != "" {
			promoted++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"baselines": baselines,
		"promoted":  promoted,
		"total":     len(baselines),
		// Echoed so an operator can check a stored verdict against the
		// thresholds that produced it without reading the source.
		"thresholds": baselineThresholds(),
	})
}

func (h *monitoringBaselineHandler) fetch(
	r *http.Request, limit int,
) ([]*store.SignalBaseline, error) {
	if sourceID := r.URL.Query().Get("source_id"); sourceID != "" {
		return h.store.ListSignalBaselinesForSource(r.Context(), sourceID, limit)
	}
	wsID := workspaceIDParam(r)
	if wsID == "" {
		return nil, errors.New("workspace_id or source_id query param required")
	}
	return h.store.ListSignalBaselines(r.Context(), wsID, limit)
}

// get serves one template's baseline — the "why did this not fire" answer.
func (h *monitoringBaselineHandler) get(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusNotImplemented, "baseline learning not available on this daemon")
		return
	}
	b, err := h.store.GetSignalBaselineByTemplate(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound,
			"no baseline for this template — the learner has not measured it yet")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"baseline":   b,
		"thresholds": baselineThresholds(),
	})
}

func baselineLimitParam(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, true
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 0 {
		writeError(w, http.StatusBadRequest, "invalid limit")
		return 0, false
	}
	return limit, true
}

// baselineThresholds is the promotion ladder as data. Serving it next to the
// verdicts is what makes a stored decision checkable rather than assertable:
// an operator can see that a rejected candidate's relative_mad of 0.71 lost to
// a 0.35 ceiling, and that 0.35 is half the value a random arrival process
// produces.
func baselineThresholds() map[string]any {
	return map[string]any{
		"min_learn_span_hours":   store.BaselineMinLearnSpan.Hours(),
		"min_deltas":             store.BaselineMinDeltas,
		"min_cycles":             store.BaselineMinCycles,
		"max_relative_mad":       store.BaselineMaxRelativeMAD,
		"max_p95_ratio":          store.BaselineMaxP95Ratio,
		"burst_separation":       store.BaselineBurstSeparation,
		"min_hour_occupancy":     store.BaselineMinHourOccupancy,
		"min_day_history_days":   store.BaselineMinDayHistoryDays,
		"window_period_multiple": store.BaselineWindowPeriodMultiple,
		"max_window_multiple":    store.BaselineMaxWindowPeriodMultiple,
		// The random-arrival reference values every threshold above is set
		// against, so the margin is visible rather than folded into a comment.
		"random_arrival_relative_mad": 0.694,
		"random_arrival_p95_ratio":    4.32,
	}
}
