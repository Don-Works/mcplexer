//go:build linux

package ephemeral

import (
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"
)

// inotifyWatcher uses Linux inotify with IN_OPEN | IN_ACCESS to detect when
// a watched file is read by another process. On the first event we hard-
// delete the file.
type inotifyWatcher struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
	stop   chan struct{}
}

func newWatcher() watcher { return &inotifyWatcher{stop: make(chan struct{})} }

func (w *inotifyWatcher) WatchAndDelete(path string) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.wg.Add(1)
	w.mu.Unlock()

	go func() {
		defer w.wg.Done()
		w.watchOne(path)
	}()
}

func (w *inotifyWatcher) Close() {
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

func (w *inotifyWatcher) watchOne(path string) {
	fd, err := syscall.InotifyInit1(syscall.IN_CLOEXEC | syscall.IN_NONBLOCK)
	if err != nil {
		slog.Warn("inotify: init", "error", err)
		return
	}
	defer func() { _ = syscall.Close(fd) }()

	// IN_OPEN | IN_ACCESS catches the very first read attempt; IN_DELETE_SELF
	// tears down the watch if something else removed the file first.
	mask := uint32(syscall.IN_OPEN | syscall.IN_ACCESS | syscall.IN_DELETE_SELF)
	if _, err := syscall.InotifyAddWatch(fd, path, mask); err != nil {
		slog.Warn("inotify: add watch", "path", path, "error", err)
		return
	}

	deadline := time.Now().Add(15 * time.Minute)
	buf := make([]byte, syscall.SizeofInotifyEvent+syscall.PathMax+1)
	for {
		select {
		case <-w.stop:
			return
		default:
		}
		if time.Now().After(deadline) {
			return
		}
		n, err := syscall.Read(fd, buf)
		if err != nil {
			if err == syscall.EAGAIN || err == syscall.EINTR {
				time.Sleep(50 * time.Millisecond)
				continue
			}
			slog.Warn("inotify: read", "error", err)
			return
		}
		if n <= 0 {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("inotify: remove on event", "path", path, "error", err)
		}
		return
	}
}
