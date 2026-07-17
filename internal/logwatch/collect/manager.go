// Package collect is the Monitoring feature's pull loop: for every
// enabled log source it periodically runs the fixed read-only docker
// logs command over sshx, redacts the output, and hands lines to the
// distiller sink. Cursored pulls make every tick idempotent-ish; a
// missed tick just means the next pull covers more.
package collect

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/don-works/mcplexer/internal/logwatch/sshx"
	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
)

// tickInterval is how often the manager re-evaluates source due-ness.
// Source cadence itself comes from each source's schedule_spec.
const tickInterval = 15 * time.Second

// tickConcurrency bounds how many due sources are pulled in parallel
// within one tick. Production remotes and DB connections don't need
// (and shouldn't get) an unbounded fan-out; tick() still waits for
// the whole batch before returning, so ticks themselves never overlap
// and a source is never pulled twice concurrently.
const tickConcurrency = 4

// RunnerEnabled reports whether THIS daemon executes monitoring jobs.
// Default true; MCPLEXER_MONITORING_RUNNER=0 marks a viewer daemon in
// a peer group whose always-on runner (the LXC) owns collection +
// workers. Shared by daemon wiring and the status API so the UI's
// peer-responsibilities panel and the actual behaviour cannot drift.
func RunnerEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv("MCPLEXER_MONITORING_RUNNER")))
	return v != "0" && v != "false" && v != "off" && v != "no"
}

// pullTimeout is the per-pull wall-clock cap (ADR 0007 §4).
const pullTimeout = 30 * time.Second

// Store is the narrow slice of store.Store the collector needs — an
// interface seam so tests run against a small fake. tick() pulls up
// to tickConcurrency sources in parallel, so every method must be
// safe for concurrent callers (calls for the same source ID never
// overlap; calls for different sources can).
type Store interface {
	ListEnabledLogSources(ctx context.Context) ([]*store.LogSource, error)
	GetRemoteHost(ctx context.Context, id string) (*store.RemoteHost, error)
	GetAuthScope(ctx context.Context, id string) (*store.AuthScope, error)
	UpdateLogSourceCursor(ctx context.Context, id string, ts time.Time, hash string) error
	SetLogSourceFailures(ctx context.Context, id string, n int) error
	SetRemoteHostPin(ctx context.Context, id, pin string) error
}

// SecretReader resolves credential material at dial time only.
// Satisfied by *secrets.Manager.
type SecretReader interface {
	Get(ctx context.Context, scopeID, key string) ([]byte, error)
}

// Runner executes one bounded pull for a source. The production runner
// wraps sshx and builds the fixed per-kind command; tests substitute a
// fake so no network is involved.
//
// cursor is journald's opaque cursor from the previous pull, empty for every
// other kind and on a journald source's first pull. logSince is the read-only
// log command's boundary; eventsSince is the unadjusted fallback for Docker
// lifecycle events. They differ by 1ns on a steady Docker-family log pull so
// the log tail is exclusive without skipping a restart event exactly at the
// persisted cursor.
type Runner interface {
	Pull(ctx context.Context, host *store.RemoteHost, cred sshx.Credential, src *store.LogSource, logSince, eventsSince time.Time, cursor string) (PullResult, error)
}

// Line is one redacted, timestamped log line handed to the sink.
type Line struct {
	TS   time.Time
	Text string
	// IncidentID identifies one occurrence without changing TemplateID.
	// Notify asks the distiller to dispatch the occurrence even when its
	// stable template was seen before.
	IncidentID string
	Notify     bool
}

// Sink receives each pull's lines. Synthetic collector events
// (truncation, discontinuity) arrive as ordinary lines prefixed
// "logwatch:" so they distill into templates like everything else.
// M3 plugs the distiller in here. tick() pulls up to tickConcurrency
// sources in parallel, so Ingest MUST be safe for concurrent callers
// — never two calls for the same source at once, but calls for
// different sources can and do overlap.
type Sink interface {
	Ingest(ctx context.Context, src *store.LogSource, host *store.RemoteHost, lines []Line) error
}

