package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// seedBaselineRows writes one promoted and two refused baselines into ws-A.
// The refusals matter as much as the promotion: the whole point of this surface
// is that "why is there no alert for this job?" has a stored answer.
//
// The promoted row points at a REAL expected-signal rule, because rule_id is a
// foreign key — that constraint is the ownership marker the learner relies on,
// so a fixture that faked it would be testing a shape the daemon cannot produce.
func seedBaselineRows(t *testing.T, ctx context.Context, db *sqlite.DB, sourceID string) {
	t.Helper()
	rule := &store.MonitoringExpectedSignal{
		WorkspaceID: "ws-A", SourceID: sourceID, Name: "auto/tpl-orders",
		MatchSubstring: "order sync completed batch=", MinCount: 1,
		WindowSeconds: 3600, Severity: store.SeverityError,
		RequireSourceLiveness: true, Enabled: true,
	}
	store.ApplyExpectedSignalDefaults(rule)
	if err := db.CreateMonitoringExpectedSignal(ctx, rule); err != nil {
		t.Fatalf("create expected signal: %v", err)
	}
	now := time.Date(2026, 7, 20, 6, 0, 0, 0, time.UTC)
	rows := []*store.SignalBaseline{
		{
			WorkspaceID: "ws-A", SourceID: sourceID, TemplateID: "tpl-orders",
			RuleID: rule.ID, Masked: "order sync completed batch=<n>",
			MatchSubstring: "order sync completed batch=",
			Decision:       store.BaselinePromoted, Reason: "recurring every 10m0s",
			PeriodSeconds: 600, WindowSeconds: 3600, RelativeMAD: 0.02, P95Ratio: 1.04,
			SampleCount: 432, CyclesObserved: 432, HourOccupancy: 1, Confidence: 0.91,
			ObservedAt: now,
		},
		{
			WorkspaceID: "ws-A", SourceID: sourceID, TemplateID: "tpl-invoices",
			Masked:   "no invoices to send for <n> accounts",
			Decision: store.BaselineRejectConditionalTerminal,
			Reason: "this line says no work was done (\"no invoices to send\"), so it is " +
				"emitted on a conditional early-return branch",
			Confidence: 0.88, ObservedAt: now,
		},
		{
			WorkspaceID: "ws-A", SourceID: sourceID, TemplateID: "tpl-noise",
			Masked:     "request <uuid> completed in <dur>",
			Decision:   store.BaselineRejectIrregular,
			Reason:     "arrivals are not periodic: regularity 0.702 (max 0.35)",
			Confidence: 0.10, ObservedAt: now,
		},
	}
	for _, b := range rows {
		if err := db.UpsertSignalBaseline(ctx, b); err != nil {
			t.Fatalf("seed baseline %s: %v", b.TemplateID, err)
		}
	}
}

// TestMonitoringBaselinesExposesDecisionsAndRefusals is the inspectability
// guarantee. An operator must be able to see what the system decided normal
// looks like, how confident it is, and why a rule did or did not fire.
func TestMonitoringBaselinesExposesDecisionsAndRefusals(t *testing.T) {
	h, db, _ := newMonitoringOwnershipHandler(t)
	ctx := context.Background()
	sources, err := db.ListLogSources(ctx, "ws-A")
	if err != nil || len(sources) == 0 {
		t.Fatalf("fixture has no log source in ws-A: %v", err)
	}
	seedBaselineRows(t, ctx, db, sources[0].ID)

	text, isErr := monitoringToolText(t, h, "monitoring__baselines", `{"workspace_id":"ws-A"}`)
	if isErr {
		t.Fatalf("monitoring__baselines returned an error: %s", text)
	}
	var got struct {
		Baselines []monitoringBaselineView `json:"baselines"`
		Summary   struct {
			Examined   int            `json:"examined"`
			Monitored  int            `json:"monitored"`
			ByDecision map[string]int `json:"by_decision"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode %s: %v", text, err)
	}

	if got.Summary.Examined != 3 || got.Summary.Monitored != 1 {
		t.Errorf("summary examined=%d monitored=%d; want 3 and 1",
			got.Summary.Examined, got.Summary.Monitored)
	}
	byTemplate := map[string]monitoringBaselineView{}
	for _, b := range got.Baselines {
		byTemplate[b.TemplateID] = b
	}

	promoted := byTemplate["tpl-orders"]
	if !promoted.Promoted || promoted.RuleID == "" {
		t.Errorf("the promoted baseline did not surface its rule: %+v", promoted)
	}
	// The operator-facing line must state the CONSEQUENCE, not just a code —
	// a decision code alone does not say whether they are covered.
	for _, want := range []string{"MONITORED", "every 10m0s", "1h0m0s"} {
		if !strings.Contains(promoted.Explain, want) {
			t.Errorf("explain %q does not mention %q", promoted.Explain, want)
		}
	}
	if promoted.Confidence != 0.91 {
		t.Errorf("confidence = %v; want 0.91", promoted.Confidence)
	}

	refused := byTemplate["tpl-invoices"]
	if refused.Promoted {
		t.Error("a conditional-terminal refusal must not report as monitored")
	}
	if !strings.Contains(refused.Explain, "NOT MONITORED") ||
		!strings.Contains(refused.Explain, string(store.BaselineRejectConditionalTerminal)) {
		t.Errorf("explain %q must name the refusal so it can be argued with", refused.Explain)
	}
	if !strings.Contains(refused.Reason, "no work was done") {
		t.Errorf("reason %q lost the stored justification", refused.Reason)
	}
}

// TestMonitoringBaselinesFilters covers the two filters an operator reaches for:
// "show me only what is actually covered" and "show me one refusal class".
func TestMonitoringBaselinesFilters(t *testing.T) {
	h, db, _ := newMonitoringOwnershipHandler(t)
	ctx := context.Background()
	sources, err := db.ListLogSources(ctx, "ws-A")
	if err != nil || len(sources) == 0 {
		t.Fatalf("fixture has no log source in ws-A: %v", err)
	}
	seedBaselineRows(t, ctx, db, sources[0].ID)

	tests := []struct {
		name string
		args string
		want []string
	}{
		{
			name: "promoted only",
			args: `{"workspace_id":"ws-A","promoted_only":true}`,
			want: []string{"tpl-orders"},
		},
		{
			name: "one refusal class",
			args: `{"workspace_id":"ws-A","decision":"conditional_terminal"}`,
			want: []string{"tpl-invoices"},
		},
		{
			name: "scoped to the source",
			args: `{"workspace_id":"ws-A","source_id":"` + sources[0].ID + `"}`,
			want: []string{"tpl-orders", "tpl-invoices", "tpl-noise"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := monitoringToolText(t, h, "monitoring__baselines", tt.args)
			if isErr {
				t.Fatalf("error result: %s", text)
			}
			var got struct {
				Baselines []monitoringBaselineView `json:"baselines"`
			}
			if err := json.Unmarshal([]byte(text), &got); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(got.Baselines) != len(tt.want) {
				t.Fatalf("returned %d baselines; want %d", len(got.Baselines), len(tt.want))
			}
			seen := map[string]bool{}
			for _, b := range got.Baselines {
				seen[b.TemplateID] = true
			}
			for _, want := range tt.want {
				if !seen[want] {
					t.Errorf("missing %s from %v", want, seen)
				}
			}
		})
	}
}
