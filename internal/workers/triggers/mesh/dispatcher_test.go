package mesh_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/workers/runner"
	triggermesh "github.com/don-works/mcplexer/internal/workers/triggers/mesh"
)

// fakeStore implements TriggerStore for unit tests.
type fakeStore struct {
	mu       sync.Mutex
	triggers []*store.WorkerMeshTrigger
	workers  map[string]*store.Worker
	scopes   map[string]map[string]bool // peerID -> scope -> granted
	listErr  error
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		workers: map[string]*store.Worker{},
		scopes:  map[string]map[string]bool{},
	}
}

func (f *fakeStore) ListAllEnabledMeshTriggers(_ context.Context) ([]*store.WorkerMeshTrigger, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	out := make([]*store.WorkerMeshTrigger, 0, len(f.triggers))
	for _, t := range f.triggers {
		if t.Enabled {
			cp := *t
			out = append(out, &cp)
		}
	}
	return out, nil
}

func (f *fakeStore) GetWorker(_ context.Context, id string) (*store.Worker, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, ok := f.workers[id]
	if !ok {
		return nil, store.ErrWorkerNotFound
	}
	return w, nil
}

func (f *fakeStore) HasPeerScope(_ context.Context, peerID, scope string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if m, ok := f.scopes[peerID]; ok {
		return m[scope], nil
	}
	return false, nil
}

func (f *fakeStore) grant(peerID, scope string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scopes[peerID] == nil {
		f.scopes[peerID] = map[string]bool{}
	}
	f.scopes[peerID][scope] = true
}

func (f *fakeStore) addWorker(w *store.Worker) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workers[w.ID] = w
}

func (f *fakeStore) addTrigger(t *store.WorkerMeshTrigger) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.triggers = append(f.triggers, t)
}

// fakeRunner counts RunWithOpts invocations + captures the RunOpts it
// was called with.
type fakeRunner struct {
	mu    sync.Mutex
	calls []runner.RunOpts
	wg    sync.WaitGroup
}

func newFakeRunner() *fakeRunner { return &fakeRunner{} }

func (r *fakeRunner) expect(n int) { r.wg.Add(n) }

func (r *fakeRunner) RunWithOpts(_ context.Context, _ string, opts runner.RunOpts) (string, error) {
	r.mu.Lock()
	r.calls = append(r.calls, opts)
	r.mu.Unlock()
	r.wg.Done()
	return "fake-run-id", nil
}

func (r *fakeRunner) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

// fakeClock returns a controllable time.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeAuditor captures every audit row emitted by the dispatcher.
type fakeAuditor struct {
	mu      sync.Mutex
	records []*store.AuditRecord
}

func newFakeAuditor() *fakeAuditor { return &fakeAuditor{} }

func (a *fakeAuditor) Record(_ context.Context, rec *store.AuditRecord) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.records = append(a.records, rec)
	return nil
}

func (a *fakeAuditor) decisions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]string, 0, len(a.records))
	for _, r := range a.records {
		if r.ParamsRedacted == nil {
			continue
		}
		out = append(out, string(r.ParamsRedacted))
	}
	return out
}

func newDispatcher(t *testing.T, store *fakeStore, runner triggermesh.WorkerExecutor, clk *fakeClock, auditor *fakeAuditor, selfPeer string) *triggermesh.Dispatcher {
	t.Helper()
	deps := triggermesh.Deps{
		Store:    store,
		Runner:   runner,
		Clock:    clk,
		SelfPeer: selfPeer,
	}
	if auditor != nil {
		deps.Auditor = auditor
	}
	d := triggermesh.New(deps)
	if err := d.Reload(context.Background()); err != nil {
		t.Fatalf("reload: %v", err)
	}
	return d
}

// helper: wait until the runner has either received n calls or the
// timeout elapses. Returns the actual count.
func waitForCalls(r *fakeRunner, want int, timeout time.Duration) int {
	deadline := time.Now().Add(timeout)
	for r.callCount() < want && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	return r.callCount()
}

