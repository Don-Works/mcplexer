// Package mesh implements the M4 mesh-trigger dispatcher: it subscribes
// to the mesh manager's insert stream, matches incoming MeshMessages
// against persisted WorkerMeshTrigger rows, applies throttle + loop +
// permission guards, and fires the matched Worker via the runner.
//
// The dispatcher is intentionally store-driven: every trigger lives in
// SQLite (worker_mesh_triggers), the worker fires via the same runner
// schedule + run-now use, and audit emissions land in the existing
// audit ledger. M4 adds zero new persistence outside the trigger table
// + four nullable columns on worker_runs.
package mesh

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
)

// triggerScopePrefix is the per-peer scope namespace the dispatcher
// requires for cross-peer mesh triggers. A peer with
// "trigger_worker:<name>" can fire that worker; "trigger_worker:*"
// fires any worker on this daemon.
const triggerScopePrefix = "trigger_worker:"

// defaultRunTimeout is the outer ceiling when a worker has no
// MaxWallClockSeconds set. The runner's loop has its own internal caps
// (Caps.MaxWallClock); this dispatcher-side ctx-timeout is an OUTER
// guard so a hung runner can't keep a subscriber goroutine alive
// forever. Per-worker overrides win — see runTimeoutFor(worker).
const defaultRunTimeout = 300 * time.Second

// runTimeoutFor returns the outer ctx-timeout the dispatcher should use
// for a worker run. The worker's own MaxWallClockSeconds wins when
// positive; we add a 30s buffer so the runner's internal cap fires
// first (giving us a clean status=failure row instead of an outer
// ctx-cancel mid-adapter). Zero / negative falls back to the default.
func runTimeoutFor(worker *store.Worker) time.Duration {
	if worker == nil || worker.MaxWallClockSeconds <= 0 {
		return defaultRunTimeout
	}
	return time.Duration(worker.MaxWallClockSeconds+30) * time.Second
}

// TriggerStore is the narrow store surface the dispatcher consumes.
// store.Store satisfies it directly; tests inject a fake.
type TriggerStore interface {
	ListAllEnabledMeshTriggers(ctx context.Context) ([]*store.WorkerMeshTrigger, error)
	GetWorker(ctx context.Context, id string) (*store.Worker, error)
	HasPeerScope(ctx context.Context, peerID, scope string) (bool, error)
}

// WorkerExecutor is the runner-shaped surface — same interface the
// scheduler consumes. *runner.Runner satisfies it.
type WorkerExecutor interface {
	RunWithOpts(ctx context.Context, workerID string, opts runner.RunOpts) (string, error)
}

// MeshSubscriber is the mesh-side hook. *mesh.Manager.Subscribe returns
// the unsubscribe closure this interface needs.
type MeshSubscriber interface {
	Subscribe(fn func(ctx context.Context, msg *store.MeshMessage)) (unsubscribe func())
}

// Auditor records dispatcher decisions. Optional; nil makes every emit
// a no-op so non-daemon paths (CLI / tests) don't need to wire audit.
type Auditor interface {
	Record(ctx context.Context, rec *store.AuditRecord) error
}

// Clock abstracts time.Now for deterministic throttle tests.
type Clock interface {
	Now() time.Time
}

// realClock returns time.Now().UTC().
type realClock struct{}

// Now implements Clock.
func (realClock) Now() time.Time { return time.Now().UTC() }

// Deps bundles the dispatcher's collaborators. All fields except Store
// and Runner are optional; the dispatcher degrades gracefully (mesh
// nil → never subscribes; audit nil → no records).
type Deps struct {
	Store    TriggerStore
	Runner   WorkerExecutor
	Auditor  Auditor
	Clock    Clock
	SelfPeer string // local libp2p peer id; empty when p2p disabled
}

// Dispatcher is the in-memory matcher + throttle + permission gate that
// fires Workers when matching mesh messages arrive. Construct via New.
type Dispatcher struct {
	store    TriggerStore
	runner   WorkerExecutor
	auditor  Auditor
	clock    Clock
	selfPeer string

	cacheMu  sync.RWMutex
	cache    []*store.WorkerMeshTrigger
	cacheTS  time.Time
	cacheErr error

	throttleMu sync.Mutex
	throttle   map[string]time.Time
}

