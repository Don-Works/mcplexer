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
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
)

var (
	monitoringOnce      sync.Once
	monitoringQry       *distill.Query
	monitoringDispatch  *escalate.Dispatcher
	monitoringCollector *collect.Manager
	monitoringColOnce   sync.Once
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
			senders[store.ChannelKindMesh] = &escalate.MeshSender{
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
		monitoringDispatch = escalate.NewDispatcher(db, senders)
		monitoringQry = distill.NewQuery(db)
		if secretsMgr != nil {
			distiller := distill.NewDistiller(db, monitoringDispatch)
			monitoringCollector = collect.NewManager(db, secretsMgr, distiller, nil)
		}
	})
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
func ensureMonitoringDispatch(db store.Store, secretsMgr *secrets.Manager, meshMgr *mesh.Manager) *escalate.Dispatcher {
	buildMonitoring(db, secretsMgr, meshMgr)
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
func startMonitoringCollector(ctx context.Context, db store.Store, secretsMgr *secrets.Manager, meshMgr *mesh.Manager) {
	buildMonitoring(db, secretsMgr, meshMgr)
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