// TestDispatcherMatchDimensions exercises every match axis: kind, tags,
// audience, content regex, from filter. Confirms each filter is AND'd
// with the others.
func TestDispatcherMatchDimensions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		trigger   *store.WorkerMeshTrigger
		msg       *store.MeshMessage
		shouldHit bool
	}{
		{
			name:      "kind match",
			trigger:   &store.WorkerMeshTrigger{KindMatch: "alert", Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3},
			msg:       &store.MeshMessage{ID: "m1", Kind: "alert"},
			shouldHit: true,
		},
		{
			name:      "kind mismatch",
			trigger:   &store.WorkerMeshTrigger{KindMatch: "alert", Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3},
			msg:       &store.MeshMessage{ID: "m2", Kind: "event"},
			shouldHit: false,
		},
		{
			name:      "tag overlap",
			trigger:   &store.WorkerMeshTrigger{TagMatch: "alpha,beta", Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3},
			msg:       &store.MeshMessage{ID: "m3", Tags: "gamma,beta"},
			shouldHit: true,
		},
		{
			name:      "tag no overlap",
			trigger:   &store.WorkerMeshTrigger{TagMatch: "alpha", Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3},
			msg:       &store.MeshMessage{ID: "m4", Tags: "gamma"},
			shouldHit: false,
		},
		{
			name:      "audience equality",
			trigger:   &store.WorkerMeshTrigger{AudienceMatch: "security", Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3},
			msg:       &store.MeshMessage{ID: "m5", Audience: "security"},
			shouldHit: true,
		},
		{
			name:      "audience wildcard",
			trigger:   &store.WorkerMeshTrigger{AudienceMatch: "*", Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3},
			msg:       &store.MeshMessage{ID: "m6", Audience: "anything"},
			shouldHit: true,
		},
		{
			name:      "content regex hit",
			trigger:   &store.WorkerMeshTrigger{ContentRegex: "(?i)breach", Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3},
			msg:       &store.MeshMessage{ID: "m7", Content: "DETECTED a Breach in prod"},
			shouldHit: true,
		},
		{
			name:      "content regex miss",
			trigger:   &store.WorkerMeshTrigger{ContentRegex: "(?i)breach", Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3},
			msg:       &store.MeshMessage{ID: "m8", Content: "all fine"},
			shouldHit: false,
		},
		{
			name: "from agent name match",
			trigger: &store.WorkerMeshTrigger{
				FromFilters: []store.TriggerFromFilter{{AgentName: "audit-watcher"}},
				Enabled:     true, ThrottleSeconds: 1, MaxChainDepth: 3,
			},
			msg:       &store.MeshMessage{ID: "m9", AgentName: "audit-watcher"},
			shouldHit: true,
		},
		{
			name: "from agent name mismatch",
			trigger: &store.WorkerMeshTrigger{
				FromFilters: []store.TriggerFromFilter{{AgentName: "audit-watcher"}},
				Enabled:     true, ThrottleSeconds: 1, MaxChainDepth: 3,
			},
			msg:       &store.MeshMessage{ID: "m10", AgentName: "other-bot"},
			shouldHit: false,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := newFakeStore()
			tc.trigger.ID = "trig"
			tc.trigger.WorkerID = "w1"
			fs.addTrigger(tc.trigger)
			fs.addWorker(&store.Worker{ID: "w1", Name: "matcher", Enabled: true})
			fr := newFakeRunner()
			if tc.shouldHit {
				fr.expect(1)
			}
			clk := &fakeClock{now: time.Now()}
			d := newDispatcher(t, fs, fr, clk, nil, "")
			d.OnMessage(context.Background(), tc.msg)
			if tc.shouldHit {
				if got := waitForCalls(fr, 1, 200*time.Millisecond); got != 1 {
					t.Fatalf("expected 1 dispatch, got %d", got)
				}
			} else {
				time.Sleep(20 * time.Millisecond) // give the goroutine a chance to misfire
				if fr.callCount() != 0 {
					t.Fatalf("unexpected dispatch on no-match case: %d", fr.callCount())
				}
			}
		})
	}
}

