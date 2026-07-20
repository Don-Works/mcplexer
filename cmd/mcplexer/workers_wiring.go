package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/don-works/mcplexer/internal/consent"
	"github.com/don-works/mcplexer/internal/gateway"
	"github.com/don-works/mcplexer/internal/mesh"
	"github.com/don-works/mcplexer/internal/models"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/skillregistry"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
	triggermesh "github.com/don-works/mcplexer/internal/workers/triggers/mesh"
)

// buildWorkerRunner assembles the runner with thin shims around the
// real impls. Constructs nil-safe: any missing collaborator (mesh
// disabled, skills not seeded) reduces to a no-op shim so the runner
// still operates against the rest of the pipeline.
//
// Returns nil when the store is missing — the daemon should never
// invoke the runner without one. The caller stashes the result on
// serverDeps so M0.4 (scheduler dispatch) and M0.5 (admin run_now)
// can share one instance.
func buildWorkerRunner(d *serverDeps, db store.Store, dbDSN string) *runner.Runner {
	if d == nil || db == nil {
		return nil
	}
	// Construct the run-event bus eagerly and stash it on serverDeps so
	// the workers admin service can hand the same instance to the SSE
	// handler. Runner publishes, handler subscribes — one shared bus.
	// The same bus also carries lightweight "delegation_updated" signals
	// from the workers HTTP handler (create/review) so the Delegations
	// page can invalidate its REST-backed list in realtime.
	if d.workerRunBus == nil {
		d.workerRunBus = runner.NewRunBus()
	}
	// Stash the dispatcher on serverDeps so SetBuiltinCaller can be
	// wired after the worker-bound gateway.Server is built. The runner
	// holds the dispatcher via the ToolDispatcher interface, but it
	// can't (and shouldn't) reach back through that interface to set
	// the caller — keeping the concrete pointer in serverDeps is the
	// cleanest seam.
	d.workerDispatcher = newToolDispatcher(db, d.engine, d.manager)
	return runner.New(runner.Deps{
		Store:          db,
		Secrets:        secretAdapter{mgr: d.secretsMgr},
		Skills:         skillReaderAdapter{reg: d.skillRegistry},
		Dispatcher:     d.workerDispatcher,
		Mesh:           meshSenderAdapter{mgr: d.meshMgr},
		Auditor:        d.auditor,
		OutputsDir:     workerOutputsRoot(dbDSN),
		RunBus:         d.workerRunBus,
		Preamble:       gateway.WorkerPreamble(),
		PreambleCLI:    gateway.WorkerPreambleCLI(),
		PeerTiers:      newSameUserPeerLister(db, d.consentResolver),
		SelfDisplay:    selfDisplayLabel(d.selfUser),
		CLIToolCounter: db,
	})
}

// sameUserPeerLister implements runner.SameUserPeerLister via the
// consent resolver: enumerate every paired peer, classify, return
// true on first Tier-1 hit. Cheap: most setups have <10 paired peers,
// and TierFor() is one indexed lookup per peer.
type sameUserPeerLister struct {
	st       store.Store
	resolver consent.Resolver
}

// newSameUserPeerLister returns nil when either dependency is missing
// — the runner treats nil as "fire unconditionally", which is the
// correct fallback for tests + single-machine deployments where peer
// classification can't happen.
func newSameUserPeerLister(st store.Store, resolver consent.Resolver) runner.SameUserPeerLister {
	if st == nil || resolver == nil {
		return nil
	}
	return &sameUserPeerLister{st: st, resolver: resolver}
}

// HasSameUserPeer returns true when at least one paired peer is
// classified as TierSameUser. Errors fall to false (most-restrictive
// — better to skip the broadcast than to spam strangers).
func (s *sameUserPeerLister) HasSameUserPeer(ctx context.Context) bool {
	if s == nil || s.st == nil || s.resolver == nil {
		return false
	}
	peers, err := s.st.ListPeers(ctx)
	if err != nil {
		slog.Warn("consolidator: ListPeers failed; skipping Tier-1 broadcast",
			"error", err)
		return false
	}
	for _, p := range peers {
		if s.resolver.TierFor(ctx, p.PeerID) == consent.TierSameUser {
			return true
		}
	}
	return false
}