// New constructs a Dispatcher. Store + Runner are mandatory; the rest
// fall back to safe defaults.
func New(deps Deps) *Dispatcher {
	d := &Dispatcher{
		store:    deps.Store,
		runner:   deps.Runner,
		auditor:  deps.Auditor,
		clock:    deps.Clock,
		selfPeer: deps.SelfPeer,
		throttle: map[string]time.Time{},
	}
	if d.clock == nil {
		d.clock = realClock{}
	}
	return d
}

// Subscribe attaches the dispatcher to a mesh manager. Returns the
// underlying unsubscribe closure so wiring code can register cleanup
// on serverDeps.cleanups.
func (d *Dispatcher) Subscribe(m MeshSubscriber) func() {
	if d == nil || m == nil {
		return func() {}
	}
	// Initial cache hydrate — best-effort. A failure here just means the
	// first message triggers a lazy reload via reloadIfStale.
	if err := d.Reload(context.Background()); err != nil {
		slog.Warn("mesh trigger dispatcher: initial reload failed", "error", err)
	}
	return m.Subscribe(d.OnMessage)
}

// Reload re-reads the trigger cache. Called at boot + after every CRUD
// invalidation from the admin service.
func (d *Dispatcher) Reload(ctx context.Context) error {
	if d == nil || d.store == nil {
		return nil
	}
	triggers, err := d.store.ListAllEnabledMeshTriggers(ctx)
	d.cacheMu.Lock()
	defer d.cacheMu.Unlock()
	d.cacheTS = d.clock.Now()
	if err != nil {
		d.cacheErr = err
		return err
	}
	d.cache = triggers
	d.cacheErr = nil
	return nil
}

// snapshot returns a copy of the trigger cache safe to iterate without
// holding the lock during dispatch.
func (d *Dispatcher) snapshot() []*store.WorkerMeshTrigger {
	d.cacheMu.RLock()
	defer d.cacheMu.RUnlock()
	out := make([]*store.WorkerMeshTrigger, len(d.cache))
	copy(out, d.cache)
	return out
}

// OnMessage is the mesh-subscriber callback — public so dispatch tests
// can invoke it directly without standing up a Manager.
func (d *Dispatcher) OnMessage(ctx context.Context, msg *store.MeshMessage) {
	if d == nil || msg == nil {
		return
	}
	triggers := d.snapshot()
	if len(triggers) == 0 {
		// Lazy reload — happens on the very first message if the cache
		// hadn't been populated yet (e.g. wiring race during boot).
		_ = d.Reload(ctx)
		triggers = d.snapshot()
		if len(triggers) == 0 {
			return
		}
	}
	for _, t := range triggers {
		d.dispatchOne(ctx, t, msg)
	}
}