// TestDispatcherWorkspaceIsolation — G2 regression. A worker in
// workspace alpha must NOT fire on a mesh_message that landed in a
// different workspace (e.g. global, beta), even when all the
// matchesTrigger filters say yes. This is the cross-tenant leak the
// audit caught.
func TestDispatcherWorkspaceIsolation(t *testing.T) {
	t.Parallel()
	fs := newFakeStore()
	fs.addTrigger(&store.WorkerMeshTrigger{
		ID: "trig", WorkerID: "w-alpha", KindMatch: "alert",
		Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3,
	})
	fs.addWorker(&store.Worker{ID: "w-alpha", Name: "alpha-watcher", WorkspaceID: "alpha", Enabled: true})
	clk := &fakeClock{now: time.Now()}
	fr := newFakeRunner()
	d := newDispatcher(t, fs, fr, clk, nil, "")

	// Message in "global" (today's peer-broadcast bucket) — must NOT fire
	// the alpha worker.
	d.OnMessage(context.Background(), &store.MeshMessage{
		ID: "m-global", Kind: "alert", WorkspaceID: "global",
	})
	// Message in "beta" — also a no-fire.
	d.OnMessage(context.Background(), &store.MeshMessage{
		ID: "m-beta", Kind: "alert", WorkspaceID: "beta",
	})
	time.Sleep(20 * time.Millisecond)
	if fr.callCount() != 0 {
		t.Fatalf("worker in alpha fired on cross-workspace messages: %d calls", fr.callCount())
	}

	// Same trigger, same content, but the message is in workspace alpha —
	// this is the happy path and must fire.
	fr.expect(1)
	d.OnMessage(context.Background(), &store.MeshMessage{
		ID: "m-alpha", Kind: "alert", WorkspaceID: "alpha",
	})
	if got := waitForCalls(fr, 1, 200*time.Millisecond); got != 1 {
		t.Fatalf("expected 1 dispatch on workspace-match, got %d", got)
	}
}

// TestDispatcherThrottle confirms a second match inside the throttle
// window is suppressed, and that advancing past the window admits again.
func TestDispatcherThrottle(t *testing.T) {
	t.Parallel()
	fs := newFakeStore()
	fs.addTrigger(&store.WorkerMeshTrigger{
		ID: "trig", WorkerID: "w1", KindMatch: "alert",
		Enabled: true, ThrottleSeconds: 5, MaxChainDepth: 3,
	})
	fs.addWorker(&store.Worker{ID: "w1", Name: "throttled", Enabled: true})
	clk := &fakeClock{now: time.Now()}
	fr := newFakeRunner()
	auditor := newFakeAuditor()
	d := newDispatcher(t, fs, fr, clk, auditor, "")

	fr.expect(1)
	msg := &store.MeshMessage{ID: "m1", Kind: "alert", AgentName: "watcher"}
	d.OnMessage(context.Background(), msg)
	waitForCalls(fr, 1, 200*time.Millisecond)

	// Second message inside the window: throttled, no runner call.
	msg2 := &store.MeshMessage{ID: "m2", Kind: "alert", AgentName: "watcher"}
	d.OnMessage(context.Background(), msg2)
	time.Sleep(20 * time.Millisecond)
	if fr.callCount() != 1 {
		t.Fatalf("throttle did not block second call: %d", fr.callCount())
	}

	// Advance past the window and confirm the third message is admitted.
	clk.advance(6 * time.Second)
	fr.expect(1)
	msg3 := &store.MeshMessage{ID: "m3", Kind: "alert", AgentName: "watcher"}
	d.OnMessage(context.Background(), msg3)
	waitForCalls(fr, 2, 200*time.Millisecond)
	if fr.callCount() != 2 {
		t.Fatalf("expected 2 dispatches after window advance, got %d", fr.callCount())
	}

	// Confirm at least one throttled audit row was emitted.
	if !containsParam(auditor.decisions(), `"decision":"throttled"`) {
		t.Fatalf("no throttled audit record: %v", auditor.decisions())
	}
}

