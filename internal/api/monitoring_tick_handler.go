// monitoring_tick_handler.go — forcing a learner pass and an absence tick.
//
// Both loops run on their own schedule, which is correct for a daemon and
// hostile to a test suite: a scenario that seeds history and then waits for the
// next natural tick spends minutes doing nothing, and a suite that slow stops
// being run. This endpoint runs one pass of each synchronously, so a scenario
// can seed, tick, and assert.
//
// It shares MCPLEXER_ALLOW_TEST_INGEST rather than taking a gate of its own.
// The two are one capability — seed history, then make the daemon look at it —
// and a second variable would only create a state where half of it works.
//
// Nothing here is a shortcut around the real code: Learn and Evaluate are the
// same methods the daemon's own goroutines call, on the same store, with the
// same deterministic policy. Forcing a tick changes WHEN the daemon looks, never
// what it concludes.
package api

import (
	"context"
	"net/http"
)

// MonitoringTicker runs one pass of the baseline learner and one pass of the
// absence evaluator. *baseline.Learner and *baseline.Evaluator already have
// these signatures; the daemon supplies an adapter holding both.
type MonitoringTicker interface {
	Learn(ctx context.Context)
	Evaluate(ctx context.Context)
}

type monitoringTickHandler struct {
	ticker MonitoringTicker // nil = not wired by this daemon
}

// tick runs the requested passes. Both default to true, because "make the
// daemon catch up" is the request being made in practice and requiring two
// flags to express it is a trap.
func (h *monitoringTickHandler) tick(w http.ResponseWriter, r *http.Request) {
	if !testIngestEnabled() {
		writeTestIngestClosed(w)
		return
	}
	if h.ticker == nil {
		writeError(w, http.StatusNotImplemented,
			"monitoring tick not available on this daemon (learner/evaluator not wired)")
		return
	}
	var in struct {
		Learn    *bool `json:"learn"`
		Evaluate *bool `json:"evaluate"`
	}
	_ = decodeJSON(r, &in)
	learn := in.Learn == nil || *in.Learn
	evaluate := in.Evaluate == nil || *in.Evaluate
	// Learning first is load-bearing: a pass that promotes a new expected-signal
	// rule must be able to have that rule evaluated in the same call, or a
	// scenario needs two ticks to observe a one-tick effect.
	if learn {
		h.ticker.Learn(r.Context())
	}
	if evaluate {
		h.ticker.Evaluate(r.Context())
	}
	writeJSON(w, http.StatusOK, map[string]bool{
		"learned": learn, "evaluated": evaluate,
	})
}
