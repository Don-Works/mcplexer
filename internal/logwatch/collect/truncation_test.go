package collect

import (
	"testing"
	"time"
)

func TestTruncationEpisodeStableUntilCompletePull(t *testing.T) {
	clock := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	m := &Manager{truncated: map[string]string{}, now: func() time.Time { return clock }}
	first := m.truncationEpisode("source-1", true)
	clock = clock.Add(time.Minute)
	if repeat := m.truncationEpisode("source-1", true); repeat != first {
		t.Fatalf("continuous truncation changed episode: %q → %q", first, repeat)
	}
	m.truncationEpisode("source-1", false)
	if rearmed := m.truncationEpisode("source-1", true); rearmed == first {
		t.Fatalf("post-recovery truncation did not re-arm: %q", rearmed)
	}
}