// Manager owns the pull loop.
type Manager struct {
	store   Store
	secrets SecretReader
	runner  Runner
	sink    Sink

	mu        sync.Mutex
	lastRun   map[string]time.Time // source id → last pull attempt
	dark      map[string]darkEpisode
	darkSeq   uint64
	hostPorts map[string]string // host id → last observed exposure fingerprint
	truncated map[string]string // source id → active truncation episode id
	truncSeq  uint64
	now       func() time.Time
	interval  time.Duration
}

// NewManager wires a collector. runner may be nil (defaults to the
// real sshx runner).
func NewManager(st Store, secrets SecretReader, sink Sink, runner Runner) *Manager {
	if runner == nil {
		runner = sshRunner{}
	}
	return &Manager{
		store: st, secrets: secrets, runner: runner, sink: sink,
		lastRun: map[string]time.Time{}, dark: map[string]darkEpisode{},
		hostPorts: map[string]string{}, truncated: map[string]string{},
		now: time.Now, interval: tickInterval,
	}
}

// Run loops until ctx is cancelled. Call in a goroutine at daemon boot.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.tick(ctx)
		}
	}
}

// tick pulls every source that is due per its schedule_spec, up to
// tickConcurrency at once, and waits for the whole batch before
// returning — so the next tick (from Run's loop) never starts while
// this one is still mid-flight.
func (m *Manager) tick(ctx context.Context) {
	sources, err := m.store.ListEnabledLogSources(ctx)
	if err != nil {
		slog.Warn("logwatch: list sources", "error", err)
		return
	}
	var g errgroup.Group
	g.SetLimit(tickConcurrency)
	for _, src := range sources {
		if ctx.Err() != nil {
			break
		}
		if !m.due(src) {
			continue
		}
		m.markRun(src.ID)
		g.Go(func() error {
			m.pullOne(ctx, src)
			return nil
		})
	}
	_ = g.Wait()
}

// pullOne runs one source's pull and records failure accounting. It
// never returns an error to the errgroup — one source's failure must
// not cancel or skip its siblings mid-tick.
func (m *Manager) pullOne(ctx context.Context, src *store.LogSource) {
	if ctx.Err() != nil {
		return
	}
	err := m.pullSource(ctx, src)
	if err == nil {
		m.recordPullSuccess(ctx, src)
		return
	}
	if ctx.Err() != nil {
		return
	}
	failures := src.ConsecutiveFailures + 1
	if storeErr := m.store.SetLogSourceFailures(ctx, src.ID, failures); storeErr != nil {
		slog.Warn("logwatch: record failure", "source", src.Name, "error", storeErr)
	}
	m.notifyCollectionFailure(ctx, src, failures, err)
	slog.Warn("logwatch: pull failed", "source", src.Name,
		"consecutive_failures", failures, "error", err)
}

func (m *Manager) recordPullSuccess(ctx context.Context, src *store.LogSource) {
	m.clearDarkEpisode(src.ID)
	if src.ConsecutiveFailures == 0 {
		return
	}
	if err := m.store.SetLogSourceFailures(ctx, src.ID, 0); err != nil {
		slog.Warn("logwatch: reset failures", "source", src.Name, "error", err)
	}
}

func (m *Manager) due(src *store.LogSource) bool {
	m.mu.Lock()
	last := m.lastRun[src.ID]
	m.mu.Unlock()
	if last.IsZero() {
		return true
	}
	next, err := scheduler.NextRun(scheduler.KindWorker, src.ScheduleSpec, last)
	if err != nil {
		slog.Warn("logwatch: bad schedule_spec, using 2m fallback",
			"source", src.Name, "spec", src.ScheduleSpec, "error", err)
		next = last.Add(2 * time.Minute)
	}
	return !m.now().Before(next)
}

func (m *Manager) markRun(sourceID string) {
	m.mu.Lock()
	m.lastRun[sourceID] = m.now()
	m.mu.Unlock()
}
