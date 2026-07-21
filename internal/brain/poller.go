package brain

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// defaultPollInterval is the sweep cadence for the polling watcher. The
// daemon indexes its own writes in-process, so sweeps only bound how fast
// EXTERNAL edits (editors, git pulls) are noticed.
const defaultPollInterval = 15 * time.Second

// DirWatcher is the shared surface of the fsnotify Watcher and the mtime
// Poller so the daemon can choose per platform.
type DirWatcher interface {
	Start(ctx context.Context) error
	AddDir(dir string) error
	SetCommitNotify(fn func(paths []string))
	Close() error
}

// fileStamp is the per-path change detector: mtime plus size, so a
// same-instant rewrite of equal length is the only undetectable case.
type fileStamp struct {
	mtime int64 // UnixNano
	size  int64
}

// Poller drives the inbound indexer from periodic mtime sweeps. It is the
// darwin alternative to the fsnotify Watcher: fsnotify's kqueue backend
// holds one open file descriptor per file inside every watched directory,
// so a brain tree with thousands of task/memory records inflates the
// daemon's fd table toward exhaustion. A sweep holds no fds between ticks.
type Poller struct {
	ix       *Indexer
	interval time.Duration
	notify   func(paths []string) // optional autocommit hook (M2)

	mu     sync.Mutex
	roots  []string
	seen   map[string]fileStamp
	closed bool
	stop   chan struct{}
	done   chan struct{}
}

// NewPoller constructs a Poller over the indexer's brain dir. interval <= 0
// uses defaultPollInterval.
func NewPoller(ix *Indexer, interval time.Duration) *Poller {
	if interval <= 0 {
		interval = defaultPollInterval
	}
	return &Poller{
		ix:       ix,
		interval: interval,
		seen:     make(map[string]fileStamp),
		stop:     make(chan struct{}),
	}
}

// SetCommitNotify wires the autocommit callback (M2). Nil disables it.
func (p *Poller) SetCommitNotify(fn func(paths []string)) { p.notify = fn }

// AddDir extends the sweep set (M6 federation parity with Watcher.AddDir).
// Existing content is baselined silently; only later changes index.
func (p *Poller) AddDir(dir string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	for _, r := range p.roots {
		if r == dir {
			return nil
		}
	}
	p.roots = append(p.roots, dir)
	for path, st := range scanTree(dir) {
		p.seen[path] = st
	}
	return nil
}

// Start baselines the workspaces tree (matching the Watcher, which only
// reacts to events after it starts — the startup reindex sweep owns
// pre-existing content) and runs the sweep loop until ctx is cancelled or
// Close is called.
func (p *Poller) Start(ctx context.Context) error {
	root := filepath.Join(p.ix.cfg.Dir, "workspaces")
	p.mu.Lock()
	if p.closed || p.done != nil {
		p.mu.Unlock()
		return nil
	}
	p.roots = append(p.roots, root)
	for path, st := range scanTree(root) {
		p.seen[path] = st
	}
	done := make(chan struct{})
	p.done = done
	p.mu.Unlock()

	go func() {
		defer close(done)
		ticker := time.NewTicker(p.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-p.stop:
				return
			case <-ticker.C:
				p.sweep(ctx)
			}
		}
	}()
	return nil
}

// sweep walks every root, indexes new/changed .md files, and mirrors the
// Watcher's remove behaviour by handing vanished paths to the indexer too.
func (p *Poller) sweep(ctx context.Context) {
	p.mu.Lock()
	roots := append([]string(nil), p.roots...)
	p.mu.Unlock()

	current := make(map[string]fileStamp)
	for _, root := range roots {
		for path, st := range scanTree(root) {
			current[path] = st
		}
	}

	var changed []string
	p.mu.Lock()
	for path, st := range current {
		if prev, ok := p.seen[path]; !ok || prev != st {
			changed = append(changed, path)
		}
	}
	for path := range p.seen {
		if _, ok := current[path]; !ok {
			changed = append(changed, path)
		}
	}
	p.seen = current
	closed := p.closed
	p.mu.Unlock()
	if closed || len(changed) == 0 {
		return
	}

	sort.Strings(changed) // deterministic index order across sweeps
	for _, path := range changed {
		if err := p.ix.IndexFile(ctx, path); err != nil {
			p.ix.log.Warn("brain: index on sweep", "path", path, "error", err)
		}
	}
	if p.notify != nil {
		p.notify(changed)
	}
}

// Close stops the sweep loop and waits for it to drain.
func (p *Poller) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.stop)
	done := p.done
	p.mu.Unlock()
	if done != nil {
		<-done
	}
	return nil
}

// scanTree stats every indexable .md under root, applying the same dot-dir
// pruning as Watcher.addTree. A missing root yields an empty map.
func scanTree(root string) map[string]fileStamp {
	out := make(map[string]fileStamp)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable entries are skipped; the sweep continues
		}
		if d.IsDir() {
			if name := d.Name(); name != RepoBrainDirName && len(name) > 1 && name[0] == '.' {
				return filepath.SkipDir
			}
			return nil
		}
		if !isMarkdown(path) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		out[path] = fileStamp{mtime: info.ModTime().UnixNano(), size: info.Size()}
		return nil
	})
	return out
}
