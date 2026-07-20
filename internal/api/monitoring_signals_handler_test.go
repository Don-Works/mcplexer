// monitoring_signals_handler_test.go — coverage for the expected-signal read
// surface. The rule row alone persists only last_outcome, so the check that
// matters is that the live evaluation carries the outcome AND the detail
// sentence naming which ladder step fired.
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

// TestExpectedSignalListSurfacesEvaluationReason is the "can an operator
// interrogate this rule" check. last_outcome alone never explains a silent
// rule; the live evaluation must carry the outcome AND the detail sentence.
func TestExpectedSignalListSurfacesEvaluationReason(t *testing.T) {
	f := newIncidentFixture(t)
	h := &monitoringSignalHandler{store: f.db}
	rec := httptest.NewRecorder()
	h.list(rec, httptest.NewRequest(http.MethodGet,
		"/api/v1/monitoring/expected-signals?workspace_id=ws-A", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var out struct {
		ExpectedSignals []struct {
			ID             string   `json:"id"`
			SourceID       string   `json:"source_id"`
			Enabled        bool     `json:"enabled"`
			MatchSubstring string   `json:"match_substring"`
			MinCount       int64    `json:"min_count"`
			Window         string   `json:"window"`
			ActiveDays     []string `json:"active_days"`
			ActiveStart    string   `json:"active_start"`
			ActiveEnd      string   `json:"active_end"`
			Evaluation     struct {
				Outcome   string `json:"outcome"`
				Detail    string `json:"detail"`
				ClassKind string `json:"class_kind"`
				Error     string `json:"error"`
			} `json:"evaluation"`
		} `json:"expected_signals"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Total != 1 {
		t.Fatalf("total = %d, want 1", out.Total)
	}
	rule := out.ExpectedSignals[0]
	if rule.ID != f.ruleA.ID || rule.SourceID != f.srcAID || !rule.Enabled {
		t.Errorf("rule identity wrong: %+v", rule)
	}
	if rule.MatchSubstring != "sync complete" || rule.MinCount != 1 {
		t.Errorf("match/min_count wrong: %+v", rule)
	}
	if rule.Window != "1h0m0s" {
		t.Errorf("window = %q, want 1h0m0s", rule.Window)
	}
	if len(rule.ActiveDays) != 7 || rule.ActiveStart != "00:00" || rule.ActiveEnd != "24:00" {
		t.Errorf("schedule wrong: days=%v start=%q end=%q",
			rule.ActiveDays, rule.ActiveStart, rule.ActiveEnd)
	}
	if rule.Evaluation.Error != "" {
		t.Fatalf("evaluation error: %s", rule.Evaluation.Error)
	}
	// A rule created moments ago has not lived a full window, so the honest
	// verdict is warming_up — and the detail must say so rather than leaving
	// the operator to guess why nothing has fired.
	if rule.Evaluation.Outcome != string(store.OutcomeSignalWarmingUp) {
		t.Errorf("outcome = %q, want %q", rule.Evaluation.Outcome, store.OutcomeSignalWarmingUp)
	}
	if rule.Evaluation.Detail == "" {
		t.Error("evaluation detail is empty — the ladder step that fired is unexplained")
	}
}
