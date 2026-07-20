package baseline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestEvaluateRaisesAbsenceWhenLearnedJobGoesSilent is the acceptance test for
// the 2026-07-20 incident: a job that ran every ten minutes for days stops
// completing, the process stays alive, and nothing else in the system notices.
func TestEvaluateRaisesAbsenceWhenLearnedJobGoesSilent(t *testing.T) {
	st := newFakeEvalStore(learnedRule())
	signal := time.Date(2026, 7, 20, 2, 0, 0, 0, time.UTC)
	st.rule.LastSignalAt = &signal
	// No matching lines, but the source is otherwise alive and healthy: this
	// is a real absence, not lost visibility.
	st.observed = store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 4200}
	st.health = store.SourceCollectionHealth{Enabled: true}

	e, tasks, notifier := newTestEvaluator(st)
	e.Evaluate(context.Background())

	if len(st.records) != 1 {
		t.Fatalf("recorded %d outcomes; want 1", len(st.records))
	}
	d := st.records[0].Decision
	if d.Outcome != store.OutcomeSignalAbsent || !d.Raise {
		t.Fatalf("outcome = %q raise = %v; want absent + raise", d.Outcome, d.Raise)
	}
	if d.ClassKey != st.rule.AbsenceClassKey() {
		t.Errorf("class key = %q; want the absence class %q", d.ClassKey, st.rule.AbsenceClassKey())
	}
	if tasks.creates != 1 {
		t.Errorf("created %d tasks; want exactly 1", tasks.creates)
	}
	if len(notifier.sent) != 1 {
		t.Fatalf("sent %d notifications; want 1", len(notifier.sent))
	}
	if !strings.Contains(notifier.sent[0].Body, "order sync completed batch=") {
		t.Error("the alert must name the learned matcher so it is actionable")
	}
}

// TestEvaluateRaisesCollectionWhenSourceIsDark proves the honesty guarantee:
// when the collector is broken we say "we cannot see", on a DIFFERENT class
// key, rather than claiming the orders stopped.
func TestEvaluateRaisesCollectionWhenSourceIsDark(t *testing.T) {
	tests := []struct {
		name   string
		health store.SourceCollectionHealth
		total  int64
	}{
		{
			name:   "pulls failing past the threshold",
			health: store.SourceCollectionHealth{Enabled: true, ConsecutiveFailures: 3},
			total:  0,
		},
		{
			name:   "source produced no lines of any kind",
			health: store.SourceCollectionHealth{Enabled: true},
			total:  0,
		},
		{
			name:   "source disabled",
			health: store.SourceCollectionHealth{Enabled: false},
			total:  0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := newFakeEvalStore(learnedRule())
			signal := time.Date(2026, 7, 20, 2, 0, 0, 0, time.UTC)
			st.rule.LastSignalAt = &signal
			st.observed = store.ExpectedSignalObservation{MatchCount: 0, TotalLines: tt.total}
			st.health = tt.health

			e, _, notifier := newTestEvaluator(st)
			e.Evaluate(context.Background())

			d := st.records[0].Decision
			if d.Outcome != store.OutcomeSignalCollection {
				t.Fatalf("outcome = %q; want collection", d.Outcome)
			}
			if d.ClassKey != st.rule.CollectionClassKey() {
				t.Errorf("class key = %q; collection must never merge with absence", d.ClassKey)
			}
			if !strings.Contains(notifier.sent[0].Body, "COLLECTION problem") {
				t.Error("the alert must say plainly that this is not proof the signal stopped")
			}
		})
	}
}

// TestEvaluateRepeatTicksConvergeOnOneIncident is the anti-spam guarantee. The
// evaluator runs every couple of minutes; a sustained outage must produce one
// incident and one task, not one of each per tick.
func TestEvaluateRepeatTicksConvergeOnOneIncident(t *testing.T) {
	st := newFakeEvalStore(learnedRule())
	signal := time.Date(2026, 7, 20, 2, 0, 0, 0, time.UTC)
	st.rule.LastSignalAt = &signal
	st.observed = store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 4200}
	st.health = store.SourceCollectionHealth{Enabled: true}

	e, tasks, notifier := newTestEvaluator(st)
	for i := 0; i < 20; i++ {
		e.Evaluate(context.Background())
	}

	if len(st.incidents) != 1 {
		t.Fatalf("created %d incidents over 20 ticks; want 1", len(st.incidents))
	}
	if tasks.creates != 1 {
		t.Errorf("created %d tasks over 20 ticks; want 1", tasks.creates)
	}
	if len(notifier.sent) != 1 {
		t.Errorf("sent %d notifications over 20 ticks; want 1 (the rest is the "+
			"persistence policy's job, not a per-tick alarm)", len(notifier.sent))
	}
	incident := st.incidents[st.rule.AbsenceClassKey()]
	if incident.OccurrenceCount != 20 {
		t.Errorf("occurrence count = %d; want 20 — the evidence should accumulate "+
			"on the one incident", incident.OccurrenceCount)
	}
}

