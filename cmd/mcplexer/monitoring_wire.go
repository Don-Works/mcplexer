// monitoring_wire.go — daemon wiring for the Monitoring feature
// (remote log intelligence; docs/design/remote-log-intelligence.md).
//
// The services are process-wide singletons: the escalate dispatcher
// holds throttle state that MUST be shared across every gateway
// session, and exactly one collector loop may run per daemon. The
// single-runner contract for peer groups (ratified 2026-07-08: only
// the always-on LXC executes jobs; laptops are viewers) is expressed
// with MCPLEXER_MONITORING_RUNNER=0, which keeps the monitoring.*
// read/notify namespace available but never starts the pull loop.
package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/logwatch/collect"
	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/logwatch/escalate"
	"github.com/don-works/mcplexer/internal/logwatch/renotify"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/tasks"
)

var (
	monitoringOnce      sync.Once
	monitoringQry       *distill.Query
	monitoringDispatch  *escalate.Dispatcher
	monitoringCollector *collect.Manager
	monitoringColOnce   sync.Once
	// monitoringRenotifyOnce guards the persistence sweep: exactly one loop
	// per daemon, or a persistent incident is reminded about twice per tick.
	monitoringRenotifyOnce sync.Once
)

// monitoringRunnerEnabled defers to collect.RunnerEnabled so the
// daemon gate and the status API cannot drift.
func monitoringRunnerEnabled() bool { return collect.RunnerEnabled() }

// buildMonitoring lazily assembles the shared services. Safe to call
// from every gateway construction site; only the first call builds.
func buildMonitoring(db store.Store, secretsMgr *secrets.Manager, meshMgr *mesh.Manager) {
	monitoringOnce.Do(func() {
		senders := map[string]escalate.Sender{}
		if secretsMgr != nil {
			senders[store.ChannelKindGChatWebhook] = &escalate.GChatWebhookSender{Secrets: secretsMgr}
		}
		if meshMgr != nil {
			senders[store.ChannelKindMesh] = newMeshSender(meshMgr)
		}
		monitoringDispatch = escalate.NewDispatcher(db, senders)
		// Durable channel health. Without this the dispatcher still detects a
		// broken route and still logs it, but the state dies with the process
		// and no API can answer "is my alerting working?" — which is how a
		// gchat webhook stayed dead for six days behind the hourly notify cap.
		// Type-asserted at the seam (as the renotify sweep is) so the health
		// methods stay off store.Store and out of every mock in the tree.
		if healthStore, ok := db.(store.MonitoringChannelHealthStore); ok {
			monitoringDispatch.RegisterChannelHealthRecorder(healthStore)
		} else {
			slog.Warn("monitoring: channel health not persisted (store lacks health support)")
		}
		monitoringQry = distill.NewQuery(db)
		if secretsMgr != nil {
			distiller := distill.NewDistiller(db, monitoringDispatch)
			monitoringCollector = collect.NewManager(db, secretsMgr, distiller, nil)
		}
	})
	// Late-bind the mesh sender. The singleton is sealed by the FIRST
	// buildMonitoring call, and during daemon boot that first call often
	// lands before the mesh.Manager exists (meshMgr=nil). A later call
	// carrying a live manager must still register kind=mesh — otherwise a
	// configured Monitoring mesh channel logs "no sender wired for channel
	// kind" forever. RegisterSender replaces under the dispatcher lock and
	// leaves the throttle state and every other sender intact, so calling
	// it on each non-nil pass is idempotent. meshMgr==nil never unwires an
	// already-registered sender, so the wiring only ever ratchets on.
	if meshMgr != nil && monitoringDispatch != nil {
		monitoringDispatch.RegisterSender(store.ChannelKindMesh, newMeshSender(meshMgr))
	}
}

// newMeshSender builds the Monitoring mesh escalate sender. Factored out
// so the initial (in-Once) assembly and the late-bind path above stay in
// lock-step — both must produce the same session-meta binding.
func newMeshSender(meshMgr *mesh.Manager) *escalate.MeshSender {
	return &escalate.MeshSender{
		Mesh: meshMgr,
		WorkspaceMeta: func(workspaceID string) mesh.SessionMeta {
			return mesh.SessionMeta{
				SessionID:    "monitoring-dispatcher",
				WorkspaceIDs: []string{workspaceID},
				ClientType:   "system",
			}
		},
	}
}

// gatewayToolCaller adapts a gateway server into the escalate
// dispatcher's ToolCaller so the whatsapp sender rides the full
// dispatch pipeline (routing, secret substitution, audit).
type gatewayToolCaller struct{ gw *gateway.Server }

