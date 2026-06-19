// manager_test.go exercises the Manager without ever spawning the
// real opencode binary. A fakeRunner stands in for the exec layer so
// tests stay hermetic and fast even on machines without opencode
// installed.
//
// The "live" integration path is intentionally guarded by an
// exec.LookPath check — if a real opencode is present we run a smoke
// test against it; otherwise the test self-skips.
package opencode

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// fakeRunner is the test seam — captures the args it was called with
// and returns canned stdout / handles. Safe under concurrent use so
// tests can drive Start/Stop from multiple goroutines without races.
type fakeRunner struct {
	mu sync.Mutex

	lookPathResult string
	lookPathErr    error

	outputs    map[string][]byte // cmdline "name arg arg" -> stdout
	outputErrs map[string]error

	outputCalls atomic.Int32

	startHandles []*fakeHandle
	startErr     error
}

func (f *fakeRunner) LookPath(name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lookPathResult, f.lookPathErr
}

// ProbeKnownPaths in the fake always returns "" so tests stay
// deterministic regardless of the host's real install state.
func (f *fakeRunner) ProbeKnownPaths() string { return "" }

func (f *fakeRunner) Output(_ context.Context, name string, args ...string) ([]byte, error) {
	f.outputCalls.Add(1)
	key := name
	if len(args) > 0 {
		key = name + " " + strings.Join(args, " ")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.outputErrs[key]; ok {
		return nil, err
	}
	if out, ok := f.outputs[key]; ok {
		return out, nil
	}
	// Fall through: match by leading command (e.g. "models").
	for k, v := range f.outputs {
		if strings.HasPrefix(key, k) {
			return v, nil
		}
	}
	return nil, fmt.Errorf("fakeRunner: no canned Output for %q", key)
}

func (f *fakeRunner) Start(_ context.Context, _ string, _ ...string) (processHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return nil, f.startErr
	}
	h := &fakeHandle{exitCh: make(chan error, 1)}
	f.startHandles = append(f.startHandles, h)
	return h, nil
}

// fakeHandle simulates a long-running process. Tests call exit() to
// release Wait().
type fakeHandle struct {
	mu       sync.Mutex
	exitCh   chan error
	signaled atomic.Int32
	killed   atomic.Bool
	waited   atomic.Bool
}

func (h *fakeHandle) Signal(_ syscall.Signal) error {
	h.signaled.Add(1)
	// Auto-exit on SIGTERM so Stop() completes without an explicit
	// exit() call from the test.
	h.exit(nil)
	return nil
}

func (h *fakeHandle) Kill() error {
	h.killed.Store(true)
	h.exit(errors.New("killed"))
	return nil
}

func (h *fakeHandle) Wait() error {
	h.waited.Store(true)
	return <-h.exitCh
}

// exit pushes an exit value onto the wait channel exactly once.
func (h *fakeHandle) exit(err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case h.exitCh <- err:
	default:
	}
}

// fakeHTTP returns 200 on the Nth call (1-indexed). Before that, it
// returns a connection-refused-ish error to simulate the port not
// being bound yet.
type fakeHTTP struct {
	readyAfter int32
	calls      atomic.Int32
}