// selfDisplayLabel composes the "alice@m1"-style human label for the
// consolidator's mesh broadcast content. Falls back gracefully when
// either component is missing — empty string lets the runner default
// to "self".
func selfDisplayLabel(selfUser *store.User) string {
	if selfUser == nil {
		return ""
	}
	name := strings.TrimSpace(selfUser.DisplayName)
	if name == "" {
		name = strings.TrimSpace(selfUser.UserID)
	}
	if name == "" {
		return ""
	}
	host, _ := os.Hostname()
	host = strings.TrimSpace(host)
	if host == "" {
		return name
	}
	// Trim the macOS .local suffix so "user@host.local" reads as
	// "user@host" — the short form fits the mesh-content one-liner
	// without dropping useful identity bits.
	host = strings.TrimSuffix(host, ".local")
	return name + "@" + host
}

// workerBuiltinAdapter satisfies runner.BuiltinToolCaller by delegating
// to the worker-bound *gateway.Server. The Server's handler has no
// initialized session (workers never perform an MCP initialize), so the
// session-keyed paths inside handleToolsCall fall back to global routes
// and admin-CWD gating denies admin tools. Concurrent worker runs share
// the same Server safely: handleToolsCall reads session state but never
// mutates it after newHandler returns.
type workerBuiltinAdapter struct {
	gw *gateway.Server
}

// CallBuiltin forwards through the gateway's full tool pipeline. The
// ctx is marked in-process so the admin CWD gate lets admin tools
// through — workers are operator-configured + per-worker allowlist-
// gated, and they never cross the JSON-RPC boundary the CWD gate is
// designed to protect against.
func (a workerBuiltinAdapter) CallBuiltin(ctx context.Context, name string, args json.RawMessage) (json.RawMessage, error) {
	return a.gw.CallTool(gateway.WithInProcessWorkerCall(ctx), name, args)
}

// WorkerToolSurface translates the gateway's WorkerToolSchema (which
// keeps internal/gateway free of an internal/models dependency) into
// the models.ToolSchema shape the runner consumes.
func (a workerBuiltinAdapter) WorkerToolSurface(ctx context.Context) []models.ToolSchema {
	raw := a.gw.WorkerToolSurface(ctx)
	out := make([]models.ToolSchema, len(raw))
	for i, t := range raw {
		schema := map[string]any{}
		if len(t.InputSchema) > 0 {
			_ = json.Unmarshal(t.InputSchema, &schema)
		}
		out[i] = models.ToolSchema{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: schema,
		}
	}
	return out
}

// workerOutputsRoot derives the file-channel jail directory from the
// daemon's DB DSN. The default data dir holds mcplexer.db plus per-area
// subdirs (skills/, mesh/, etc.); worker file outputs land under
// "<data_dir>/worker-outputs/" so operators can clean them up
// independently of the database.
func workerOutputsRoot(dbDSN string) string {
	if dbDSN == "" {
		return ""
	}
	dir := filepath.Dir(dbDSN)
	if dir == "" || dir == "." {
		return ""
	}
	return filepath.Join(dir, "worker-outputs")
}

// secretAdapter wraps *secrets.Manager so a missing manager fails with
// a clear runner-shaped error rather than a nil-deref. Workers that
// don't need a key (SecretScopeID="") never hit Get so the nil-manager
// path is fine in that case.
type secretAdapter struct {
	mgr *secrets.Manager
}

// Get returns the secret bytes for (scopeID, key). Errors when the
// manager isn't wired or the scope/key is missing.
func (s secretAdapter) Get(ctx context.Context, scopeID, key string) ([]byte, error) {
	if s.mgr == nil {
		return nil, errors.New("secrets manager not wired")
	}
	return s.mgr.Get(ctx, scopeID, key)
}

// skillReaderAdapter wraps *skillregistry.Registry, translating the
// runner's (name, version) convention into Registry.Get's
// (scope, name, VersionRef) signature. It implements workspace-then-global
// fallback: when workspaceID is non-empty, the worker's workspace scope
// (which unions globals) is tried first so workspace-scoped skills resolve
// and shadow same-named globals; empty workspaceID falls back to pure global.
type skillReaderAdapter struct {
	reg *skillregistry.Registry
}

