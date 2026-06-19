// Package opencode supervises a local `opencode serve` subprocess so
// workers (and any other in-process consumer) can attach to the OpenCode
// HTTP server on http://127.0.0.1:<port> without the user starting it
// manually.
//
// Layered design:
//   - Manager owns the binary lookup, port, lifetime, and a 5min model
//     cache. Start/Stop are idempotent and concurrency-safe; the
//     supervisor goroutine restarts the subprocess on unexpected exit
//     with capped exponential backoff, resetting after 5min of stable
//     uptime.
//   - The model list is fetched via `opencode models` (one model per
//     non-blank line) and cached in memory; the cache is invalidated on
//     Stop so a restart re-discovers what auth changes brought in.
//   - All exec calls go through a small commandRunner seam (runner.go)
//     so tests never have to spawn the real binary.
package opencode

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ErrNotInstalled is returned when no `opencode` binary can be located
// on PATH and no explicit BinaryPath was provided in Options.
var ErrNotInstalled = errors.New("opencode binary not found")

// defaultPort matches the port that `opencode serve` would otherwise
// pick when --port 0 is given, but we pin it so workers can be
// configured against a stable endpoint.
const defaultPort = 4096

// modelCacheTTL caps how long ListModels reuses a previous result. The
// list is cheap to regenerate (a single CLI invocation) so we keep
// the TTL short enough that newly authenticated providers show up in
// the worker UI without a daemon restart.
const modelCacheTTL = 5 * time.Minute

// readinessTimeout bounds how long Start waits for `opencode serve` to
// start answering HTTP requests on /global/health before giving up.
const readinessTimeout = 30 * time.Second

// stopTimeout bounds how long Stop waits for graceful SIGTERM exit
// before escalating to SIGKILL.
const stopTimeout = 5 * time.Second

