package cache

import "testing"

// releasableLister is a mockLister that also records ReleaseSession calls,
// standing in for the downstream Manager which owns per-session instances.
type releasableLister struct {
	mockLister
	released []string
}

func (r *releasableLister) ReleaseSession(sessionID string) {
	r.released = append(r.released, sessionID)
}

func TestCachingLister_ReleaseSession_ForwardsToInner(t *testing.T) {
	inner := &releasableLister{}
	cl := NewCachingToolLister(inner, NewToolCache(nil))

	cl.ReleaseSession("sess-1")

	if len(inner.released) != 1 || inner.released[0] != "sess-1" {
		t.Fatalf("forwarded releases = %v, want [sess-1]", inner.released)
	}
}

func TestCachingLister_ReleaseSession_NoopWhenInnerLacksMethod(t *testing.T) {
	// A bare ToolLister with no ReleaseSession must not panic — the wrapper
	// type-asserts and silently skips, matching the disconnect path's
	// optional SessionReleaser contract.
	cl := NewCachingToolLister(&mockLister{}, NewToolCache(nil))
	cl.ReleaseSession("sess-1")
}