// GetSkillBody returns the skill body markdown. Empty version maps to
// "latest" (the active head). The "@stable" alias is honoured.
// workspaceID selects the primary scope for workspace-scoped skill resolution
// with global fallback.
func (s skillReaderAdapter) GetSkillBody(ctx context.Context, workspaceID, name, version string) (string, error) {
	if s.reg == nil {
		return "", errors.New("skill registry not wired")
	}
	ref, err := skillregistry.ParseVersionRef(versionOrLatest(version))
	if err != nil {
		return "", fmt.Errorf("parse version %q: %w", version, err)
	}
	scope := skillregistry.GlobalScope()
	if workspaceID != "" {
		scope = store.SkillScope{WorkspaceIDs: []string{workspaceID}}
	}
	entry, err := s.reg.Get(ctx, scope, name, ref)
	if err != nil {
		return "", err
	}
	if entry == nil {
		return "", store.ErrNotFound
	}
	rendered, err := s.reg.RenderEntry(ctx, entry)
	if err != nil {
		return "", fmt.Errorf("render skill %q: %w", name, err)
	}
	return rendered.Body, nil
}

func versionOrLatest(version string) any {
	v := strings.TrimSpace(version)
	if v == "" {
		return "latest"
	}
	return v
}

// meshSenderAdapter wraps *mesh.Manager so the runner can fire
// worker.* lifecycle signals without taking a mesh.SessionMeta. The
// shim constructs a per-worker SessionMeta so multi-worker setups
// stay distinguishable in mesh queries.
type meshSenderAdapter struct {
	mgr *mesh.Manager
}

// Send pushes a worker-emitted message into the mesh. WorkspaceID is
// stamped onto the SessionMeta so subscribers that filter by workspace
// (telegram bridge, etc.) see the message. NotifyUser fires the
// notify-bus event; ReplyTo threads the emission to an upstream id.
func (m meshSenderAdapter) Send(ctx context.Context, out runner.MeshOutbound) (string, error) {
	if m.mgr == nil {
		return "", nil // mesh disabled — no-op, no error
	}
	clientType := "worker"
	if out.AgentDisplayName != "" {
		clientType = out.AgentDisplayName
	}
	meta := mesh.SessionMeta{
		SessionID:  "worker:" + out.WorkerID,
		ClientType: clientType,
	}
	if out.WorkspaceID != "" {
		meta.WorkspaceIDs = []string{out.WorkspaceID}
	}
	msg, err := m.mgr.Send(ctx, meta, mesh.SendRequest{
		Kind:       out.Kind,
		Priority:   out.Priority,
		Content:    out.Content,
		Tags:       out.Tags,
		NotifyUser: out.NotifyUser,
		ReplyTo:    out.ReplyTo,
		ToPeer:     out.ToPeer,
		LocalOnly:  out.ToPeer == "" && !out.BroadcastPeers,
		ActorKind:  "worker",
	})
	if err != nil {
		return "", err
	}
	if msg == nil {
		return "", nil
	}
	return msg.ID, nil
}

// mesh4dispatcher constructs the M4 mesh-trigger dispatcher with all the
// real collaborators. Kept as a small free function so serve.go's wiring
// block reads top-to-bottom; the dispatcher's narrow Deps make this
// glue-only.
func mesh4dispatcher(
	st triggermesh.TriggerStore,
	runner triggermesh.WorkerExecutor,
	auditor triggermesh.Auditor,
	meshMgr *mesh.Manager,
) *triggermesh.Dispatcher {
	selfPeer := ""
	if meshMgr != nil {
		selfPeer = meshMgr.SelfPeerID()
	}
	return triggermesh.New(triggermesh.Deps{
		Store:    st,
		Runner:   runner,
		Auditor:  auditor,
		SelfPeer: selfPeer,
	})
}

// meshSubscribeAdapter adapts *mesh.Manager.Subscribe to the dispatcher's
// MeshSubscriber interface — the only reason this isn't *mesh.Manager
// directly is that the dispatcher's interface uses a non-receiver func
// signature so it can be implemented by test fakes without taking on a
// hard mesh dependency.
type meshSubscribeAdapter struct {
	mgr *mesh.Manager
}

// Subscribe wires the dispatcher into the mesh manager.
func (a meshSubscribeAdapter) Subscribe(
	fn func(ctx context.Context, msg *store.MeshMessage),
) func() {
	if a.mgr == nil {
		return func() {}
	}
	return a.mgr.Subscribe(fn)
}
