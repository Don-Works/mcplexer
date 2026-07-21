package ephemeral

import (
	"log/slog"
	"os"
	"sync"
	"time"
)

// watcher is the platform-specific file-read detector. Implementations call
// onFirstRead exactly once per path, then hard-delete the file.
type watcher interface {
	WatchAndDelete(path string)
	Close()
}

// pollingWatcher is the cross-platform fallback used on systems where we
// don't ship a kqueue/inotify implementation. It busy-polls the file's atime
// (which the kernel updates on read on most filesystems with default mount
// options). When atime advances past the file's mtime, the watcher hard-
// deletes the file. This is best-effort — kqueue/inotify on darwin/linux is
// strictly preferred, and the build-tag selects them automatically.
//
//nolint:unused // Used by watcher_other.go on platforms other than darwin/linux.
type pollingWatcher struct {
	mu     sync.Mutex
	closed bool
	stop   chan struct{}
	wg     sync.WaitGroup
}

//nolint:unused // Used by watcher_other.go on platforms other than darwin/linux.
func newPollingWatcher() *pollingWatcher {
	return &pollingWatcher{stop: make(chan struct{})}
}

//nolint:unused // Used by watcher_other.go on platforms other than darwin/linux.
func (w *pollingWatcher) WatchAndDelete(path string) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.wg.Add(1)
	w.mu.Unlock()

	go func() {
		defer w.wg.Done()
		w.pollForRead(path)
	}()
}

// pollForRead checks the atime of path every 100ms; once atime > mtime it
// hard-deletes the file.
//
//nolint:unused // Used by watcher_other.go on platforms other than darwin/linux.
func (w *pollingWatcher) pollForRead(path string) {
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	deadline := time.Now().Add(15 * time.Minute)
	for {
		select {
		case <-w.stop:
			return
		case <-t.C:
			if time.Now().After(deadline) {
				return
			}
			info, err := os.Stat(path)
			if err != nil {
				if os.IsNotExist(err) {
					return
				}
				slog.Warn("ephemeral polling watcher: stat", "error", err)
				return
			}
			a := atimeOf(info)
			if a.After(info.ModTime()) {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					slog.Warn("ephemeral polling watcher: remove",
						"error", err)
				}
				return
			}
		}
	}
}

//nolint:unused // Used by watcher_other.go on platforms other than darwin/linux.
func (w *pollingWatcher) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.stop)
	w.mu.Unlock()
	w.wg.Wait()
}
