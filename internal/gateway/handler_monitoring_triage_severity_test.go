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

// observeTemplateAt moves tpl-A's last_seen, which is what
// monitoringTemplatesForCommit derives observedAt from. observedAt is the
// clock the notification policy runs on, so this is how the test drives
// incident age deterministically without touching the wall clock.
func observeTemplateAt(t *testing.T, h *handler, at time.Time) {
	t.Helper()
	tpl, err := h.store.GetLogTemplate(context.Background(), "tpl-A")
	if err != nil {
		t.Fatalf("get tpl-A: %v", err)
	}
	tpl.LastSeen = at
	if _, err := h.store.UpsertLogTemplate(context.Background(), tpl, 1); err != nil {
		t.Fatalf("upsert tpl-A at %s: %v", at, err)
	}
}

// TestMonitoringCommitTriageDispatchesEffectiveSeverity drives a warn incident
// past the 12h age-escalation tier and asserts the notification is dispatched
// AND recorded at the escalated severity. Before the fix both used the raw
// "warn" classifier severity, which ranks below the default "error" channel
// floor and is therefore silently dropped at delivery.
func TestMonitoringCommitTriageDispatchesEffectiveSeverity(t *testing.T) {
	h, db, notifier := newMonitoringOwnershipHandler(t)
	ctx := runner.WithWorkerRunCtx(context.Background(), runner.WorkerRunCtx{
		RunID: "run-age-escalation", WorkerID: "log-watch", WorkspaceID: "ws-A",
	})
	args := monitoringCommitArgsJSON(t, store.SeverityWarn)

	// First sighting, 13h ago. One occurrence bucket — not yet sustained, so
	// the incident is notified at its plain classifier severity.
	first := time.Now().UTC().Add(-13 * time.Hour)
	observeTemplateAt(t, h, first)
	if text, isErr := monitoringToolTextWithContext(t, ctx, h, "monitoring__commit_triage", args); isErr {
		t.Fatalf("first commit failed: %s", text)
	}
	if len(notifier.notes) != 1 || notifier.notes[0].Severity != store.SeverityWarn {
		t.Fatalf("first notification severity = %+v, want one note at warn", notifier.notes)
	}

	incident, err := db.GetMonitoringIncidentByClass(ctx, "ws-A", "correlation:api a ordersync go")
	if err != nil {
		t.Fatalf("read incident: %v", err)
	}
	// The handler stamps last_notified_at from the wall clock while the
	// incident's own first_seen/last_seen live on the observedAt timeline.
	// Pin it back onto that timeline so the age-tier comparison under test is
	// the one a real deployment makes.
	if err := db.MarkMonitoringIncidentNotified(ctx, incident.ID, store.SeverityWarn, first); err != nil {
		t.Fatalf("pin last_notified_at: %v", err)
	}

	// Still recurring 12.5h later: a second occurrence bucket makes it
	// sustained, and the age crosses tier 2.
	second := first.Add(12*time.Hour + 30*time.Minute)
	observeTemplateAt(t, h, second)
	text, isErr := monitoringToolTextWithContext(t, ctx, h, "monitoring__commit_triage", args)
	if isErr {
		t.Fatalf("second commit failed: %s", text)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		t.Fatalf("decode second commit: %v (%s)", err, text)
	}
	if decoded["notification_dispatched"] != true {
		t.Fatalf("second commit did not notify: %#v", decoded)
	}

	if len(notifier.notes) != 2 {
		t.Fatalf("notification count = %d, want 2", len(notifier.notes))
	}
	escalated := notifier.notes[1]
	// warn (rank 1) raised by two age tiers = critical.
	if escalated.Severity != store.SeverityCritical {
		t.Fatalf("dispatched severity = %q, want %q (escalated); the classifier severity is dropped at the default error channel floor",
			escalated.Severity, store.SeverityCritical)
	}
	if store.SeverityRank(escalated.Severity) < store.SeverityRank(store.SeverityError) {
		t.Fatalf("dispatched severity %q ranks below the default channel min_severity; it would never be delivered",
			escalated.Severity)
	}

	// The recorded notification severity must match what was dispatched, so
	// the next decision compares against what the operator actually received.
	after, err := db.GetMonitoringIncidentByClass(ctx, "ws-A", "correlation:api a ordersync go")
	if err != nil {
		t.Fatalf("re-read incident: %v", err)
	}
	if after.LastNotifiedSeverity != store.SeverityCritical {
		t.Fatalf("last_notified_severity = %q, want %q", after.LastNotifiedSeverity, store.SeverityCritical)
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