// TestDispatcherLoopGuard confirms a message whose chain-depth tag has
// reached the trigger's MaxChainDepth is rejected with a loop_guard
// audit row.
func TestDispatcherLoopGuard(t *testing.T) {
	t.Parallel()
	fs := newFakeStore()
	fs.addTrigger(&store.WorkerMeshTrigger{
		ID: "trig", WorkerID: "w1", KindMatch: "finding",
		Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 2,
	})
	fs.addWorker(&store.Worker{ID: "w1", Name: "looper", Enabled: true})
	fr := newFakeRunner()
	auditor := newFakeAuditor()
	d := newDispatcher(t, fs, fr, &fakeClock{now: time.Now()}, auditor, "")

	// chain-depth:2 meets MaxChainDepth=2 — should be loop-guarded.
	msg := &store.MeshMessage{
		ID: "m-loop", Kind: "finding", Tags: "worker,output,chain-depth:2",
	}
	d.OnMessage(context.Background(), msg)
	time.Sleep(30 * time.Millisecond)
	if fr.callCount() != 0 {
		t.Fatalf("loop guard did not block dispatch: %d", fr.callCount())
	}
	if !containsParam(auditor.decisions(), `"decision":"loop_guard"`) {
		t.Fatalf("no loop_guard audit: %v", auditor.decisions())
	}

	// chain-depth:1 < MaxChainDepth=2 — should admit.
	fr.expect(1)
	msg2 := &store.MeshMessage{
		ID: "m-pass", Kind: "finding", Tags: "worker,output,chain-depth:1",
	}
	d.OnMessage(context.Background(), msg2)
	if got := waitForCalls(fr, 1, 200*time.Millisecond); got != 1 {
		t.Fatalf("expected 1 dispatch below MaxChainDepth, got %d", got)
	}
	// Confirm the depth was passed into the runner: N+1 = 2.
	gotDepth := fr.calls[0].TriggerChainDepth
	if gotDepth != 2 {
		t.Fatalf("expected TriggerChainDepth=2 in RunOpts, got %d", gotDepth)
	}
}

// TestDispatcherCrossPeerPermissionDenied verifies a message arriving
// from a peer without the trigger_worker scope is rejected with a
// denied audit row.
func TestDispatcherCrossPeerPermissionDenied(t *testing.T) {
	t.Parallel()
	fs := newFakeStore()
	fs.addTrigger(&store.WorkerMeshTrigger{
		ID: "trig", WorkerID: "w1", KindMatch: "task",
		Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3,
	})
	fs.addWorker(&store.Worker{ID: "w1", Name: "remote-target", Enabled: true})
	fr := newFakeRunner()
	auditor := newFakeAuditor()
	d := newDispatcher(t, fs, fr, &fakeClock{now: time.Now()}, auditor, "self-peer")

	// Inbound P2P-tagged message — sourcePeerID extracted from tags.
	msg := &store.MeshMessage{
		ID: "m-remote", Kind: "task", Tags: "p2p,from:12D3RemotePeer",
	}
	d.OnMessage(context.Background(), msg)
	time.Sleep(30 * time.Millisecond)
	if fr.callCount() != 0 {
		t.Fatalf("permission gate failed open: %d", fr.callCount())
	}
	if !containsParam(auditor.decisions(), `"decision":"denied"`) {
		t.Fatalf("no denied audit record: %v", auditor.decisions())
	}
}

