// handler_monitoring_triage_severity_test.go — the notification a sustained
// incident actually reaches the operator with.
//
// Escalation is only real if it survives delivery. The escalate dispatcher
// drops any notification ranked below channel.MinSeverity, which defaults to
// "error" (internal/store/sqlite/monitoring_channel.go). Dispatching the raw
// classifier severity meant the store computed an age escalation correctly and
// the gateway then threw it away at the channel floor: the incident escalated
// on paper and the operator heard nothing — the exact silence that let the
// 2026-07-20 hung-job incident run ~12h unnoticed.
package gateway

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

func TestMonitoringEffectiveSeverityFallback(t *testing.T) {
	tests := []struct {
		name     string
		result   *store.MonitoringTriageResult
		fallback string
		want     string
	}{
		{
			name:     "store-computed escalation wins over the classifier severity",
			result:   &store.MonitoringTriageResult{EffectiveSeverity: store.SeverityCritical},
			fallback: store.SeverityWarn,
			want:     store.SeverityCritical,
		},
		{
			name:     "no escalation keeps the classifier severity",
			result:   &store.MonitoringTriageResult{EffectiveSeverity: store.SeverityWarn},
			fallback: store.SeverityWarn,
			want:     store.SeverityWarn,
		},
		{
			name:     "empty effective severity falls back",
			result:   &store.MonitoringTriageResult{},
			fallback: store.SeverityWarn,
			want:     store.SeverityWarn,
		},
		{
			name:     "whitespace-only effective severity falls back",
			result:   &store.MonitoringTriageResult{EffectiveSeverity: "   "},
			fallback: store.SeverityError,
			want:     store.SeverityError,
		},
		{
			name:     "nil result falls back",
			result:   nil,
			fallback: store.SeverityWarn,
			want:     store.SeverityWarn,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := monitoringEffectiveSeverity(tc.result, tc.fallback); got != tc.want {
				t.Fatalf("monitoringEffectiveSeverity = %q, want %q", got, tc.want)
			}
		})
	}
}

// monitoringCommitArgsJSON builds a commit_triage payload that satisfies the
// non-benign title/body bounds, so the tests below only vary severity.
func monitoringCommitArgsJSON(t *testing.T, severity string) string {
	t.Helper()
	payload := map[string]any{
		"disposition": "actionable",
		"severity":    severity,
		"title":       "Order sync worker stopped completing",
		"body": "Observed evidence\n- api-A stopped emitting completion lines\n\n" +
			"Verified facts\n- the recurring job process is still alive\n\n" +
			"Hypotheses / unknowns\n- the job is wedged rather than crashed",
		"template_ids":    []string{"tpl-A"},
		"correlation_key": "api-A|ordersync.go:42",
		"workspace_id":    "ws-A",
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal commit args: %v", err)
	}
	return string(raw)
}

func TestMonitoringBoundedTriageSeverity(t *testing.T) {
	templates := []*store.LogTemplate{
		{Severity: store.SeverityInfo},
		{Severity: store.SeverityWarn},
	}
	for _, tc := range []struct {
		name, requested, want string
	}{
		{"model cannot promote past evidence", store.SeverityCritical, store.SeverityWarn},
		{"model may keep evidence severity", store.SeverityWarn, store.SeverityWarn},
		{"model may lower severity", store.SeverityInfo, store.SeverityInfo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := monitoringBoundedTriageSeverity(tc.requested, templates); got != tc.want {
				t.Fatalf("bounded severity = %q, want %q", got, tc.want)
			}
		})
	}
}

// A worker may explain evidence, but it cannot manufacture a higher page
// class than the deterministic template classifier observed.
func TestMonitoringCommitTriageCapsWorkerPromotionAtTemplateSeverity(t *testing.T) {
	h, db, notifier := newMonitoringOwnershipHandler(t)
	ctx := runner.WithWorkerRunCtx(context.Background(), runner.WorkerRunCtx{
		RunID: "run-severity-cap", WorkerID: "log-watch", WorkspaceID: "ws-A",
	})
	text, isErr := monitoringToolTextWithContext(
		t, ctx, h, "monitoring__commit_triage",
		monitoringCommitArgsJSON(t, store.SeverityCritical),
	)
	if isErr {
		t.Fatalf("commit failed: %s", text)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("decode commit: %v (%s)", err, text)
	}
	if decoded["severity"] != store.SeverityError ||
		decoded["requested_severity"] != store.SeverityCritical {
		t.Fatalf("severity audit fields = %#v", decoded)
	}
	if len(notifier.notes) != 1 {
		t.Fatalf("notification count = %d, want 1", len(notifier.notes))
	}
	if got := notifier.notes[0].Severity; got != store.SeverityError {
		t.Fatalf("dispatched severity = %q, want classifier cap %q", got, store.SeverityError)
	}
	incident, err := db.GetMonitoringIncidentByClass(ctx, "ws-A", "correlation:api a ordersync go")
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	if incident.Severity != store.SeverityError ||
		incident.LastNotifiedSeverity != store.SeverityError {
		t.Fatalf("incident recorded an unbounded worker severity: %+v", incident)
	}
}

// TestMonitoringCommitTriageFallsBackToClassifierSeverity covers the ordinary
// path: a brand-new incident has no age escalation, so the dispatched severity
// is the classifier's and must never be blank.
func TestMonitoringCommitTriageFallsBackToClassifierSeverity(t *testing.T) {
	h, _, notifier := newMonitoringOwnershipHandler(t)
	ctx := runner.WithWorkerRunCtx(context.Background(), runner.WorkerRunCtx{
		RunID: "run-no-escalation", WorkerID: "log-watch", WorkspaceID: "ws-A",
	})
	if text, isErr := monitoringToolTextWithContext(
		t, ctx, h, "monitoring__commit_triage", monitoringCommitArgsJSON(t, store.SeverityError),
	); isErr {
		t.Fatalf("commit failed: %s", text)
	}
	if len(notifier.notes) != 1 {
		t.Fatalf("notification count = %d, want 1", len(notifier.notes))
	}
	if got := notifier.notes[0].Severity; got != store.SeverityError {
		t.Fatalf("dispatched severity = %q, want %q", got, store.SeverityError)
	}
}