// dispatchOne runs every guard for one trigger × message pair and fires
// the runner (in its own goroutine with an outer timeout) when admitted.
func (d *Dispatcher) dispatchOne(
	ctx context.Context, t *store.WorkerMeshTrigger, msg *store.MeshMessage,
) {
	if !t.Enabled {
		return
	}
	if !matchesTrigger(t, msg) {
		return
	}
	// Workspace isolation. Always fetch the worker so we can match the
	// inbound message workspace against its explicit readable grant set;
	// a peer-broadcast that landed in an ungranted workspace MUST NOT
	// fire this worker.
	worker, err := d.store.GetWorker(ctx, t.WorkerID)
	if err != nil {
		d.audit(ctx, t, msg, "", chainDepthFromTags(msg.Tags), "denied", "worker_lookup_failed")
		return
	}
	depth := chainDepthFromTags(msg.Tags)
	if worker.ArchivedAt != nil {
		d.audit(ctx, t, msg, sourcePeerID(msg), depth, "denied", "worker_archived")
		return
	}
	if !worker.Enabled {
		d.audit(ctx, t, msg, sourcePeerID(msg), depth, "denied", "worker_disabled")
		return
	}
	if !workerCanReadWorkspace(worker, msg.WorkspaceID) {
		// Silent skip — this is the common case (one trigger per
		// workspace, message landed in a different workspace). Auditing
		// every miss would balloon the ledger. The cross-workspace
		// security concern is only the DROP — and that's what this is.
		return
	}
	if depth >= t.MaxChainDepth {
		d.audit(ctx, t, msg, "", depth, "loop_guard",
			"chain_depth_exceeded")
		return
	}
	srcPeer := sourcePeerID(msg)
	if srcPeer != "" && srcPeer != d.selfPeer {
		if !d.peerHasTriggerScope(ctx, srcPeer, worker.Name) {
			d.audit(ctx, t, msg, srcPeer, depth, "denied",
				"peer_missing_trigger_scope")
			return
		}
	}
	sourceKey := throttleKey(msg, srcPeer)
	if !d.tryReserveThrottle(t.ID, sourceKey, t.ThrottleSeconds) {
		d.audit(ctx, t, msg, srcPeer, depth, "throttled", "")
		return
	}
	d.audit(ctx, t, msg, srcPeer, depth, "fired", "")
	d.fireRunner(ctx, t, msg, srcPeer, depth, worker)
}

func workerCanReadWorkspace(worker *store.Worker, workspaceID string) bool {
	if worker == nil {
		return false
	}
	if worker.WorkspaceID == workspaceID {
		return true
	}
	for _, g := range worker.WorkspaceAccess {
		if g.WorkspaceID != workspaceID {
			continue
		}
		return g.CanRead()
	}
	return false
}

// fireRunner kicks the worker run on its own goroutine with an outer
// timeout context so a long-running model call doesn't keep the
// subscriber goroutine alive.
func (d *Dispatcher) fireRunner(
	ctx context.Context, t *store.WorkerMeshTrigger,
	msg *store.MeshMessage, srcPeer string, depth int, worker *store.Worker,
) {
	if d.runner == nil {
		return
	}
	go func() {
		runCtx, cancel := context.WithTimeout(context.Background(), runTimeoutFor(worker))
		defer cancel()
		_, err := d.runner.RunWithOpts(runCtx, t.WorkerID, runner.RunOpts{
			TriggerKind:       "mesh",
			TriggerMessageID:  msg.ID,
			TriggerSourcePeer: srcPeer,
			TriggerChainDepth: depth + 1,
		})
		if err != nil {
			slog.Warn("mesh trigger dispatch: runner returned error",
				"trigger_id", t.ID,
				"worker_id", t.WorkerID,
				"msg_id", msg.ID,
				"error", err,
			)
		}
		_ = ctx // ctx left unused intentionally — the outer ctx may be
		// the subscriber's ctx, which dies as soon as Send returns;
		// runs need their own lifetime.
	}()
}

// peerHasTriggerScope checks the wildcard scope first, then the worker-
// specific scope. Either grants permission.
func (d *Dispatcher) peerHasTriggerScope(
	ctx context.Context, peerID, workerName string,
) bool {
	if peerID == "" || peerID == d.selfPeer {
		return true
	}
	if ok, _ := d.store.HasPeerScope(ctx, peerID, triggerScopePrefix+"*"); ok {
		return true
	}
	if workerName == "" {
		return false
	}
	ok, _ := d.store.HasPeerScope(ctx, peerID, triggerScopePrefix+workerName)
	return ok
}

// audit emits one worker_trigger.mesh audit record. Best-effort.
func (d *Dispatcher) audit(
	ctx context.Context,
	t *store.WorkerMeshTrigger, msg *store.MeshMessage,
	srcPeer string, depth int, decision, reason string,
) {
	if d == nil || d.auditor == nil {
		return
	}
	rec := buildAuditRecord(t, msg, srcPeer, depth, decision, reason, d.clock.Now())
	_ = d.auditor.Record(ctx, rec)
}
