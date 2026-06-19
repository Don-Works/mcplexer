package brain

import (
	"sync"
	"time"
)

// selfWriteTTL bounds how long a recorded self-write hash stays
// recognisable. A debounced fsnotify event for an outbound write fires
// well within this window (sub-second to a few seconds); the TTL only
// exists so a long-lived process never accumulates stale entries for
// files that were later edited by a human (whose change must NOT be
// suppressed).
const selfWriteTTL = 30 * time.Second

// selfWriteSet records the (path, sha) pairs the serializer has just
// written so the resulting fsnotify event is recognised as
// self-induced and skipped, preventing a write→index→write loop
// (docs/brain.md §6 outbound step 4).
//
// Entries are TTL-evicted: IsSelf only matches a hash recorded within
// selfWriteTTL, and a matching consume removes the entry so a genuine
// later human edit producing the same bytes is not perpetually
// suppressed.
type selfWriteSet struct {
	mu  sync.Mutex
	m   map[string]selfWriteEntry
	now func() time.Time // injectable for tests
}

type selfWriteEntry struct {
	sha string
	at  time.Time
}

// newSelfWriteSet constructs an empty set using the real clock.
func newSelfWriteSet() *selfWriteSet {
	return &selfWriteSet{m: make(map[string]selfWriteEntry), now: time.Now}
}

// Mark records that path was just written with content hashing to sha.
func (s *selfWriteSet) Mark(path, sha string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked()
	s.m[path] = selfWriteEntry{sha: sha, at: s.now()}
}

// IsSelf reports whether (path, sha) matches a recent self-write and,
// when it does, consumes the entry so a subsequent identical event from
// a genuine external edit is not suppressed.
func (s *selfWriteSet) IsSelf(path, sha string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictLocked()
	e, ok := s.m[path]
	if !ok {
		return false
	}
	if e.sha != sha {
		return false
	}
	delete(s.m, path)
	return true
}

// evictLocked drops entries older than the TTL. Caller holds s.mu.
func (s *selfWriteSet) evictLocked() {
	cutoff := s.now().Add(-selfWriteTTL)
	for p, e := range s.m {
		if e.at.Before(cutoff) {
			delete(s.m, p)
		}
	}
}
