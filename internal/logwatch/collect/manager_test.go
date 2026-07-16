package collect

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/sshx"
	"github.com/don-works/mcplexer/internal/store"
)

// concurrencyStore is a Store fake keyed by source ID — unlike
// fakeStore's single shared fields, it must tolerate the concurrent
// callers tick() now makes across different sources.
type concurrencyStore struct {
	host  *store.RemoteHost
	scope *store.AuthScope

	mu       sync.Mutex
	sources  []*store.LogSource
	failures map[string]int
}

func newConcurrencyStore(host *store.RemoteHost, scope *store.AuthScope) *concurrencyStore {
	return &concurrencyStore{host: host, scope: scope, failures: map[string]int{}}
}

func (s *concurrencyStore) ListEnabledLogSources(context.Context) ([]*store.LogSource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*store.LogSource(nil), s.sources...), nil
}
func (s *concurrencyStore) GetRemoteHost(context.Context, string) (*store.RemoteHost, error) {
	return s.host, nil
}
func (s *concurrencyStore) GetAuthScope(context.Context, string) (*store.AuthScope, error) {
	return s.scope, nil
}
func (s *concurrencyStore) UpdateLogSourceCursor(context.Context, string, time.Time, string) error {
	return nil
}
func (s *concurrencyStore) SetLogSourceFailures(_ context.Context, id string, n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[id] = n
	return nil
}
func (s *concurrencyStore) SetRemoteHostPin(context.Context, string, string) error { return nil }

func (s *concurrencyStore) failureCount(id string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failures[id]
}

// concurrencyRunner tracks in-flight/max-concurrent Pull calls and
// can fail specific source IDs on demand.
type concurrencyRunner struct {
	delay   time.Duration
	failIDs map[string]bool

	inFlight int32
	maxSeen  int32
	calls    int32
}

func (r *concurrencyRunner) Pull(ctx context.Context, _ *store.RemoteHost, _ sshx.Credential, src *store.LogSource, _ time.Time) (PullResult, error) {
	atomic.AddInt32(&r.calls, 1)
	n := atomic.AddInt32(&r.inFlight, 1)
	defer atomic.AddInt32(&r.inFlight, -1)
	for {
		max := atomic.LoadInt32(&r.maxSeen)
		if n <= max || atomic.CompareAndSwapInt32(&r.maxSeen, max, n) {
			break
		}
	}
	select {
	case <-time.After(r.delay):
	case <-ctx.Done():
		return PullResult{}, ctx.Err()
	}
	if r.failIDs[src.ID] {
		return PullResult{}, errors.New("simulated pull failure")
	}
	return PullResult{Result: sshx.Result{Stdout: []byte("2026-07-08T14:00:00Z hello from " + src.ID + "\n")}}, nil
}

// syncSink is a thread-safe Sink fake — tick() pulls sources
// concurrently, so Ingest must tolerate concurrent callers.
type syncSink struct {
	mu        sync.Mutex
	lines     []Line
	dark      []darkAlert
	healthErr error
}

type darkAlert struct {
	sourceID  string
	episodeID string
	failures  int
	reason    FailureReason
}

func (s *syncSink) Ingest(_ context.Context, _ *store.LogSource, _ *store.RemoteHost, lines []Line) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, lines...)
	return nil
}

func (s *syncSink) lineCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.lines)
}

func (s *syncSink) NotifyCollectionFailure(
	_ context.Context, source *store.LogSource, _ *store.RemoteHost,
	failures int, episodeID string, reason FailureReason,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dark = append(s.dark, darkAlert{
		sourceID: source.ID, episodeID: episodeID, failures: failures, reason: reason,
	})
	return s.healthErr
}

func (s *syncSink) darkAlerts() []darkAlert {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]darkAlert(nil), s.dark...)
}