func (f *fakeHTTP) Get(_ string) (*http.Response, error) {
	n := f.calls.Add(1)
	if n < f.readyAfter {
		return nil, errors.New("connection refused")
	}
	return &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"data":[]}`)),
	}, nil
}

// newTestManager wires a Manager with a fakeRunner + fakeHTTP so tests
// run hermetically. ready=N means the HTTP probe returns 200 on the
// Nth call.
func newTestManager(t *testing.T, runner *fakeRunner, ready int32) *Manager {
	t.Helper()
	m := NewManager(Options{Port: 14096})
	m.runner = runner
	m.http = &fakeHTTP{readyAfter: ready}
	return m
}

func TestStatusBinaryMissing(t *testing.T) {
	r := &fakeRunner{lookPathErr: exec.ErrNotFound}
	m := newTestManager(t, r, 1)

	s := m.Status()
	if s.Installed {
		t.Fatalf("expected Installed=false, got %+v", s)
	}
	if s.Running {
		t.Fatalf("expected Running=false on cold manager")
	}
}

func TestStatusBinaryFound(t *testing.T) {
	r := &fakeRunner{lookPathResult: "/usr/local/bin/opencode"}
	m := newTestManager(t, r, 1)
	s := m.Status()
	if !s.Installed {
		t.Fatalf("expected Installed=true, got %+v", s)
	}
	if s.BinaryPath != "/usr/local/bin/opencode" {
		t.Fatalf("expected BinaryPath set, got %q", s.BinaryPath)
	}
}

func TestStartNotInstalledReturnsSentinel(t *testing.T) {
	r := &fakeRunner{lookPathErr: exec.ErrNotFound}
	m := newTestManager(t, r, 1)
	err := m.Start(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("expected ErrNotInstalled, got %v", err)
	}
}

func TestStartStopLifecycle(t *testing.T) {
	r := &fakeRunner{
		lookPathResult: "/fake/opencode",
		outputs: map[string][]byte{
			"/fake/opencode --version": []byte("opencode 0.99.0\n"),
		},
	}
	m := newTestManager(t, r, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	s := m.Status()
	if !s.Running {
		t.Fatalf("expected Running=true after Start, got %+v", s)
	}
	if s.Version != "opencode 0.99.0" {
		t.Fatalf("expected version captured, got %q", s.Version)
	}

	// Second Start is idempotent.
	if err := m.Start(ctx); err != nil {
		t.Fatalf("idempotent Start: %v", err)
	}

	if err := m.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if got := m.Status(); got.Running {
		t.Fatalf("expected Running=false after Stop, got %+v", got)
	}
	// Stop again — idempotent.
	if err := m.Stop(); err != nil {
		t.Fatalf("second Stop: %v", err)
	}
}

func TestListModelsCachesResult(t *testing.T) {
	r := &fakeRunner{
		lookPathResult: "/fake/opencode",
		outputs: map[string][]byte{
			"/fake/opencode models": []byte("anthropic/claude-opus-4-7\n\nopenai/gpt-4o\nmistral/large\n"),
		},
	}
	m := newTestManager(t, r, 1)

	got, err := m.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"anthropic/claude-opus-4-7", "openai/gpt-4o", "mistral/large"}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("model[%d]: got %q, want %q", i, got[i], want[i])
		}
	}
	// Mutate the returned slice — must not corrupt the cache.
	got[0] = "TAMPERED"

	// Second call hits the cache: outputCalls stays at 1.
	if _, err := m.ListModels(context.Background()); err != nil {
		t.Fatalf("cached ListModels: %v", err)
	}
	if n := r.outputCalls.Load(); n != 1 {
		t.Fatalf("expected 1 Output call (cache hit on 2nd), got %d", n)
	}
	// Verify cache wasn't mutated by the earlier tamper.
	cached, _ := m.ListModels(context.Background())
	if cached[0] != "anthropic/claude-opus-4-7" {
		t.Fatalf("cache mutated by caller: got %q", cached[0])
	}
}

func TestListModelsRefreshesAfterTTL(t *testing.T) {
	r := &fakeRunner{
		lookPathResult: "/fake/opencode",
		outputs: map[string][]byte{
			"/fake/opencode models": []byte("a\nb\n"),
		},
	}
	m := newTestManager(t, r, 1)

	if _, err := m.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels first: %v", err)
	}
	// Backdate the cache so the next call must refresh.
	m.mu.Lock()
	m.cacheAt = time.Now().Add(-10 * time.Minute)
	m.mu.Unlock()

	if _, err := m.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels refresh: %v", err)
	}
	if n := r.outputCalls.Load(); n != 2 {
		t.Fatalf("expected 2 Output calls after TTL expiry, got %d", n)
	}
}

func TestListModelsBinaryMissing(t *testing.T) {
	r := &fakeRunner{lookPathErr: exec.ErrNotFound}
	m := newTestManager(t, r, 1)
	_, err := m.ListModels(context.Background())
	if !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("expected ErrNotInstalled, got %v", err)
	}
}

func TestRefreshModelsBypassesWarmCache(t *testing.T) {
	r := &fakeRunner{
		lookPathResult: "/fake/opencode",
		outputs: map[string][]byte{
			"/fake/opencode models": []byte("a\nb\n"),
		},
	}
	m := newTestManager(t, r, 1)

	if _, err := m.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels warm-up: %v", err)
	}
	// Cache is warm (well inside TTL): RefreshModels must still run.
	if _, err := m.RefreshModels(context.Background()); err != nil {
		t.Fatalf("RefreshModels: %v", err)
	}
	if n := r.outputCalls.Load(); n != 2 {
		t.Fatalf("expected 2 Output calls (refresh busts cache), got %d", n)
	}
	// The forced run repopulates the cache: a plain ListModels right
	// after is a cache hit again.
	if _, err := m.ListModels(context.Background()); err != nil {
		t.Fatalf("ListModels after refresh: %v", err)
	}
	if n := r.outputCalls.Load(); n != 2 {
		t.Fatalf("expected refresh to repopulate cache, got %d Output calls", n)
	}
}

func TestConcurrentStartStop(t *testing.T) {
	r := &fakeRunner{
		lookPathResult: "/fake/opencode",
		outputs: map[string][]byte{
			"/fake/opencode --version": []byte("opencode 0.99.0\n"),
		},
	}
	m := newTestManager(t, r, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := m.Start(ctx); err != nil {
		t.Fatalf("initial Start: %v", err)
	}

	// Hammer Start + Stop from N goroutines and assert no panic / no
	// race. The -race detector catches the rest. With 50 goroutines
	// some Start calls will run while Stop is racing them; both must
	// be safe.
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); _ = m.Start(ctx) }()
		go func() { defer wg.Done(); _ = m.Stop() }()
	}
	wg.Wait()

	// Final stop — manager should land in a consistent state. There's
	// an inherent race where a Start that won the storm may still be
	// settling when Stop runs; we retry briefly until the supervisor
	// drains. -race is the primary safety contract here; the visible
	// state-converges-on-Stop check is best-effort.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		_ = m.Stop()
		if !m.Status().Running {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("expected Running=false after concurrent storm, got %+v", m.Status())
}

func TestEndpointUsesConfiguredPort(t *testing.T) {
	m := NewManager(Options{Port: 8123})
	if got, want := m.Endpoint(), "http://127.0.0.1:8123"; got != want {
		t.Fatalf("Endpoint: got %q, want %q", got, want)
	}
}

func TestEndpointDefaultsTo4096(t *testing.T) {
	m := NewManager(Options{})
	if got, want := m.Endpoint(), "http://127.0.0.1:4096"; got != want {
		t.Fatalf("Endpoint default: got %q, want %q", got, want)
	}
}

func TestParseModelLines(t *testing.T) {
	in := []byte("foo\n\n  bar\n\t\nbaz/qux\n")
	got := parseModelLines(in)
	want := []string{"foo", "bar", "baz/qux"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q want %q", i, got[i], want[i])
		}
	}
}

// TestRealBinarySmoke runs a tiny end-to-end check when an actual
// opencode binary is on PATH. Self-skips otherwise so the suite is
// hermetic by default.
func TestRealBinarySmoke(t *testing.T) {
	path, err := exec.LookPath("opencode")
	if err != nil {
		t.Skip("opencode not installed")
	}
	t.Logf("opencode found at %s", path)
	// We DON'T spawn `opencode serve` in unit tests — it would bind a
	// real port and could conflict with the user's running daemon.
	// Smoke-test the runner.Output path only.
	r := execCommandRunner{}
	out, err := r.Output(context.Background(), path, "--version")
	if err != nil {
		t.Fatalf("--version: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("expected non-empty --version output")
	}
}
