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
	"log/slog"
	"os"
	"strings"
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

// monitoringRunnerEnabled reports whether THIS daemon executes the
// collector loop. Default true; set MCPLEXER_MONITORING_RUNNER=0 on
// viewer daemons (personal laptops in a peer group whose LXC owns
// the job).
func monitoringRunnerEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("MCPLEXER_MONITORING_RUNNER")))
	return v != "0" && v != "false" && v != "off" && v != "no"
}

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
