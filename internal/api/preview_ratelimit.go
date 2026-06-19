package api

import (
	"sync"
	"time"
)

// previewRateLimiter is a tiny per-remote-address token bucket. Real
// production rate-limiting belongs in middleware; for the addon preview
// pane the goal is just to stop a stuck UI from hammering target APIs.
type previewRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*previewBucket
	rate    int           // tokens per window
	window  time.Duration // bucket reset interval
}

type previewBucket struct {
	tokens int
	reset  time.Time
}

// newPreviewRateLimiter caps each remote at rate calls per window.
func newPreviewRateLimiter(rate int, window time.Duration) *previewRateLimiter {
	return &previewRateLimiter{
		buckets: make(map[string]*previewBucket),
		rate:    rate,
		window:  window,
	}
}

// allow returns true if the caller is under the per-window quota. It's
// intentionally lock-coarse — the volumes here are tiny.
func (l *previewRateLimiter) allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[key]
	if !ok || now.After(b.reset) {
		l.buckets[key] = &previewBucket{tokens: l.rate - 1, reset: now.Add(l.window)}
		return true
	}
	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}
