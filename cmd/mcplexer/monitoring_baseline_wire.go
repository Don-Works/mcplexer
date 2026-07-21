// monitoring_baseline_wire.go — daemon wiring for the auto-baseline learner and
// the absence evaluator.
//
// This file exists because the two halves it starts were both, at one point,
// correct code that NOTHING CALLED. store.EvaluateExpectedSignal was a pure,
// fully-tested absence evaluator with zero callers; the learner that gives it
// rules was 2400 lines with no boot path. A detector nobody starts is
// indistinguishable from no detector, and on 2026-07-20 that distinction cost
// seven hours and thirty-nine minutes of silence while a wedged order-sync job
// emitted nothing at all.
//
// Both loops honour the same single-runner gate as the collector and the
// renotify sweep: on a paired viewer machine the rules and incidents are
// replicated, but running the learner twice would double every write and running
// the evaluator twice would double every page.
package main

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"

	"github.com/don-works/mcplexer/internal/logwatch/baseline"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

var (
	// monitoringLearnOnce guards the learning loop: exactly one per daemon,
	// or two passes race on the same UNIQUE(template_id) baseline row.
	monitoringLearnOnce sync.Once
	// monitoringEvalOnce guards the absence tick. Two of them would raise
	// the same class twice per interval and double every alert.
	monitoringEvalOnce sync.Once
)

// startMonitoringBaseline launches the learner and the absence evaluator exactly
// once per daemon. Called from serve.go immediately after the collector, and
// asserted to be called by TestServeBootWiresMonitoringBaseline.
//
// It is deliberately tolerant: a store that cannot satisfy the interfaces, a
// missing dispatcher or a missing task service each disable one half with a log
// line rather than failing boot. Monitoring is an accessory to the daemon, not a
// precondition for it.
func startMonitoringBaseline(ctx context.Context, db store.Store, tasksSvc *tasks.Service) {
	if !monitoringRunnerEnabled() {
		slog.Info("monitoring: viewer mode — baseline learner and absence evaluator not started")
		return
	}
	startBaselineLearner(ctx, db)
	startBaselineEvaluator(ctx, db, tasksSvc)
}

// startBaselineLearner starts the loop that decides what normal looks like.
func startBaselineLearner(ctx context.Context, db store.Store) {
	learnStore, ok := db.(baseline.Store)
	if !ok {
		slog.Warn("monitoring: baseline learner not started (store lacks baseline support)")
		return
	}
	learner := baseline.NewLearner(learnStore)
	if learner == nil {
		return
	}
	monitoringLearnOnce.Do(func() {
		go learner.Run(ctx)
		slog.Info("monitoring: baseline learner started")
	})
}

// startBaselineEvaluator starts the loop that decides whether normal is still
// happening. Without it a learned rule is a row nobody reads.
func startBaselineEvaluator(ctx context.Context, db store.Store, tasksSvc *tasks.Service) {
	evalStore, ok := db.(baseline.EvalStore)
	if !ok {
		slog.Warn("monitoring: absence evaluator not started (store lacks expected-signal support)")
		return
	}
	if tasksSvc == nil {
		slog.Warn("monitoring: absence evaluator not started (no task service)")
		return
	}
	evaluator := baseline.NewEvaluator(
		evalStore, &baselineTaskEnsurer{tasks: tasksSvc}, monitoringDispatch)
	if evaluator == nil {
		slog.Warn("monitoring: absence evaluator not started (no dispatcher)")
		return
	}
	monitoringEvalOnce.Do(func() {
		go evaluator.Run(ctx)
		slog.Info("monitoring: absence evaluator started")
	})
}

// baselineTaskEnsurer elects the canonical task an absence incident hangs off.
//
// Idempotency is keyed on meta.logwatch_class, the same marker the triage path
// uses, so an absence task and a triage task for the same class converge on one
// row instead of two. This is what keeps a twelve-hour outage to ONE task rather
// than one per two-minute tick.
type baselineTaskEnsurer struct{ tasks *tasks.Service }