// TestEvaluateRecoveryClearsIncident closes the loop. An absence alert never
// followed by "it came back", with a task left open forever, trains operators
// to ignore the next one.
func TestEvaluateRecoveryClearsIncident(t *testing.T) {
	st := newFakeEvalStore(learnedRule())
	signal := time.Date(2026, 7, 20, 2, 0, 0, 0, time.UTC)
	st.rule.LastSignalAt = &signal
	st.observed = store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 4200}
	st.health = store.SourceCollectionHealth{Enabled: true}

	e, tasks, notifier := newTestEvaluator(st)
	e.Evaluate(context.Background())
	if st.rule.ActiveIncidentID == "" {
		t.Fatal("expected an active incident after the absence tick")
	}

	// The job comes back.
	st.observed = store.ExpectedSignalObservation{MatchCount: 6, TotalLines: 4300}
	e.Evaluate(context.Background())

	if st.rule.ActiveIncidentID != "" {
		t.Error("the incident latch must clear when the signal returns")
	}
	last := st.records[len(st.records)-1].Decision
	if last.Outcome != store.OutcomeSignalHealthy || !last.SignalPresent {
		t.Errorf("outcome = %q; want healthy", last.Outcome)
	}
	if len(notifier.sent) != 2 {
		t.Fatalf("sent %d notifications; want the raise plus a recovery", len(notifier.sent))
	}
	recovery := notifier.sent[1]
	if !strings.Contains(recovery.Title, "Recovered") {
		t.Errorf("recovery title = %q; want an explicit recovery", recovery.Title)
	}
	if len(tasks.closed) != 1 || tasks.closed[0] != "task-"+st.rule.AbsenceClassKey() {
		t.Errorf("closed tasks = %v; want the canonical absence task closed", tasks.closed)
	}
}

// TestEvaluateDoesNotFireOutsideActiveHours guards the false-positive that
// would get the whole channel muted.
func TestEvaluateDoesNotFireOutsideActiveHours(t *testing.T) {
	rule := learnedRule()
	// A 09:00-17:00 rule, being evaluated at 03:00.
	rule.ActiveStartMinute, rule.ActiveEndMinute = 9*60, 17*60
	signal := time.Date(2026, 7, 19, 16, 0, 0, 0, time.UTC)
	rule.LastSignalAt = &signal

	st := newFakeEvalStore(rule)
	st.observed = store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 0}
	st.health = store.SourceCollectionHealth{Enabled: true}

	e, tasks, notifier := newTestEvaluator(st)
	e.Evaluate(context.Background())

	if d := st.records[0].Decision; d.Raise {
		t.Fatalf("raised %q at 03:00 on a 09:00-17:00 rule", d.Outcome)
	}
	if tasks.creates != 0 || len(notifier.sent) != 0 {
		t.Errorf("created %d tasks and sent %d notifications outside active hours; want none",
			tasks.creates, len(notifier.sent))
	}
}

// TestEvaluateNeverFiresBeforeItHasSeenTheSignal is the bootstrap guard: a rule
// that has never observed its signal has not proven it CAN observe it.
func TestEvaluateNeverFiresBeforeItHasSeenTheSignal(t *testing.T) {
	st := newFakeEvalStore(learnedRule())
	st.rule.LastSignalAt = nil
	st.observed = store.ExpectedSignalObservation{MatchCount: 0, TotalLines: 4200}
	st.health = store.SourceCollectionHealth{Enabled: true}

	e, tasks, _ := newTestEvaluator(st)
	e.Evaluate(context.Background())

	if d := st.records[0].Decision; d.Outcome != store.OutcomeSignalAwaitingFirst {
		t.Fatalf("outcome = %q; want awaiting_first_signal", d.Outcome)
	}
	if tasks.creates != 0 {
		t.Error("a rule that has never seen its signal must not raise")
	}
}
