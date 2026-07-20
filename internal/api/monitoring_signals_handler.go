// monitoring_signals_handler.go — the read surface for expected-signal
// (absence) rules.
//
// A rule an operator cannot interrogate is a rule they will not trust, and the
// stored row alone cannot be interrogated: only `last_outcome` is persisted,
// while the evaluator's Detail — the sentence naming which ladder step fired
// and why — is written to incident evidence and otherwise discarded. For every
// non-raising outcome (warming_up, partial_window, awaiting_first_signal,
// outside_active_hours, inconclusive) that means the daemon knows exactly why
// the rule is silent and nobody can ask it.
//
// So this endpoint re-runs the evaluation on demand. store.EvaluateExpectedSignal
// is a pure total function over facts the daemon already holds — no clock
// beyond the one injected, no network, and emphatically no model — so
// evaluating on read is deterministic and costs one indexed count per rule.
// The stored latches are served alongside under their own keys: `last_outcome`
// is what the evaluator last recorded, `evaluation` is what is true now.
package api

import (
	"fmt"
	"net/http"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

type monitoringSignalHandler struct {
	store store.MonitoringExpectedSignalStore // nil = unsupported by this store
}

// signalEvaluation is the live verdict for one rule, or the reason none could
// be produced. Error is populated per-rule rather than failing the whole list:
// one unloadable timezone must not blind the operator to every other rule.
type signalEvaluation struct {
	Outcome     string    `json:"outcome"`
	Reason      string    `json:"reason,omitempty"`
	Detail      string    `json:"detail,omitempty"`
	Raise       bool      `json:"raise"`
	ClassKey    string    `json:"class_key,omitempty"`
	ClassKind   string    `json:"class_kind,omitempty"`
	Severity    string    `json:"severity,omitempty"`
	Title       string    `json:"title,omitempty"`
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`

	MatchCount          int64      `json:"match_count"`
	TotalLines          int64      `json:"total_lines"`
	LastMatchAt         *time.Time `json:"last_match_at,omitempty"`
	SourceEnabled       bool       `json:"source_enabled"`
	ConsecutiveFailures int        `json:"consecutive_failures"`

	Error string `json:"error,omitempty"`
}

// signalRow is one rule with its schedule spelled out and its live evaluation.
type signalRow struct {
	*store.MonitoringExpectedSignal
	Window      string            `json:"window"`
	ActiveDays  []string          `json:"active_days"`
	ActiveStart string            `json:"active_start"`
	ActiveEnd   string            `json:"active_end"`
	Evaluation  *signalEvaluation `json:"evaluation,omitempty"`
}

// list serves a workspace's expected-signal rules with their current verdicts.
func (h *monitoringSignalHandler) list(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		writeError(w, http.StatusNotImplemented,
			"expected-signal rules not available on this daemon")
		return
	}
	wsID := workspaceIDParam(r)
	if wsID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id query param required")
		return
	}
	rules, err := h.store.ListMonitoringExpectedSignals(r.Context(), wsID)
	if err != nil {
		writeMonitoringErr(w, err, "list expected signals")
		return
	}
	now := time.Now().UTC()
	rows := make([]signalRow, 0, len(rules))
	raising := 0
	for _, rule := range rules {
		row := newSignalRow(rule)
		row.Evaluation = h.evaluate(r, rule, now)
		if row.Evaluation != nil && row.Evaluation.Raise {
			raising++
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"expected_signals": rows,
		"total":            len(rows),
		"raising":          raising,
		"evaluated_at":     now,
	})
}

// evaluate observes and judges one rule at now. Errors are carried on the row.
func (h *monitoringSignalHandler) evaluate(
	r *http.Request, rule *store.MonitoringExpectedSignal, now time.Time,
) *signalEvaluation {
	observed, health, err := h.store.ObserveExpectedSignal(r.Context(), rule, now)
	if err != nil {
		return &signalEvaluation{Error: err.Error()}
	}
	loc, err := rule.Location()
	if err != nil {
		return &signalEvaluation{Error: err.Error()}
	}
	decision := store.EvaluateExpectedSignal(store.ExpectedSignalInput{
		Rule: *rule, Observed: observed, Health: health, Now: now, Location: loc,
	})
	kind := ""
	if decision.ClassKey != "" {
		kind, _ = store.ClassifyIncidentClassKey(decision.ClassKey)
	}
	return &signalEvaluation{
		Outcome: string(decision.Outcome), Reason: decision.Reason,
		Detail: decision.Detail, Raise: decision.Raise,
		ClassKey: decision.ClassKey, ClassKind: kind,
		Severity: decision.Severity, Title: decision.Title,
		WindowStart: decision.WindowStart, WindowEnd: decision.WindowEnd,
		MatchCount: observed.MatchCount, TotalLines: observed.TotalLines,
		LastMatchAt:   observed.LastMatchAt,
		SourceEnabled: health.Enabled, ConsecutiveFailures: health.ConsecutiveFailures,
	}
}

var signalWeekdays = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// newSignalRow renders the rule's stored knobs in the units an operator reads
// them in — a duration rather than a second count, weekday names rather than a
// bitmask, wall-clock times rather than minutes past midnight.
func newSignalRow(rule *store.MonitoringExpectedSignal) signalRow {
	days := make([]string, 0, len(signalWeekdays))
	for i, name := range signalWeekdays {
		if rule.ActiveDaysMask&(1<<uint(i)) != 0 {
			days = append(days, name)
		}
	}
	return signalRow{
		MonitoringExpectedSignal: rule,
		Window:                   rule.Window().String(),
		ActiveDays:               days,
		ActiveStart:              signalClock(rule.ActiveStartMinute),
		ActiveEnd:                signalClock(rule.ActiveEndMinute),
	}
}

// signalClock renders minutes-past-midnight as HH:MM. 1440 is rendered 24:00
// rather than wrapping to 00:00, which would make an all-day rule look like an
// empty one.
func signalClock(minute int) string {
	return fmt.Sprintf("%02d:%02d", minute/60, minute%60)
}
