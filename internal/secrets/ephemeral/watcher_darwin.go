//go:build darwin

package ephemeral

import (
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"
)

// kqueueWatcher uses BSD kqueue with EVFILT_VNODE to detect when a watched
// file is opened/read by another process. On the first event we hard-delete
// the file. NOTE_DELETE / NOTE_RENAME also tear down the watch.
//
// We also run an atime-polling check in parallel: NOTE_OPEN fires for opens
// performed AFTER the kevent registers, but readers that open via descriptor
// caching (or kernel implementations that elide vnode notifications) can be
// missed. The polling check provides a 100ms fallback that observes the
// atime advance.
type kqueueWatcher struct {
	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup
	stop   chan struct{}
}

func newWatcher() watcher { return &kqueueWatcher{stop: make(chan struct{})} }

func (w *kqueueWatcher) WatchAndDelete(path string) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.wg.Add(2)
	w.mu.Unlock()

	// kqueue path: low-latency event-driven detection.
	go func() {
		defer w.wg.Done()
		w.watchOne(path)
	}()
	// atime polling fallback: catches readers that don't trigger NOTE_OPEN.
	go func() {
		defer w.wg.Done()
		w.pollAtime(path)
	}()
}

// pollAtime stats path every 100ms and removes it once atime > mtime,
// indicating another process has read the file. Whichever of kqueue or
// polling fires first deletes the file; the other just observes ENOENT.
func (w *kqueueWatcher) pollAtime(path string) {
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
				continue
			}
			a := atimeOf(info)
			if a.After(info.ModTime()) {
				if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
					slog.Warn("kqueue/atime: remove on read",
						"path", path, "error", err)
				}
				return
			}
		}
	}
}

func (w *kqueueWatcher) Close() {
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

func (w *kqueueWatcher) watchOne(path string) {
	f, err := os.Open(path)
	if err != nil {
		slog.Warn("kqueue: open watched file", "path", path, "error", err)
		return
	}
	defer func() { _ = f.Close() }()

	kq, err := syscall.Kqueue()
	if err != nil {
		slog.Warn("kqueue: init", "error", err)
		return
	}
	defer func() { _ = syscall.Close(kq) }()

	// NOTE_OPEN is the most reliable signal for "another process started
	// reading this file". It is available on macOS 10.10+ (sys/event.h
	// constant 0x00000080). Stdlib's syscall package does not expose
	// NOTE_OPEN, so we use the literal value here.
	const noteOpen = 0x00000080

	// EVFILT_VNODE = -4. Wait for OPEN | DELETE | RENAME | ATTRIB.
	change := syscall.Kevent_t{
		Ident:  uint64(f.Fd()),
		Filter: syscall.EVFILT_VNODE,
		Flags:  syscall.EV_ADD | syscall.EV_CLEAR,
		Fflags: noteOpen | syscall.NOTE_DELETE | syscall.NOTE_RENAME | syscall.NOTE_ATTRIB,
	}
	events := make([]syscall.Kevent_t, 1)
	if _, err := syscall.Kevent(kq, []syscall.Kevent_t{change}, nil, nil); err != nil {
		slog.Warn("kqueue: register", "error", err)
		return
	}

	timeout := syscall.Timespec{Sec: 1}
	deadline := time.Now().Add(15 * time.Minute)
	for {
		select {
		case <-w.stop:
			return
		default:
		}
		if time.Now().After(deadline) {
			return
		}
		n, err := syscall.Kevent(kq, nil, events, &timeout)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			slog.Warn("kqueue: wait", "error", err)
			return
		}
		if n == 0 {
			continue
		}
		// Any event triggers deletion — we want to remove on first read.
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.Warn("kqueue: remove on event", "path", path, "error", err)
		}
		return
	}
}
