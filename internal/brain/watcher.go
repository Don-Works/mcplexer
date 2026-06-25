package brain

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// defaultDebounce is the per-path settle window. A single editor save
// surfaces as a Create/Rename/Write burst; coalescing them avoids
// reindexing the same file 3–5× (SPEC §6.2).
const defaultDebounce = 300 * time.Millisecond

// debounceCeiling is the hard upper bound on how long a path's reindex is
// deferred while events keep arriving, so a streaming writer still flushes.
const debounceCeiling = 60 * time.Second

// Watcher drives the inbound indexer from fsnotify events. It watches
// PARENT directories (not individual files — editors save atomically via
// temp+rename, which fires on the dir, and recursive watch is
// unsupported); manually Adds subdirs as they are created; drops Chmod
// events; and debounces per-path before invoking the indexer.
type Watcher struct {
	w        *fsnotify.Watcher
	ix       *Indexer
	debounce time.Duration
	ceiling  time.Duration
	log      *slog.Logger
	notify   func(paths []string) // optional autocommit hook (M2)

	mu     sync.Mutex
	timers map[string]*debounceState
	closed bool
	done   chan struct{}
}

type debounceState struct {
	timer *time.Timer
	first time.Time
}

// NewWatcher constructs a Watcher over the indexer's brain dir. debounce
// <= 0 uses defaultDebounce.
func NewWatcher(ix *Indexer, debounce time.Duration) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if debounce <= 0 {
		debounce = defaultDebounce
	}
	return &Watcher{
		w:        fw,
		ix:       ix,
		debounce: debounce,
		ceiling:  debounceCeiling,
		log:      ix.log,
		timers:   make(map[string]*debounceState),
	}, nil
}

// SetCommitNotify wires the autocommit callback (M2). Nil disables it.
func (w *Watcher) SetCommitNotify(fn func(paths []string)) { w.notify = fn }

// AddDir extends the watch set to cover dir and its existing subdirs. Used
// to observe a repo-local .mcplexer/ folder registered after Start (M6 —
// federation). Idempotent: fsnotify.Add on an already-watched dir is a
// no-op. A missing dir is tolerated. Safe to call from the session-resolve
// path concurrently with the event loop (addTree only touches the fsnotify
// watcher, which is internally synchronised).
func (w *Watcher) AddDir(dir string) error {
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return nil
	}
	return w.addTree(dir)
}

// Start adds the watch set (the brain root + every existing subdir under
// workspaces/) and runs the event loop in a goroutine until ctx is
// cancelled or Close is called.
func (w *Watcher) Start(ctx context.Context) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	if w.done != nil {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()

	root := filepath.Join(w.ix.cfg.Dir, "workspaces")
	if err := w.addTree(root); err != nil {
		return err
	}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	if w.done != nil {
		w.mu.Unlock()
		return nil
	}
	done := make(chan struct{})
	w.done = done
	w.mu.Unlock()

	go func() {
		defer close(done)
		w.loop(ctx)
	}()
	return nil
}

// addTree adds dir and all its existing subdirectories to the watch set.
// A missing root is tolerated (the brain repo may not exist yet).
func (w *Watcher) addTree(root string) error {
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			// Don't descend into dot-dirs (.history/, .git/, .cache/) — they
			// hold archive/VCS/derived data, never indexable records. The
			// per-repo brain root (.mcplexer/, M6) is the exception: it IS an
			// indexable folder, so it (and its non-dot subdirs) are watched.
			if name := d.Name(); name != RepoBrainDirName && len(name) > 1 && name[0] == '.' {
				return filepath.SkipDir
			}
			if addErr := w.w.Add(path); addErr != nil {
				w.log.Warn("brain: watch add", "path", path, "error", addErr)
			}
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// loop is the event pump. Chmod events are dropped; dir creates extend
// the watch set; file events are debounced per path.
func (w *Watcher) loop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.w.Events:
			if !ok {
				return
			}
			w.handle(ev)
		case err, ok := <-w.w.Errors:
			if !ok {
				return
			}
			w.log.Warn("brain: watcher error", "error", err)
		}
	}
}

// handle routes one fsnotify event.
func (w *Watcher) handle(ev fsnotify.Event) {
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return
	}

	if ev.Op&fsnotify.Chmod != 0 && ev.Op == fsnotify.Chmod {
		return // pure Chmod — ignore (SPEC §6.1)
	}

	// A newly created directory must be added to the watch set so its
	// children are observed (fsnotify is non-recursive).
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			if err := w.addTree(ev.Name); err != nil {
				w.log.Warn("brain: watch new subdir", "path", ev.Name, "error", err)
			}
			return
		}
	}

	if !isMarkdown(ev.Name) {
		return
	}
	w.schedule(ev.Name)
}

// schedule (re)arms the per-path debounce timer. Repeated events reset the
// timer up to the hard ceiling measured from the first event in the burst.
func (w *Watcher) schedule(path string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	now := time.Now()
	st, ok := w.timers[path]
	if !ok {
		st = &debounceState{first: now}
		w.timers[path] = st
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	delay := w.debounce
	if elapsed := now.Sub(st.first); elapsed+w.debounce > w.ceiling {
		// Past the ceiling — fire as soon as possible.
		if remaining := w.ceiling - elapsed; remaining < delay {
			delay = remaining
		}
		if delay < 0 {
			delay = 0
		}
	}
	st.timer = time.AfterFunc(delay, func() { w.fire(path) })
}

// fire runs the indexer for a settled path and clears its debounce state.
func (w *Watcher) fire(path string) {
	w.mu.Lock()
	delete(w.timers, path)
	closed := w.closed
	w.mu.Unlock()
	if closed {
		return
	}
	if err := w.ix.IndexFile(context.Background(), path); err != nil {
		w.log.Warn("brain: index on settle", "path", path, "error", err)
	}
	if w.notify != nil {
		w.notify([]string{path})
	}
}

// Close stops the watcher and any pending debounce timers.
func (w *Watcher) Close() error {
	w.mu.Lock()
	w.closed = true
	for _, st := range w.timers {
		if st.timer != nil {
			st.timer.Stop()
		}
	}
	w.timers = make(map[string]*debounceState)
	done := w.done
	w.mu.Unlock()

	err := w.w.Close()
	if done != nil {
		<-done
	}
	return err
}