func manySources(n int) []*store.LogSource {
	out := make([]*store.LogSource, n)
	for i := range out {
		out[i] = &store.LogSource{
			ID: fmt.Sprintf("s%d", i), WorkspaceID: "ws", RemoteHostID: "h1",
			Name: fmt.Sprintf("svc-%d", i), Kind: store.LogSourceKindDocker, Selector: "api",
			ScheduleSpec: "2m", MaxPullBytes: 1 << 20, Enabled: true,
		}
	}
	return out
}

func testHostAndScope() (*store.RemoteHost, *store.AuthScope) {
	return &store.RemoteHost{ID: "h1", Name: "prod-1", SSHUser: "logwatch", SSHHost: "10.0.0.1", SSHPort: 22, AuthScopeID: "sc1", Enabled: true},
		&store.AuthScope{ID: "sc1", Type: sshx.AuthScopeTypeSSHKey}
}

// TestTick_BoundedConcurrency proves due sources are pulled in
// parallel (not one at a time) but never more than tickConcurrency at
// once, and tick() does not return until the whole batch is done.
func TestTick_BoundedConcurrency(t *testing.T) {
	host, scope := testHostAndScope()
	st := newConcurrencyStore(host, scope)
	st.sources = manySources(10)

	runner := &concurrencyRunner{delay: 30 * time.Millisecond}
	sink := &syncSink{}
	m := NewManager(st, fakeSecrets{}, sink, runner)

	m.tick(context.Background())

	if runner.calls != 10 {
		t.Fatalf("expected 10 pull calls, got %d", runner.calls)
	}
	if runner.maxSeen <= 1 {
		t.Fatalf("expected parallel pulls, max concurrent was %d", runner.maxSeen)
	}
	if runner.maxSeen > tickConcurrency {
		t.Fatalf("concurrency bound violated: max concurrent %d > %d", runner.maxSeen, tickConcurrency)
	}
	if got := sink.lineCount(); got != 10 {
		t.Fatalf("tick returned before the whole batch finished: got %d lines, want 10", got)
	}
}

// TestTick_FailureCountersAccurate proves per-source failure
// accounting stays correct when sources succeed and fail within the
// same concurrent batch.
func TestTick_FailureCountersAccurate(t *testing.T) {
	host, scope := testHostAndScope()
	st := newConcurrencyStore(host, scope)
	sources := manySources(8)
	sources[1].ConsecutiveFailures = 2
	st.sources = sources

	runner := &concurrencyRunner{delay: 10 * time.Millisecond, failIDs: map[string]bool{"s1": true, "s5": true}}
	m := NewManager(st, fakeSecrets{}, &syncSink{}, runner)

	m.tick(context.Background())

	if got := st.failureCount("s1"); got != 3 {
		t.Fatalf("s1 failures: got %d, want 3", got)
	}
	if got := st.failureCount("s5"); got != 1 {
		t.Fatalf("s5 failures: got %d, want 1", got)
	}
	for _, id := range []string{"s0", "s2", "s3", "s4", "s6", "s7"} {
		if got := st.failureCount(id); got != 0 {
			t.Fatalf("%s failures: got %d, want 0 (must not have failed)", id, got)
		}
	}
}

// TestTick_ContextCancellation proves tick() unwinds promptly when
// the context is cancelled mid-batch instead of waiting out every
// remaining source.
func TestTick_ContextCancellation(t *testing.T) {
	host, scope := testHostAndScope()
	st := newConcurrencyStore(host, scope)
	st.sources = manySources(12)

	runner := &concurrencyRunner{delay: 2 * time.Second} // still running when we cancel
	m := NewManager(st, fakeSecrets{}, &syncSink{}, runner)

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	done := make(chan struct{})
	go func() {
		m.tick(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("tick() did not return promptly after context cancellation")
	}
	for _, source := range st.sources {
		if got := st.failureCount(source.ID); got != 0 {
			t.Fatalf("shutdown cancellation counted as source failure for %s: %d", source.ID, got)
		}
	}
}
