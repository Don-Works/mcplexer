package brain

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Autocommit cadence. AUTO local commit on idle, MANUAL push (Appendix B
// decision #6). The idle window coalesces a burst of agent/editor writes
// into one meaningful commit; the ceiling guarantees a streaming writer
// still gets flushed (SPEC §7).
const (
	defaultAutocommitIdle    = 8 * time.Second
	defaultAutocommitCeiling = 60 * time.Second
)

// AutoCommitter debounces touched-path notifications from the watcher +
// serializer into a single local commit. It NEVER pushes — push is manual
// (the admin tool / dashboard). A change signal resets the idle timer; the
// first signal in a quiet period also arms the ceiling so a continuous
// stream still flushes at the ceiling.
type AutoCommitter struct {
	git     *Git
	idle    time.Duration
	ceiling time.Duration
	log     *slog.Logger

	// provenance recorded in every commit body; set once at wire time.
	session string
	agent   string

	mu          sync.Mutex
	pending     map[string]struct{} // touched paths since last commit
	idleTimer   *time.Timer
	ceilTimer   *time.Timer
	closed      bool
	commitForCB func(ctx context.Context, paths []string, msg string) error // overridable in tests
}

// NewAutoCommitter constructs an AutoCommitter. idle/ceiling <= 0 use the
// defaults. The committer is inert until Notify is called.
func NewAutoCommitter(git *Git, idle, ceiling time.Duration, log *slog.Logger) *AutoCommitter {
	if log == nil {
		log = slog.Default()
	}
	if idle <= 0 {
		idle = defaultAutocommitIdle
	}
	if ceiling <= 0 {
		ceiling = defaultAutocommitCeiling
	}
	a := &AutoCommitter{
		git:     git,
		idle:    idle,
		ceiling: ceiling,
		log:     log,
		pending: make(map[string]struct{}),
	}
	a.commitForCB = func(ctx context.Context, paths []string, msg string) error {
		return git.Commit(ctx, paths, msg)
	}
	return a
}

// SetProvenance records the session/agent that gets stamped into commit
// bodies. Best-effort context; either may be empty.
func (a *AutoCommitter) SetProvenance(session, agent string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.session = session
	a.agent = agent
}

// Notify records touched paths and (re)arms the idle timer. Repeated calls
// reset the idle timer up to the ceiling measured from the first call in a
// quiet period. Safe to call from the watcher + serializer concurrently.
func (a *AutoCommitter) Notify(paths []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	for _, p := range paths {
		if p != "" {
			a.pending[p] = struct{}{}
		}
	}
	if len(a.pending) == 0 {
		return
	}
	if a.idleTimer != nil {
		a.idleTimer.Stop()
	}
	a.idleTimer = time.AfterFunc(a.idle, a.flush)
	if a.ceilTimer == nil {
		// First signal in a quiet period — arm the hard ceiling.
		a.ceilTimer = time.AfterFunc(a.ceiling, a.flush)
	}
}

// flush commits whatever paths have accumulated and clears the timers. It
// is the AfterFunc target for both the idle and ceiling timers; the mutex +
// drained-pending check make a double-fire a no-op.
func (a *AutoCommitter) flush() {
	a.mu.Lock()
	if a.closed || len(a.pending) == 0 {
		a.resetTimersLocked()
		a.mu.Unlock()
		return
	}
	paths := make([]string, 0, len(a.pending))
	for p := range a.pending {
		paths = append(paths, p)
	}
	a.pending = make(map[string]struct{})
	session, agent := a.session, a.agent
	a.resetTimersLocked()
	a.mu.Unlock()

	msg := BuildCommitMessage(paths, session, agent)
	if err := a.commitForCB(context.Background(), paths, msg); err != nil {
		a.log.Warn("brain: autocommit", "paths", len(paths), "error", err)
	}
}

// resetTimersLocked stops + nils both timers. Caller MUST hold a.mu.
func (a *AutoCommitter) resetTimersLocked() {
	if a.idleTimer != nil {
		a.idleTimer.Stop()
		a.idleTimer = nil
	}
	if a.ceilTimer != nil {
		a.ceilTimer.Stop()
		a.ceilTimer = nil
	}
}

// Flush forces an immediate commit of any pending paths (used on daemon
// shutdown so an in-flight burst is not lost). Synchronous.
func (a *AutoCommitter) Flush() {
	a.flush()
}

// Close stops the committer after a final flush of pending work.
func (a *AutoCommitter) Close() {
	a.flush()
	a.mu.Lock()
	a.closed = true
	a.resetTimersLocked()
	a.mu.Unlock()
}

// BuildCommitMessage renders a conventional-commit machine message:
// a [machine]-tagged subject summarising the touched entity kinds, plus a
// body listing the touched paths and the session/agent provenance (SPEC §7).
func BuildCommitMessage(touched []string, session, agent string) string {
	sorted := append([]string(nil), touched...)
	sort.Strings(sorted)

	tasks, mems, other := classifyTouched(sorted)
	parts := make([]string, 0, 3)
	if tasks > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", tasks, plural(tasks, "task", "tasks")))
	}
	if mems > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", mems, plural(mems, "memory", "memories")))
	}
	if other > 0 {
		parts = append(parts, fmt.Sprintf("%d %s", other, plural(other, "file", "files")))
	}
	summary := "changes"
	if len(parts) > 0 {
		summary = strings.Join(parts, ", ")
	}

	ws := dominantWorkspace(sorted)
	subject := "chore(brain): autosave " + summary + "  [machine]"
	if ws != "" {
		subject = "chore(brain): autosave " + ws + " — " + summary + "  [machine]"
	}

	var b strings.Builder
	b.WriteString(subject)
	b.WriteString("\n\nTouched:\n")
	for _, p := range sorted {
		b.WriteString("  ")
		b.WriteString(filepath.ToSlash(p))
		b.WriteString("\n")
	}
	if session != "" || agent != "" {
		b.WriteString("\n")
		if session != "" {
			b.WriteString("Session: " + session + "  ")
		}
		if agent != "" {
			b.WriteString("Agent: " + agent)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// classifyTouched buckets paths by entity kind from their parent dir.
func classifyTouched(paths []string) (tasks, mems, other int) {
	for _, p := range paths {
		switch {
		case strings.Contains(filepath.ToSlash(p), "/"+taskSubdir+"/"):
			tasks++
		case strings.Contains(filepath.ToSlash(p), "/memory/"):
			mems++
		default:
			other++
		}
	}
	return
}

// dominantWorkspace returns the workspace slug if every touched path shares
// one, else "". Keeps the subject specific without lying when a burst spans
// workspaces.
func dominantWorkspace(paths []string) string {
	ws := ""
	for _, p := range paths {
		w := workspaceFromPath(p)
		if w == "" {
			continue
		}
		if ws == "" {
			ws = w
		} else if ws != w {
			return ""
		}
	}
	return ws
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
