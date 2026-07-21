package memory

import (
	"context"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// defaultDigestDebounce is how long after the first dirtying save the
// digest is regenerated. Subsequent saves within the window coalesce.
const defaultDigestDebounce = 30 * time.Second

// scheduleDigest marks the scope ("" = global) dirty and arms the flush
// timer if it is not already running. Cheap; safe to call on every write.
func (s *Service) scheduleDigest(workspaceID *string) {
	if s.digest == nil {
		return
	}
	scope := ptrOr(workspaceID, "")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.digestPending == nil {
		s.digestPending = make(map[string]struct{})
	}
	s.digestPending[scope] = struct{}{}
	if s.digestTimer == nil {
		delay := s.digestDelay
		if delay <= 0 {
			delay = defaultDigestDebounce
		}
		s.digestTimer = time.AfterFunc(delay, s.flushDigests)
	}
}

// flushDigests drains the pending scope set and regenerates one digest
// per scope. Uses a background context because the saves that scheduled
// the flush have long since returned.
func (s *Service) flushDigests() {
	s.mu.Lock()
	scopes := s.digestPending
	s.digestPending = nil
	if s.digestTimer != nil {
		s.digestTimer.Stop()
		s.digestTimer = nil
	}
	s.mu.Unlock()
	if s.digest == nil || len(scopes) == 0 {
		return
	}
	ctx := context.Background()
	for scope := range scopes {
		s.writeDigestScope(ctx, scope)
	}
}

// writeDigestScope renders and persists the digest for one scope.
// Best-effort: list/write failures are swallowed because the digest is a
// derived cache.
func (s *Service) writeDigestScope(ctx context.Context, scope string) {
	sc := store.SkillScope{}
	if scope != "" {
		sc.WorkspaceIDs = []string{scope}
	}
	entries, err := s.store.ListMemories(ctx, store.MemoryFilter{
		Scope: sc, Limit: digestMaxEntries,
	})
	if err != nil {
		return
	}
	_ = s.digest.Write(ctx, scope, entries)
}

// SetDigestDebounceForTest overrides the debounce window. Test hook only.
func (s *Service) SetDigestDebounceForTest(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.digestDelay = d
}

// FlushDigestsForTest forces a synchronous flush of every pending scope
// so tests can assert digest content without sleeping out the debounce.
func (s *Service) FlushDigestsForTest() {
	s.flushDigests()
}
