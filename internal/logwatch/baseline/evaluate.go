// evaluate.go — the tick that runs the absence evaluator against learned rules.
//
// This is the half that makes the 2026-07-20 incident detectable. The learner
// decides what normal looks like; this decides, every couple of minutes,
// whether normal is still happening. Both are deterministic — the judgement is
// store.EvaluateExpectedSignal, a pure function, and nothing here consults a
// model or wakes a worker.
//
// The class key comes from the rule, so repeat ticks converge on ONE incident
// with a growing occurrence ledger rather than one incident per tick, and
// "the orders stopped" never merges with "we cannot see the orders" — different
// incidents with different fixes.
package baseline

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

// evaluateInterval is how often learned rules are re-checked.
//
// Every learned window is at least BaselineMinPromotedWindow (5 minutes), so a
// 2-minute tick can never be the reason an absence is missed, and detection
// latency is dominated by the window rather than by the sweep. Each tick is one
// indexed aggregate per rule; the rules are few because promotion is
// deliberately hard.
const evaluateInterval = 2 * time.Minute

// EvalStore is the evaluator's slice of storage. GetMonitoringIncidentByClass
// is needed on the recovery edge: RecordExpectedSignalOutcome clears the rule's
// incident latch as part of recovering, so the incident (and the task hanging
// off it) has to be resolved by its stable class key instead.
type EvalStore interface {
	store.MonitoringExpectedSignalStore
	GetMonitoringIncidentByClass(
		ctx context.Context, workspaceID, classKey string) (*store.MonitoringIncident, error)
}

// TaskEnsurer elects the canonical task an incident hangs off. Implemented in
// daemon wiring against tasks.Service, so this package stays free of the task
// service and its mesh plumbing.
//
// Ensure MUST be idempotent per class key: the incident machinery converges on
// one incident per class, and a task created per tick would undo that.
type TaskEnsurer interface {
	Ensure(ctx context.Context, workspaceID, classKey, title, body, severity string) (string, error)
	Close(ctx context.Context, workspaceID, taskID, note string) error
}

// Evaluator re-asks whether each learned signal is still arriving.
type Evaluator struct {
	store    EvalStore
	tasks    TaskEnsurer
	notifier distill.Notifier
	now      func() time.Time
	interval time.Duration
}

// NewEvaluator wires an evaluator. Returns nil when a dependency is missing so
// a caller can start it unconditionally at boot.
func NewEvaluator(st EvalStore, tasks TaskEnsurer, notifier distill.Notifier) *Evaluator {
	if st == nil || tasks == nil || notifier == nil {
		return nil
	}
	return &Evaluator{
		store: st, tasks: tasks, notifier: notifier,
		now: time.Now, interval: evaluateInterval,
	}
}

// Run loops until ctx is cancelled.
func (e *Evaluator) Run(ctx context.Context) {
	if e == nil {
		return
	}
	t := time.NewTicker(e.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Evaluate(ctx)
		}
	}
}

// Evaluate runs one pass over every enabled rule. Exported so the daemon and
// tests can drive a single tick without a timer.
func (e *Evaluator) Evaluate(ctx context.Context) {
	if e == nil {
		return
	}
	rules, err := e.store.ListEnabledMonitoringExpectedSignals(ctx)
	if err != nil {
		slog.Warn("baseline: list expected signals", "error", err)
		return
	}
	for _, rule := range rules {
		if ctx.Err() != nil {
			return
		}
		if err := e.evaluateRule(ctx, rule); err != nil {
			slog.Warn("baseline: evaluate expected signal",
				"rule", rule.Name, "error", err)
		}
	}
}

// evaluateRule observes, judges and records one rule.
//
// Deliberately knows nothing about deploys. A release is accounted for by
// SUBTRACTING its restart gap from the learner's evidence, never by suppressing
// a raise here — so there is no suppression state on this path that could fail
// to expire and swallow a real outage.
func (e *Evaluator) evaluateRule(ctx context.Context, rule *store.MonitoringExpectedSignal) error {
	now := e.now().UTC()
	observed, health, err := e.store.ObserveExpectedSignal(ctx, rule, now)
	if err != nil {
		if errors.Is(err, store.ErrLogSourceNotFound) {
			// The source was deleted out from under the rule. Silence is the
			// right response: there is nothing to observe and nothing to fix.
			return nil
		}
		return err
	}
	loc, err := rule.Location()
	if err != nil {
		return err
	}
	decision := store.EvaluateExpectedSignal(store.ExpectedSignalInput{
		Rule: *rule, Observed: observed, Health: health, Now: now, Location: loc,
	})
	taskID, err := e.ensureTask(ctx, rule, decision)
	if err != nil {
		return err
	}
	result, err := e.store.RecordExpectedSignalOutcome(ctx, store.ExpectedSignalRecord{
		RuleID: rule.ID, TaskID: taskID, Decision: decision, ObservedAt: now,
	})
	if err != nil {
		return err
	}
	return e.announce(ctx, rule, decision, result, now)
}

