// supervisor.go holds the restart-on-crash loop and the model-listing
// cache. These live alongside Manager but in a separate file to keep
// manager.go under the 300-line cap. Logic notes:
//
//   - supervise() is launched once per successful Start. It waits on
//     handle.Wait(), restarts with exponential backoff (1s, 2s, 4s,
//     8s, 16s, capped at 30s), and resets the backoff after
//     stabilityWindow of clean uptime so isolated crashes hours apart
//     don't escalate to the cap.
//   - ListModels caches the parsed output of `opencode models` for
//     modelCacheTTL. The cache is invalidated on Stop because the set
//     of available models can change when the user re-authenticates
//     providers while opencode is down.
package opencode

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// stabilityWindow is the duration of uninterrupted uptime that resets
// the supervisor restart backoff to 1s. Without this, a child that
// crashes hours apart would still hit the 30s cap on its second crash.
const stabilityWindow = 5 * time.Minute

// supervise watches the running subprocess and restarts it with capped
// exponential backoff on unexpected exit. Exits only when Stop is
// called or the parent context is cancelled.
func (m *Manager) supervise(parentCtx context.Context) {
	defer m.closeSupervisorDone()

	backoff := time.Second
	for {
		m.mu.Lock()
		handle := m.cmd
		startedAt := m.startedAt
		m.mu.Unlock()
		if handle == nil {
			return
		}

		waitErr := handle.Wait()

		m.mu.Lock()
		operatorStop := m.stopping
		m.cmd = nil
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		if waitErr != nil {
			m.lastError = waitErr.Error()
		}
		m.mu.Unlock()

		if operatorStop {
			slog.Info("opencode: subprocess stopped by operator")
			return
		}
		if parentCtx.Err() != nil {
			slog.Info("opencode: parent context cancelled, supervisor exiting")
			return
		}

		// Reset backoff after a stable run.
		if !startedAt.IsZero() && time.Since(startedAt) > stabilityWindow {
			backoff = time.Second
		}

		slog.Warn("opencode: subprocess exited, restarting",
			"error", waitErr, "backoff", backoff)
		select {
		case <-parentCtx.Done():
			return
		case <-time.After(backoff):
		}

		if err := m.spawnAndWait(parentCtx); err != nil {
			slog.Error("opencode: restart failed", "error", err)
			m.mu.Lock()
			m.lastError = err.Error()
			m.mu.Unlock()
			backoff = nextBackoff(backoff)
			continue
		}
		backoff = time.Second
	}
}

// closeSupervisorDone closes the supervisor-done channel exactly once.
// Pulled out of supervise() so the deferred call stays small.
func (m *Manager) closeSupervisorDone() {
	m.mu.Lock()
	ch := m.supervisorDone
	m.supervisorDone = nil
	m.mu.Unlock()
	if ch != nil {
		close(ch)
	}
}

// nextBackoff doubles the current backoff, capping at 30s.
func nextBackoff(cur time.Duration) time.Duration {
	cur *= 2
	if cur > 30*time.Second {
		return 30 * time.Second
	}
	return cur
}

// ListModels shells out to `opencode models`, caches the result for
// modelCacheTTL, and returns one model name per non-blank stdout line.
// The cache is shared across goroutines and invalidated on Stop.
func (m *Manager) ListModels(ctx context.Context) ([]string, error) {
	m.mu.Lock()
	if m.cacheModels != nil && time.Since(m.cacheAt) < modelCacheTTL {
		out := make([]string, len(m.cacheModels))
		copy(out, m.cacheModels)
		m.mu.Unlock()
		return out, nil
	}
	m.mu.Unlock()
	return m.fetchModels(ctx)
}

// RefreshModels is the cache-bust path: it always re-runs
// `opencode models` and replaces the cached copy on success. Use it
// when the operator just authenticated a provider or changed plans and
// must not wait out modelCacheTTL for the new catalogue to appear.
func (m *Manager) RefreshModels(ctx context.Context) ([]string, error) {
	return m.fetchModels(ctx)
}

// fetchModels runs `opencode models` and replaces the cache.
func (m *Manager) fetchModels(ctx context.Context) ([]string, error) {
	m.mu.Lock()
	binary := m.binaryPath
	m.mu.Unlock()

	if binary == "" {
		resolved, err := m.runner.LookPath("opencode")
		if err != nil {
			return nil, ErrNotInstalled
		}
		binary = resolved
	}

	out, err := m.runner.Output(ctx, binary, "models")
	if err != nil {
		return nil, fmt.Errorf("opencode models: %w", err)
	}
	models := parseModelLines(out)

	m.mu.Lock()
	m.cacheModels = models
	m.cacheAt = time.Now()
	m.mu.Unlock()

	dup := make([]string, len(models))
	copy(dup, models)
	return dup, nil
}

// CacheAge returns how long ago ListModels last refreshed the cache.
// Zero when the cache is empty. Used by the HTTP handler to surface
// `cached: true|false` to the dashboard.
func (m *Manager) CacheAge() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cacheAt.IsZero() {
		return 0
	}
	return time.Since(m.cacheAt)
}

// parseModelLines scans stdout and returns one model name per
// non-blank line. Whitespace is trimmed; blank lines are skipped.
func parseModelLines(out []byte) []string {
	var models []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		models = append(models, line)
	}
	return models
}
