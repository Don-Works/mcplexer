package gateway

import (
	"strings"
	"sync"
	"time"
)

const (
	errorTrackerWindow    = 10 * time.Minute
	errorTrackerThreshold = 3
)

// errorEntry records a single error with its timestamp and message.
type errorEntry struct {
	at  time.Time
	msg string
}

// errorTracker detects when a client is struggling with repeated errors
// within a sliding time window. When the threshold is exceeded, callers
// can append pattern-specific guidance to the error message.
type errorTracker struct {
	mu      sync.Mutex
	entries []errorEntry
}

// RecordError records a tool call error and returns true if the error count
// within the window has reached the threshold.
func (t *errorTracker) RecordError(msg string) bool {
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()

	t.entries = append(t.entries, errorEntry{at: now, msg: msg})
	t.prune(now)

	return len(t.entries) >= errorTrackerThreshold
}

// RecordSuccess resets the error tracker on a successful tool call.
func (t *errorTracker) RecordSuccess() {
	t.mu.Lock()
	t.entries = t.entries[:0]
	t.mu.Unlock()
}

// Guidance returns pattern-specific guidance based on recent error messages.
func (t *errorTracker) Guidance() string {
	t.mu.Lock()
	msgs := make([]string, len(t.entries))
	for i, e := range t.entries {
		msgs[i] = e.msg
	}
	t.mu.Unlock()

	combined := strings.Join(msgs, " ")
	lower := strings.ToLower(combined)

	switch {
	case containsAny(lower, "read-only", "read only", "permission denied", "cannot execute delete", "cannot execute update", "cannot execute insert"):
		return " This tool only supports SELECT queries. Do not use INSERT, UPDATE, or DELETE."
	case containsAny(lower, "expected object", "expected array", "cannot unmarshal string", "invalid type"):
		return " Check parameter types — pass objects/arrays, not stringified JSON."
	case containsAny(lower, "syntax", "column", "relation", "no such", "does not exist"):
		return " Query information_schema.columns to discover table and column names before writing SQL queries."
	default:
		return " Multiple consecutive errors detected. Check server config or file a report."
	}
}

// containsAny returns true if s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// prune removes entries older than the window. Must be called with mu held.
func (t *errorTracker) prune(now time.Time) {
	cutoff := now.Add(-errorTrackerWindow)
	i := 0
	for i < len(t.entries) && t.entries[i].at.Before(cutoff) {
		i++
	}
	if i > 0 {
		t.entries = t.entries[i:]
	}
}