// ensureTask elects the canonical task, but only when the decision raises.
// A healthy tick must not create anything — that is the difference between a
// detector and a noise generator.
func (e *Evaluator) ensureTask(
	ctx context.Context, rule *store.MonitoringExpectedSignal, d store.ExpectedSignalDecision,
) (string, error) {
	if !d.Raise {
		return "", nil
	}
	return e.tasks.Ensure(ctx, rule.WorkspaceID, d.ClassKey, d.Title, d.Detail, d.Severity)
}

// announce dispatches notifications for raises and recoveries.
func (e *Evaluator) announce(
	ctx context.Context, rule *store.MonitoringExpectedSignal,
	d store.ExpectedSignalDecision, result *store.ExpectedSignalResult, now time.Time,
) error {
	if result == nil {
		return nil
	}
	if result.Recovered {
		return e.announceRecovery(ctx, rule, now)
	}
	if !result.ShouldNotify || result.Incident == nil {
		return nil
	}
	// EffectiveSeverity, never d.Severity: channel min_severity defaults to
	// "error", so an aged-up warn dispatched raw is filtered out at every
	// channel and the alert silently evaporates.
	severity := result.EffectiveSeverity
	if !store.ValidSeverity(severity) {
		severity = d.Severity
	}
	return e.notifier.Notify(ctx, distill.Notification{
		WorkspaceID: rule.WorkspaceID, Severity: severity,
		Title: d.Title, Body: absenceBody(rule, d, now),
		TaskID: result.Incident.TaskID, IncidentID: result.Incident.ID,
		NewIncident: result.NewIncident,
	})
}

// announceRecovery closes the loop: it notifies, and it closes the canonical
// task. An absence alert that is never followed by "it came back" — and whose
// task sits open forever — trains operators to ignore the next one.
//
// Both the absence and the collection class are resolved, because a rule can
// recover from either and leaving the other open would strand a task nobody
// will ever close by hand.
func (e *Evaluator) announceRecovery(
	ctx context.Context, rule *store.MonitoringExpectedSignal, now time.Time,
) error {
	note := "Expected signal " + rule.Name + " is arriving again as of " +
		now.UTC().Format(time.RFC3339) + "."
	incidentID, taskID := e.resolveRecoveredIncident(ctx, rule)
	err := e.notifier.Notify(ctx, distill.Notification{
		WorkspaceID: rule.WorkspaceID, Severity: store.SeverityInfo,
		Title:      "Recovered: expected signal " + rule.Name + " is arriving again",
		Body:       note,
		TaskID:     taskID,
		IncidentID: incidentID,
	})
	if err != nil {
		return err
	}
	if taskID != "" {
		if closeErr := e.tasks.Close(ctx, rule.WorkspaceID, taskID, note); closeErr != nil {
			slog.Warn("baseline: recovery notified but task not closed",
				"rule", rule.Name, "task", taskID, "error", closeErr)
		}
	}
	slog.Info("baseline: expected signal recovered", "rule", rule.Name,
		"workspace", rule.WorkspaceID)
	return nil
}

// resolveRecoveredIncident finds the incident the recovery clears. Best effort:
// a lookup failure must never stop the recovery notification going out.
func (e *Evaluator) resolveRecoveredIncident(
	ctx context.Context, rule *store.MonitoringExpectedSignal,
) (string, string) {
	for _, classKey := range []string{rule.AbsenceClassKey(), rule.CollectionClassKey()} {
		incident, err := e.store.GetMonitoringIncidentByClass(ctx, rule.WorkspaceID, classKey)
		if err != nil || incident == nil {
			continue
		}
		return incident.ID, incident.TaskID
	}
	return "", ""
}

// absenceBody renders the operator-facing explanation. Assembled from rule
// columns and the decision only, so it is identical for the same state and
// clock on every machine — and it always names the learned cadence, because
// "this normally runs every 10 minutes" is what makes the alert actionable.
func absenceBody(
	rule *store.MonitoringExpectedSignal, d store.ExpectedSignalDecision, now time.Time,
) string {
	body := d.Detail + "\n\nRule: " + rule.Name + " (learned automatically)\n" +
		"Window: " + rule.Window().String() + "\n" +
		"Evaluated: " + now.Format(time.RFC3339) + "\n"
	if rule.MatchSubstring != "" {
		body += "Matching: " + rule.MatchSubstring + "\n"
	}
	if rule.LastSignalAt != nil {
		body += "Last confirmed signal: " + rule.LastSignalAt.UTC().Format(time.RFC3339) + "\n"
	}
	if d.Outcome == store.OutcomeSignalCollection {
		body += "\nThis is a COLLECTION problem, not proof the signal stopped. " +
			"Fix visibility first.\n"
	}
	return body
}
