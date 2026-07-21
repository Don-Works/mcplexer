// Package triggers drives the Schedule Guard's non-time-based job kinds:
// file_watch (fsnotify), git_hook (git hooks adoption), and the crontab
// import wizard. Each trigger calls back into the Scheduler's RunOnce
// so audit/approval/execution stays in one place.
package triggers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/don-works/mcplexer/internal/scheduler"
	"github.com/don-works/mcplexer/internal/store"
)

// JobRunner is the narrow Scheduler surface triggers need. The Scheduler
// already implements RunOnce; tests pass a stub that records calls.
type JobRunner interface {
	RunOnce(ctx context.Context, jobID string) error
}

// defaultDebounce is the default per-job event coalescing window. A
// single editor save typically emits CHMOD+WRITE+CLOSE_WRITE within a
// few ms; 500ms collapses that into one fire.
const defaultDebounce = 500 * time.Millisecond

// FileWatcher watches a set of path globs (one per ScheduledJob with
// kind="file_watch"). When a watched path changes, it invokes
// runner.RunOnce(ctx, job.ID) — exactly like a cron tick would.
type FileWatcher struct {
	store    store.ScheduledJobStore
	runner   JobRunner
	debounce time.Duration

	mu       sync.Mutex
	watcher  *fsnotify.Watcher
	specs    map[string]string   // jobID -> spec (glob)
	watched  map[string]struct{} // dirs currently in fsnotify
	pending  map[string]*time.Timer
	running  bool
	cancelFn context.CancelFunc
	doneCh   chan struct{}
}

// NewFileWatcher constructs but does not start a FileWatcher. A zero
// debounceWindow falls back to defaultDebounce.
func NewFileWatcher(
	s store.ScheduledJobStore, runner JobRunner, debounceWindow time.Duration,
) (*FileWatcher, error) {
	if s == nil {
		return nil, errors.New("filewatch: store required")
	}
	if runner == nil {
		return nil, errors.New("filewatch: runner required")
	}
	if debounceWindow <= 0 {
		debounceWindow = defaultDebounce
	}
	return &FileWatcher{
		store:    s,
		runner:   runner,
		debounce: debounceWindow,
		specs:    map[string]string{},
		watched:  map[string]struct{}{},
		pending:  map[string]*time.Timer{},
	}, nil
}

// Start kicks off the fsnotify watcher goroutine. Returns immediately;
// the goroutine runs until ctx is cancelled or Stop is called. It is
// safe to call Reload before Start.
func (w *FileWatcher) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return errors.New("filewatch: already running")
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		w.mu.Unlock()
		return fmt.Errorf("filewatch: new watcher: %w", err)
	}
	w.watcher = fw
	loopCtx, cancel := context.WithCancel(ctx)
	w.cancelFn = cancel
	w.doneCh = make(chan struct{})
	w.running = true
	w.mu.Unlock()
	if err := w.Reload(loopCtx); err != nil {
		_ = w.Stop()
		return err
	}
	go w.run(loopCtx)
	return nil
}

// Stop signals the watcher goroutine, closes fsnotify, and waits up to
// 2s for the loop to exit. Idempotent.
func (w *FileWatcher) Stop() error {
	w.mu.Lock()
	if !w.running {
		w.mu.Unlock()
		return nil
	}
	cancel := w.cancelFn
	done := w.doneCh
	fw := w.watcher
	w.running = false
	w.cancelFn = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if fw != nil {
		_ = fw.Close()
	}
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("filewatch: stop timed out")
	}
}

// Reload re-reads every kind="file_watch" job from the store and
// rebuilds the (jobID -> spec) map. Newly seen specs add fsnotify
// watches on their fixed-prefix directory; removed jobs are dropped
// from the map but watched dirs are not torn down (cheap, idempotent
// — fsnotify dedups Add calls).
func (w *FileWatcher) Reload(ctx context.Context) error {
	jobs, err := w.store.ListScheduledJobs(ctx)
	if err != nil {
		return fmt.Errorf("filewatch: list jobs: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	next := map[string]string{}
	for _, j := range jobs {
		if !j.Enabled || j.Kind != scheduler.KindFileWatch {
			continue
		}
		next[j.ID] = j.Spec
	}
	w.specs = next
	if w.watcher == nil {
		return nil
	}
	for _, spec := range next {
		dir := fixedPrefixDir(spec)
		if dir == "" {
			continue
		}
		if _, ok := w.watched[dir]; ok {
			continue
		}
		if err := w.watcher.Add(dir); err != nil {
			slog.Warn("filewatch: add", "dir", dir, "err", err)
			continue
		}
		w.watched[dir] = struct{}{}
	}
	return nil
}

// run is the event loop. Each fsnotify event is matched against every
// known spec (cheap — specs are usually <10) and matching jobs are
// debounced per-id before RunOnce is called.
func (w *FileWatcher) run(ctx context.Context) {
	defer close(w.doneCh)
	w.mu.Lock()
	fw := w.watcher
	w.mu.Unlock()
	if fw == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-fw.Events:
			if !ok {
				return
			}
			w.handleEvent(ctx, ev)
		case err, ok := <-fw.Errors:
			if !ok {
				return
			}
			slog.Warn("filewatch: watcher error", "err", err)
		}
	}
}

// handleEvent dispatches a single fsnotify event. New directories
// created under a watched root are auto-added (recursive watch).
func (w *FileWatcher) handleEvent(ctx context.Context, ev fsnotify.Event) {
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			w.addWatchDir(ev.Name)
		}
	}
	w.mu.Lock()
	matches := matchingJobs(w.specs, ev.Name)
	w.mu.Unlock()
	for _, jobID := range matches {
		w.scheduleFire(ctx, jobID)
	}
}

func (w *FileWatcher) addWatchDir(dir string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.watcher == nil {
		return
	}
	if _, ok := w.watched[dir]; ok {
		return
	}
	if err := w.watcher.Add(dir); err != nil {
		slog.Warn("filewatch: add dir", "dir", dir, "err", err)
		return
	}
	w.watched[dir] = struct{}{}
}

// scheduleFire debounces per job-id. If a timer is already pending,
// reset it; otherwise create a new one whose callback invokes the
// runner outside the mutex.
func (w *FileWatcher) scheduleFire(ctx context.Context, jobID string) {
	w.mu.Lock()
	if t, ok := w.pending[jobID]; ok {
		t.Reset(w.debounce)
		w.mu.Unlock()
		return
	}
	t := time.AfterFunc(w.debounce, func() {
		w.mu.Lock()
		delete(w.pending, jobID)
		w.mu.Unlock()
		if err := w.runner.RunOnce(ctx, jobID); err != nil {
			slog.Warn("filewatch: RunOnce", "job", jobID, "err", err)
		}
	})
	w.pending[jobID] = t
	w.mu.Unlock()
}