// TestDispatcherCrossPeerPermissionGranted verifies the same trigger
// fires when the peer holds either the wildcard or the worker-specific
// scope.
func TestDispatcherCrossPeerPermissionGranted(t *testing.T) {
	t.Parallel()
	t.Run("wildcard scope", func(t *testing.T) {
		t.Parallel()
		fs := newFakeStore()
		fs.addTrigger(&store.WorkerMeshTrigger{
			ID: "trig", WorkerID: "w1", KindMatch: "task",
			Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3,
		})
		fs.addWorker(&store.Worker{ID: "w1", Name: "remote-target", Enabled: true})
		fs.grant("12D3RemotePeer", "trigger_worker:*")
		fr := newFakeRunner()
		fr.expect(1)
		d := newDispatcher(t, fs, fr, &fakeClock{now: time.Now()}, nil, "self-peer")
		d.OnMessage(context.Background(), &store.MeshMessage{
			ID: "m-remote", Kind: "task", Tags: "p2p,from:12D3RemotePeer",
		})
		waitForCalls(fr, 1, 200*time.Millisecond)
		if fr.callCount() != 1 {
			t.Fatalf("wildcard scope did not admit dispatch: %d", fr.callCount())
		}
		if fr.calls[0].TriggerSourcePeer != "12D3RemotePeer" {
			t.Fatalf("source peer not passed to runner: %s", fr.calls[0].TriggerSourcePeer)
		}
		if fr.calls[0].TriggerKind != "mesh" {
			t.Fatalf("trigger kind wrong: %s", fr.calls[0].TriggerKind)
		}
	})
	t.Run("worker-specific scope", func(t *testing.T) {
		t.Parallel()
		fs := newFakeStore()
		fs.addTrigger(&store.WorkerMeshTrigger{
			ID: "trig", WorkerID: "w1", KindMatch: "task",
			Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3,
		})
		fs.addWorker(&store.Worker{ID: "w1", Name: "remote-target", Enabled: true})
		fs.grant("12D3RemotePeer", "trigger_worker:remote-target")
		fr := newFakeRunner()
		fr.expect(1)
		d := newDispatcher(t, fs, fr, &fakeClock{now: time.Now()}, nil, "self-peer")
		d.OnMessage(context.Background(), &store.MeshMessage{
			ID: "m-remote2", Kind: "task", Tags: "p2p,from:12D3RemotePeer",
		})
		waitForCalls(fr, 1, 200*time.Millisecond)
		if fr.callCount() != 1 {
			t.Fatalf("worker-specific scope did not admit: %d", fr.callCount())
		}
	})
}

// TestDispatcherLocalBypass verifies local-origin messages skip the
// permission check (no from: tag = local).
func TestDispatcherLocalBypass(t *testing.T) {
	t.Parallel()
	fs := newFakeStore()
	fs.addTrigger(&store.WorkerMeshTrigger{
		ID: "trig", WorkerID: "w1", KindMatch: "event",
		Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3,
	})
	fs.addWorker(&store.Worker{ID: "w1", Name: "local-target", Enabled: true})
	fr := newFakeRunner()
	fr.expect(1)
	d := newDispatcher(t, fs, fr, &fakeClock{now: time.Now()}, nil, "self-peer")
	// No "from:" tag — this is a local Send.
	d.OnMessage(context.Background(), &store.MeshMessage{
		ID: "m-local", Kind: "event", Tags: "worker,output",
	})
	waitForCalls(fr, 1, 200*time.Millisecond)
	if fr.callCount() != 1 {
		t.Fatalf("local message blocked despite no peer source: %d", fr.callCount())
	}
	if fr.calls[0].TriggerSourcePeer != "" {
		t.Fatalf("local message stamped a peer: %q", fr.calls[0].TriggerSourcePeer)
	}
}