func (c gatewayToolCaller) CallTool(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return c.gw.CallTool(gateway.WithInProcessWorkerCall(ctx), name, args)
}

// registerMonitoringBridgeSenders attaches the senders whose
// dependencies come up later in daemon boot (telegram manager, the
// worker gateway used as the downstream bridge for openwa).
func registerMonitoringBridgeSenders(tg escalate.TelegramBridge, gw *gateway.Server) {
	if monitoringDispatch == nil {
		return
	}
	if tg != nil {
		monitoringDispatch.RegisterSender(store.ChannelKindTelegram, &escalate.TelegramSender{Bridge: tg})
	}
	if gw != nil {
		monitoringDispatch.RegisterSender(store.ChannelKindWhatsApp, &escalate.WhatsAppSender{Caller: gatewayToolCaller{gw: gw}})
	}
}

// ensureMonitoringDispatch builds (once) and returns the shared
// dispatcher — the REST Deps need it as a value, not a package var
// that might still be nil at construction order's mercy.
func ensureMonitoringDispatch(db store.Store, secretsMgr *secrets.Manager, meshMgr *mesh.Manager, human *notify.Bus) *escalate.Dispatcher {
	buildMonitoring(db, secretsMgr, meshMgr)
	if monitoringDispatch != nil && human != nil {
		monitoringDispatch.RegisterHumanPublisher(human)
	}
	return monitoringDispatch
}

// wireMonitoringGateway attaches the shared services to one gateway
// server so its sessions see the monitoring.* namespace.
func wireMonitoringGateway(gw *gateway.Server, db store.Store, secretsMgr *secrets.Manager, meshMgr *mesh.Manager) {
	if gw == nil || db == nil {
		return
	}
	buildMonitoring(db, secretsMgr, meshMgr)
	gw.SetMonitoring(monitoringQry, monitoringDispatch)
}

// startMonitoringCollector launches the pull loop exactly once per
// daemon, honouring the single-runner env gate. No-op without a
// secrets manager (SSH credentials are unreachable then).
//
// It is also the single boot entry point for every monitoring loop that
// is NOT the pull itself. Hanging them here rather than in serve.go is
// deliberate: each one is independently testable through this function,
// so "the daemon starts it" is asserted by a test rather than by a line
// of boot code nobody checks.
func startMonitoringCollector(
	ctx context.Context, db store.Store, secretsMgr *secrets.Manager,
	meshMgr *mesh.Manager, tasksSvc *tasks.Service,
) {
	buildMonitoring(db, secretsMgr, meshMgr)
	// The re-notification sweep is started before the collector's own gate:
	// it is independent of SSH credentials and must run wherever the
	// dispatcher does, or an unresolved incident goes quiet again.
	startMonitoringRenotify(ctx, db)
	// Same reasoning for baseline learning and absence evaluation: both read
	// rows the daemon already holds and neither needs SSH, so a daemon
	// without collector credentials must still learn what normal looks like
	// and still notice when a learned signal stops.
	startMonitoringBaseline(ctx, db, tasksSvc)
	if monitoringCollector == nil {
		slog.Info("monitoring: collector not started (no secrets manager)")
		return
	}
	if !monitoringRunnerEnabled() {
		slog.Info("monitoring: viewer mode — MCPLEXER_MONITORING_RUNNER=0, collector not started")
		return
	}
	monitoringColOnce.Do(func() {
		go monitoringCollector.Run(ctx)
		slog.Info("monitoring: collector started")
	})
}

// startMonitoringRenotify launches the persistence sweep exactly once per
// daemon. Without this goroutine the policy is only ever consulted when a
// worker triages, which is precisely the path a steady-severity recurring
// incident stops taking — the 2026-07-20 twelve-hour silence.
//
// It honours the same single-runner gate as the collector: on a viewer node
// the incidents are replicated but the notifications are not this daemon's to
// send, and two runners would double every reminder.
func startMonitoringRenotify(ctx context.Context, db store.Store) {
	if !monitoringRunnerEnabled() {
		slog.Info("monitoring: viewer mode — renotify sweep not started")
		return
	}
	renotifyStore, ok := db.(store.MonitoringRenotifyStore)
	if !ok {
		slog.Warn("monitoring: renotify sweep not started (store lacks renotify support)")
		return
	}
	sweeper := renotify.New(renotifyStore, monitoringDispatch)
	if sweeper == nil {
		slog.Warn("monitoring: renotify sweep not started (no dispatcher)")
		return
	}
	monitoringRenotifyOnce.Do(func() {
		go sweeper.Run(ctx)
		slog.Info("monitoring: renotify sweep started")
	})
}