// Ensure returns the canonical task id for a class, creating it on first sight,
// then widens it to workspace visibility so the incident replicates to mirrored
// peers — a system task stays private otherwise and never reaches the peer
// dashboard. Widening is best-effort: a failure must not fail the tick.
func (e *baselineTaskEnsurer) Ensure(
	ctx context.Context, workspaceID, classKey, title, body, severity string,
) (string, error) {
	taskID, err := e.electCanonicalTask(ctx, workspaceID, classKey, title, body, severity)
	if err != nil || taskID == "" {
		return taskID, err
	}
	if pubErr := e.tasks.PublishSystemTask(ctx, taskID); pubErr != nil {
		slog.Warn("monitoring: absence task not widened for replication",
			"task", taskID, "class", classKey, "error", pubErr)
	}
	return taskID, nil
}

// electCanonicalTask reuses (or mints, or adopts on a uniqueness race) the one
// canonical task for a class.
func (e *baselineTaskEnsurer) electCanonicalTask(
	ctx context.Context, workspaceID, classKey, title, body, severity string,
) (string, error) {
	if existing, err := e.byClass(ctx, workspaceID, classKey); err != nil {
		return "", err
	} else if existing != "" {
		return existing, nil
	}
	task, err := e.tasks.Create(ctx, tasks.CreateOptions{
		WorkspaceID: workspaceID, Title: title, Description: body,
		Status: "open", Priority: baselineTaskPriority(severity),
		Tags: []string{"logwatch", "incident", "expected-signal"},
		Meta: `{"logwatch_class":` + jsonQuote(classKey) +
			`,"logwatch_kind":"expected_signal"}`,
		SourceKind: store.TaskSourceAgent, ActorKind: "system",
	})
	if err == nil {
		return task.ID, nil
	}
	if !errors.Is(err, store.ErrAlreadyExists) {
		return "", err
	}
	// A concurrent writer won the unique class index; adopt its row rather
	// than failing the tick.
	return e.byClass(ctx, workspaceID, classKey)
}

// byClass returns the OLDEST task carrying this class marker, so repeated
// lookups are stable even if a duplicate ever slipped in.
func (e *baselineTaskEnsurer) byClass(
	ctx context.Context, workspaceID, classKey string,
) (string, error) {
	rows, err := e.tasks.List(ctx, store.TaskFilter{
		WorkspaceID: workspaceID,
		MetaMatch:   map[string]string{"logwatch_class": classKey},
		Limit:       10,
	})
	if err != nil || len(rows) == 0 {
		return "", err
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].CreatedAt.Before(rows[j].CreatedAt) })
	return rows[0].ID, nil
}

// Close resolves the canonical task on recovery. An absence alert whose task is
// never closed trains operators to ignore the next one.
func (e *baselineTaskEnsurer) Close(ctx context.Context, workspaceID, taskID, note string) error {
	done, description := "done", note
	_, err := e.tasks.Update(ctx, workspaceID, taskID, tasks.UpdatePatch{
		Status: &done, Description: &description, ActorKind: "system",
	})
	return err
}

// baselineTaskPriority maps alert severity onto the task board's vocabulary.
func baselineTaskPriority(severity string) string {
	switch severity {
	case store.SeverityCritical:
		return "critical"
	case store.SeverityError, store.SeverityWarn:
		return "high"
	default:
		return "normal"
	}
}

// jsonQuote renders a string as a JSON scalar. Hand-rolled rather than pulled
// from encoding/json so building the two-key meta blob cannot fail and force an
// error path that has nothing useful to do.
func jsonQuote(s string) string {
	out := make([]rune, 0, len(s)+2)
	out = append(out, '"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			out = append(out, '\\', r)
		case '\n', '\r', '\t':
			out = append(out, ' ')
		default:
			out = append(out, r)
		}
	}
	return string(append(out, '"'))
}