// Status snapshots the current state of the manager. Returned as JSON
// from the HTTP API so the dashboard can render binary detection +
// runtime state without polling internal state.
type Status struct {
	Installed  bool      `json:"installed"`
	Running    bool      `json:"running"`
	Port       int       `json:"port,omitempty"`
	BinaryPath string    `json:"binary_path,omitempty"`
	Version    string    `json:"version,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

// Options configures a Manager. Zero-valued fields fall back to sane
// defaults: PATH lookup, port 4096, no auto-start.
type Options struct {
	BinaryPath string // empty → look up on PATH
	Port       int    // 0 → 4096 default
	AutoStart  bool   // currently informational; daemon wires this into Start
}

// httpClient is the seam for readiness probing. Tests inject a fake
// that flips to 200 after N polls.
type httpClient interface {
	Get(url string) (*http.Response, error)
}

// Manager owns the opencode subprocess lifecycle, the resolved binary
// path, and the model-list cache. All public methods are safe under
// concurrent use.
type Manager struct {
	opts Options

	runner commandRunner
	http   httpClient

	mu         sync.Mutex
	binaryPath string
	port       int
	version    string

	cmd       processHandle
	cancel    context.CancelFunc
	startedAt time.Time
	lastError string

	// supervisorDone closes when the supervisor goroutine exits.
	// nil before Start; reset on each successful Start.
	supervisorDone chan struct{}

	// stopping flips to true while Stop is in flight so the supervisor
	// distinguishes operator-initiated exit from a crash.
	stopping bool

	// model cache
	cacheModels []string
	cacheAt     time.Time
}

// NewManager constructs a Manager but does NOT start the subprocess.
// Callers must call Start (typically gated by an AutoStart flag in the
// daemon's config). The returned Manager is safe to call Status/Stop
// on even when Start has never been invoked.
func NewManager(opts Options) *Manager {
	if opts.Port == 0 {
		opts.Port = defaultPort
	}
	return &Manager{
		opts:   opts,
		runner: execCommandRunner{},
		http:   &http.Client{Timeout: 2 * time.Second},
	}
}

// Endpoint returns the base URL of the managed opencode server. Always
// loopback; the port is whatever Options.Port resolved to. The URL is
// valid even before Start so opencode_cli workers can use it with
// `opencode run --attach`.
func (m *Manager) Endpoint() string {
	m.mu.Lock()
	port := m.port
	m.mu.Unlock()
	if port == 0 {
		port = m.opts.Port
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// Status returns a snapshot of the manager's current state. Safe to
// call from any goroutine including before Start.
func (m *Manager) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	binary := m.binaryPath
	if binary == "" {
		// Probe PATH so the UI can show install-state without the
		// daemon having to call Start first.
		if resolved, err := m.resolveBinaryLocked(); err == nil {
			binary = resolved
		}
	}
	s := Status{
		Installed:  binary != "",
		Running:    m.cmd != nil,
		BinaryPath: binary,
		Version:    m.version,
		StartedAt:  m.startedAt,
		LastError:  m.lastError,
	}
	if s.Running {
		s.Port = m.port
	}
	return s
}

// resolveBinaryLocked returns the absolute path of the opencode binary,
// preferring Options.BinaryPath when set. Caller must hold m.mu.
//
// macOS launchd hands the daemon a stripped-down PATH that excludes the
// user's per-account install locations (`~/.opencode/bin`, `~/.local/bin`,
// often `/opt/homebrew/bin` too). After PATH lookup fails we probe a
// short list of well-known install roots so the typical user doesn't
// have to edit a plist before the runtime works.
func (m *Manager) resolveBinaryLocked() (string, error) {
	if m.opts.BinaryPath != "" {
		return m.opts.BinaryPath, nil
	}
	if path, err := m.runner.LookPath("opencode"); err == nil {
		return path, nil
	}
	if path := m.runner.ProbeKnownPaths(); path != "" {
		return path, nil
	}
	return "", ErrNotInstalled
}

// Start launches `opencode serve` and waits for it to accept HTTP
// requests on /global/health. It is idempotent: a second Start while
// already running is a no-op. The supervisor goroutine restarts the
// subprocess on unexpected exit until Stop is called.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.cmd != nil {
		m.mu.Unlock()
		return nil
	}
	binary, err := m.resolveBinaryLocked()
	if err != nil {
		m.lastError = err.Error()
		m.mu.Unlock()
		return err
	}
	m.binaryPath = binary
	m.port = m.opts.Port
	m.stopping = false
	m.lastError = ""
	m.supervisorDone = make(chan struct{})
	m.mu.Unlock()

	// Fetch version best-effort; the call is short-lived and a failure
	// shouldn't block startup.
	if out, err := m.runner.Output(ctx, binary, "--version"); err == nil {
		m.mu.Lock()
		m.version = strings.TrimSpace(string(out))
		m.mu.Unlock()
	}

	if err := m.spawnAndWait(ctx); err != nil {
		m.mu.Lock()
		m.lastError = err.Error()
		// supervisor goroutine never started, so don't close its chan
		// from anywhere else.
		if m.supervisorDone != nil {
			close(m.supervisorDone)
			m.supervisorDone = nil
		}
		m.mu.Unlock()
		return err
	}

	go m.supervise(ctx)
	return nil
}

// spawnAndWait spawns the subprocess, records the handle, and polls
// /global/health until it returns OK or readinessTimeout elapses.
func (m *Manager) spawnAndWait(ctx context.Context) error {
	m.mu.Lock()
	binary := m.binaryPath
	port := m.port
	m.mu.Unlock()

	// Spawn the subprocess with a context independent of the caller's.
	// If we derived from `ctx` (typically an HTTP request context),
	// completion of the Start HTTP handler would cancel childCtx, which
	// exec.CommandContext interprets as a SIGKILL request. The result
	// would be "signal: killed" on the very first successful start —
	// the bug operators see when they click Start and then refresh the
	// page. The subprocess lifetime is owned by the Manager, not by any
	// individual caller; Stop() cancels via m.cancel.
	childCtx, cancel := context.WithCancel(context.Background())
	handle, err := m.runner.Start(childCtx, binary,
		"serve", "--port", fmt.Sprintf("%d", port), "--hostname", "127.0.0.1")
	if err != nil {
		cancel()
		return fmt.Errorf("spawn opencode serve: %w", err)
	}

	m.mu.Lock()
	m.cmd = handle
	m.cancel = cancel
	m.startedAt = time.Now()
	m.mu.Unlock()

	slog.Info("opencode: subprocess started",
		"binary", binary, "port", port)

	if err := m.waitReady(ctx, port); err != nil {
		// Tear down the half-started child so it doesn't linger.
		cancel()
		_ = handle.Wait()
		m.mu.Lock()
		m.cmd = nil
		m.cancel = nil
		m.mu.Unlock()
		return fmt.Errorf("opencode serve never reached ready: %w", err)
	}
	slog.Info("opencode: subprocess ready", "port", port)
	return nil
}

// waitReady polls /global/health until it returns OK or the timeout
// elapses. OpenCode's documented server API exposes this health route;
// the server is not an OpenAI-compatible /v1 chat endpoint.
func (m *Manager) waitReady(ctx context.Context, port int) error {
	deadline := time.Now().Add(readinessTimeout)
	url := fmt.Sprintf("http://127.0.0.1:%d/global/health", port)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		resp, err := m.http.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 500 {
				// 2xx, 3xx, and 4xx all imply the server bound the port
				// and accepted the request. A 401 still means "ready".
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s", readinessTimeout)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Stop sends SIGTERM to the running subprocess and waits up to
// stopTimeout for graceful exit before escalating to SIGKILL. Safe to
// call when not running; idempotent across concurrent callers.
func (m *Manager) Stop() error {
	m.mu.Lock()
	handle := m.cmd
	cancel := m.cancel
	done := m.supervisorDone
	if handle == nil {
		m.stopping = false
		m.mu.Unlock()
		return nil
	}
	m.stopping = true
	m.mu.Unlock()

	slog.Info("opencode: stopping subprocess")
	if err := handle.Signal(syscall.SIGTERM); err != nil {
		// Process likely already gone; fall through to wait/kill.
		slog.Debug("opencode: SIGTERM returned error", "error", err)
	}

	// Wait for the supervisor goroutine to acknowledge the exit. This
	// implicitly waits on handle.Wait() and clears m.cmd. Falls back
	// to SIGKILL on timeout so we never deadlock the caller.
	if done != nil {
		select {
		case <-done:
		case <-time.After(stopTimeout):
			slog.Warn("opencode: graceful stop timed out, escalating to SIGKILL")
			_ = handle.Kill()
			if cancel != nil {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				slog.Error("opencode: supervisor did not exit after SIGKILL")
			}
		}
	}

	m.mu.Lock()
	m.cacheModels = nil
	m.cacheAt = time.Time{}
	m.mu.Unlock()
	return nil
}
