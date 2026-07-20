// monitoring_tick_wire.go — the daemon's adapter for the gated on-demand
// learner/evaluator tick.
//
// The learner and the evaluator are stateless apart from the store, so a pass
// driven from the API can use freshly-built instances rather than reaching into
// the running goroutines' internals. Same store, same task ensurer, same
// dispatcher, same deterministic policy — the only thing this changes is WHEN a
// pass happens, which is exactly what an integration suite needs and nothing
// more.
package main

import (
	"context"

	"github.com/don-works/mcplexer/internal/api"
	"github.com/don-works/mcplexer/internal/logwatch/baseline"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

// monitoringTicker drives one pass of each loop. Either half may be nil when
// the store or the task service cannot support it; both methods are nil-safe.
type monitoringTicker struct {
	learner   *baseline.Learner
	evaluator *baseline.Evaluator
}

func (t *monitoringTicker) Learn(ctx context.Context) { t.learner.Learn(ctx) }

func (t *monitoringTicker) Evaluate(ctx context.Context) { t.evaluator.Evaluate(ctx) }

// newMonitoringTicker builds the adapter, or returns a nil interface when
// neither half can be assembled. Returning an explicit nil rather than a typed
// nil pointer matters: the handler checks the interface for nil and a typed nil
// would pass that check and then serve a misleading success.
func newMonitoringTicker(
	db store.Store, secretsMgr *secrets.Manager, meshMgr *mesh.Manager,
	human *notify.Bus, tasksSvc *tasks.Service,
) api.MonitoringTicker {
	ticker := &monitoringTicker{}
	if learnStore, ok := db.(baseline.Store); ok {
		ticker.learner = baseline.NewLearner(learnStore)
	}
	evalStore, ok := db.(baseline.EvalStore)
	if ok && tasksSvc != nil {
		ticker.evaluator = baseline.NewEvaluator(evalStore,
			&baselineTaskEnsurer{tasks: tasksSvc},
			ensureMonitoringDispatch(db, secretsMgr, meshMgr, human))
	}
	if ticker.learner == nil && ticker.evaluator == nil {
		return nil
	}
	return ticker
}
