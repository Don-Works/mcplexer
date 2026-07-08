// Package collect is the Monitoring feature's pull loop: for every
// enabled log source it periodically runs the fixed read-only docker
// logs command over sshx, redacts the output, and hands lines to the
// distiller sink. Cursored pulls make every tick idempotent-ish; a
// missed tick just means the next pull covers more.
package collect

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/sshx"
	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
)

// tickInterval is how often the manager re-evaluates source due-ness.
// Source cadence itself comes from each source's schedule_spec.
const tickInterval = 15 * time.Second

// pullTimeout is the per-pull wall-clock cap (ADR 0007 §4).
const pullTimeout = 30 * time.Second

// Store is the narrow slice of store.Store the collector needs —
// an interface seam so tests run against a small fake.
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

// Runner executes one bounded pull. The production runner wraps sshx;
// tests substitute a fake so no network is involved.
type Runner interface {
	Pull(ctx context.Context, host *store.RemoteHost, cred sshx.Credential, selector string, since time.Time, maxBytes int64) (sshx.Result, error)
}

// Line is one redacted, timestamped log line handed to the sink.
type Line struct {
	TS   time.Time
	Text string
}

// Sink receives each pull's lines. Synthetic collector events
// (truncation, discontinuity) arrive as ordinary lines prefixed
// "logwatch:" so they distill into templates like everything else.
// M3 plugs the distiller in here.
type Sink interface {
	Ingest(ctx context.Context, src *store.LogSource, host *store.RemoteHost, lines []Line) error
}

// Manager owns the pull loop.
type Manager struct {
	store   Store
	secrets SecretReader
	runner  Runner
	sink    Sink

	mu       sync.Mutex
	lastRun  map[string]time.Time // source id → last pull attempt
	now      func() time.Time
	interval time.Duration
}

// NewManager wires a collector. runner may be nil (defaults to the
// real sshx runner).
func NewManager(st Store, secrets SecretReader, sink Sink, runner Runner) *Manager {
	if runner == nil {
		runner = sshRunner{}
	}
	return &Manager{
		store: st, secrets: secrets, runner: runner, sink: sink,
		lastRun: map[string]time.Time{}, now: time.Now, interval: tickInterval,
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

// tick pulls every source that is due per its schedule_spec.
func (m *Manager) tick(ctx context.Context) {
	sources, err := m.store.ListEnabledLogSources(ctx)
	if err != nil {
		slog.Warn("logwatch: list sources", "error", err)
		return
	}
	for _, src := range sources {
		if !m.due(src) {
			continue
		}
		m.markRun(src.ID)
		if err := m.pullSource(ctx, src); err != nil {
			n := src.ConsecutiveFailures + 1
			if serr := m.store.SetLogSourceFailures(ctx, src.ID, n); serr != nil {
				slog.Warn("logwatch: record failure", "source", src.Name, "error", serr)
			}
			slog.Warn("logwatch: pull failed", "source", src.Name,
				"consecutive_failures", n, "error", err)
		}
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
