package baseline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// absenceScenario stages the 2026-07-20 incident: a learned job goes silent
// while the source is otherwise alive and healthy.
func absenceScenario() *fakeEvalStore {
	st := newFakeEvalStore(learnedRule())
	signal := time.Date(2026, 7, 20, 2, 0, 0, 0, time.UTC)
	st.rule.LastSignalAt = &signal
	st.observed = store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 4200}
	st.health = store.SourceCollectionHealth{Enabled: true}
	return st
}

// TestEvaluateAbsenceAlertIsHumanReadable is the end-to-end guarantee behind the
// CRITICAL fix: the notification, the canonical task and the stored incident all
// lead with the matched signal and the source's DISPLAY name — never the
// learner's auto/<hash> rule name, which survives only as a body footer.
func TestEvaluateAbsenceAlertIsHumanReadable(t *testing.T) {
	st := absenceScenario()
	e, tasks, notifier := newTestEvaluator(st)
	e.Evaluate(context.Background())

	if len(notifier.sent) != 1 {
		t.Fatalf("sent %d notifications; want 1", len(notifier.sent))
	}
	n := notifier.sent[0]

	// Title: WHAT (matched text), WHERE (display name), HOW ABNORMAL — no hash.
	for _, want := range []string{`"order sync completed batch="`, "orders-api", "saw 0"} {
		if !strings.Contains(n.Title, want) {
			t.Errorf("title %q missing %q", n.Title, want)
		}
	}
	if strings.Contains(n.Title, "auto/") {
		t.Errorf("title must not headline the auto/ rule name: %q", n.Title)
	}
	if strings.ContainsAny(n.Title, "\n\r") {
		t.Errorf("title must be one line: %q", n.Title)
	}
	if n.SourceName != "orders-api" {
		t.Errorf("SourceName = %q; want the resolved display name", n.SourceName)
	}
	// The auto/ name is demoted to a stable footer identifier in the body.
	if !strings.Contains(n.Body, "Rule: auto/tpl-orders-s (learned automatically)") {
		t.Errorf("body must keep the auto/ id as a footer:\n%s", n.Body)
	}
	if !strings.Contains(n.Body, "orders-api") {
		t.Errorf("body must name the source by its display name:\n%s", n.Body)
	}

	// The stored incident (what the dashboard renders) carries the same text.
	incident := st.incidents[st.rule.AbsenceClassKey()]
	if incident == nil || incident.Title != n.Title {
		t.Fatalf("incident title = %q; want it to match the human alert title %q",
			incidentTitle(incident), n.Title)
	}

	// The canonical task the operator opens is human-readable too.
	taskTitle := tasks.titles[st.rule.AbsenceClassKey()]
	for _, want := range []string{"order sync completed batch=", "orders-api"} {
		if !strings.Contains(taskTitle, want) {
			t.Errorf("task title %q missing %q", taskTitle, want)
		}
	}
	if strings.Contains(taskTitle, "auto/") {
		t.Errorf("task title must not headline the auto/ name: %q", taskTitle)
	}
}

// TestEvaluateAbsenceAlertFallsBackToSourceID proves source-name resolution is
// best-effort: when the source cannot be read the alert degrades to the raw id
// rather than failing or going blank.
func TestEvaluateAbsenceAlertFallsBackToSourceID(t *testing.T) {
	st := absenceScenario()
	st.sourceErr = errors.New("source lookup failed")
	e, _, notifier := newTestEvaluator(st)
	e.Evaluate(context.Background())

	if len(notifier.sent) != 1 {
		t.Fatalf("sent %d notifications; want 1", len(notifier.sent))
	}
	n := notifier.sent[0]
	if !strings.Contains(n.Title, "src-1") {
		t.Errorf("title should fall back to the raw source id: %q", n.Title)
	}
	if strings.Contains(n.Title, "orders-api") {
		t.Errorf("title should not name a source it could not resolve: %q", n.Title)
	}
	if n.SourceName != "src-1" {
		t.Errorf("SourceName = %q; want the raw id fallback", n.SourceName)
	}
}

// TestEvaluateRecoveryAlertIsHumanReadable checks the recovery edge names the
// same signal and source as the raise.
func TestEvaluateRecoveryAlertIsHumanReadable(t *testing.T) {
	st := absenceScenario()
	e, _, notifier := newTestEvaluator(st)
	e.Evaluate(context.Background())

	// The job comes back.
	st.observed = store.ExpectedSignalObservation{MatchCount: 6, TotalLines: 4300}
	e.Evaluate(context.Background())

	if len(notifier.sent) != 2 {
		t.Fatalf("sent %d notifications; want the raise plus a recovery", len(notifier.sent))
	}
	rec := notifier.sent[1]
	if !strings.HasPrefix(rec.Title, "Recovered:") {
		t.Errorf("recovery title must announce recovery: %q", rec.Title)
	}
	for _, want := range []string{"order sync completed batch=", "orders-api"} {
		if !strings.Contains(rec.Title, want) {
			t.Errorf("recovery title %q missing %q", rec.Title, want)
		}
	}
	if strings.Contains(rec.Title, "auto/") {
		t.Errorf("recovery title must not headline the auto/ name: %q", rec.Title)
	}
	if rec.SourceName != "orders-api" {
		t.Errorf("recovery SourceName = %q; want the resolved display name", rec.SourceName)
	}
}

func incidentTitle(i *store.MonitoringIncident) string {
	if i == nil {
		return "<nil incident>"
	}
	return i.Title
}