// TestDispatcherDisabledTriggerIgnored confirms enabled=false rows are
// excluded from the cache (the store-level filter already does this,
// but the dispatcher honours an in-memory mutation defensively).
func TestDispatcherDisabledTriggerIgnored(t *testing.T) {
	t.Parallel()
	fs := newFakeStore()
	fs.addTrigger(&store.WorkerMeshTrigger{
		ID: "trig-off", WorkerID: "w1", KindMatch: "event",
		Enabled: false, ThrottleSeconds: 1, MaxChainDepth: 3,
	})
	fs.addWorker(&store.Worker{ID: "w1", Name: "disabled", Enabled: true})
	fr := newFakeRunner()
	d := newDispatcher(t, fs, fr, &fakeClock{now: time.Now()}, nil, "")
	d.OnMessage(context.Background(), &store.MeshMessage{ID: "x", Kind: "event"})
	time.Sleep(30 * time.Millisecond)
	if fr.callCount() != 0 {
		t.Fatalf("disabled trigger fired: %d", fr.callCount())
	}
}

func TestDispatcherDisabledWorkerIgnored(t *testing.T) {
	t.Parallel()
	fs := newFakeStore()
	fs.addTrigger(&store.WorkerMeshTrigger{
		ID: "trig-on", WorkerID: "w1", KindMatch: "event",
		Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3,
	})
	fs.addWorker(&store.Worker{ID: "w1", Name: "paused-worker", Enabled: false})
	fr := newFakeRunner()
	auditor := newFakeAuditor()
	d := newDispatcher(t, fs, fr, &fakeClock{now: time.Now()}, auditor, "")

	d.OnMessage(context.Background(), &store.MeshMessage{ID: "x", Kind: "event"})
	time.Sleep(30 * time.Millisecond)
	if fr.callCount() != 0 {
		t.Fatalf("disabled worker fired: %d", fr.callCount())
	}
	if !containsParam(auditor.decisions(), `"reason":"worker_disabled"`) {
		t.Fatalf("no worker_disabled audit record: %v", auditor.decisions())
	}
}

// TestDispatcherDoesNotBlockSend verifies OnMessage returns quickly even
// when the runner is slow — the dispatcher dispatches in a goroutine.
func TestDispatcherDoesNotBlockSend(t *testing.T) {
	t.Parallel()
	fs := newFakeStore()
	fs.addTrigger(&store.WorkerMeshTrigger{
		ID: "trig", WorkerID: "w1", KindMatch: "event",
		Enabled: true, ThrottleSeconds: 1, MaxChainDepth: 3,
	})
	fs.addWorker(&store.Worker{ID: "w1", Name: "slow", Enabled: true})
	slow := &slowRunner{delay: 50 * time.Millisecond}
	d := newDispatcher(t, fs, slow, &fakeClock{now: time.Now()}, nil, "")

	start := time.Now()
	d.OnMessage(context.Background(), &store.MeshMessage{ID: "m", Kind: "event"})
	elapsed := time.Since(start)
	if elapsed > 30*time.Millisecond {
		t.Fatalf("OnMessage blocked for %v (runner delay is 50ms); should return immediately", elapsed)
	}
	// Allow the goroutine to finish so we don't leak it across tests.
	time.Sleep(80 * time.Millisecond)
	if atomic.LoadInt32(&slow.calls) != 1 {
		t.Fatalf("expected 1 slow call, got %d", atomic.LoadInt32(&slow.calls))
	}
}

// slowRunner sleeps inside RunWithOpts so the test can confirm
// OnMessage doesn't block on dispatch.
type slowRunner struct {
	delay time.Duration
	calls int32
}

func (s *slowRunner) RunWithOpts(_ context.Context, _ string, _ runner.RunOpts) (string, error) {
	time.Sleep(s.delay)
	atomic.AddInt32(&s.calls, 1)
	return "id", nil
}

// containsParam returns true when any decision payload contains the
// substring needle. Cheap matcher used in the throttle/loop-guard tests.
func containsParam(decisions []string, needle string) bool {
	for _, d := range decisions {
		if stringsContains(d, needle) {
			return true
		}
	}
	return false
}

func stringsContains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	if len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
