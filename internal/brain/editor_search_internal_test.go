package brain

import (
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// TestTier exercises the three-tier classifier: exact-prefix beats token
// beats fuzzy, and the fuzzy tier is suppressed past the scale cliff.
func TestTier(t *testing.T) {
	cases := []struct {
		name     string
		title    string // already lowercased, as Search passes it
		query    string
		fuzzyOff bool
		want     int
	}{
		{"exact prefix", "re-arm worker cron", "re-arm", false, searchTierExact},
		{"token boundary", "fix the scheduler bug", "sched", false, searchTierToken},
		{"substring fuzzy", "denoise telegram footer", "noise", false, searchTierFuzzy},
		{"fuzzy dropped at scale", "denoise telegram footer", "noise", true, -1},
		{"no match", "spec the brain", "kafka", false, -1},
		{"empty query", "anything", "", false, -1},
		{"prefix wins over token", "spec spec", "spec", false, searchTierExact},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tier(tc.title, tc.query, tc.fuzzyOff); got != tc.want {
				t.Fatalf("tier(%q,%q,fuzzyOff=%v) = %d, want %d", tc.title, tc.query, tc.fuzzyOff, got, tc.want)
			}
		})
	}
}

// TestFrecency verifies the intra-tier rank ordering: a better tier always
// outranks a worse one, and within a tier a fresher record outranks a stale
// one (recency boost), but never enough to cross a tier boundary.
func TestFrecency(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	fresh := now.Add(-1 * time.Hour)
	stale := now.Add(-30 * 24 * time.Hour)

	exactStale := frecency(searchTierExact, stale, now)
	tokenFresh := frecency(searchTierToken, fresh, now)
	if exactStale <= tokenFresh {
		t.Fatalf("exact tier (even stale, %.2f) must outrank token tier (even fresh, %.2f)", exactStale, tokenFresh)
	}

	tokenFreshScore := frecency(searchTierToken, fresh, now)
	tokenStaleScore := frecency(searchTierToken, stale, now)
	if tokenFreshScore <= tokenStaleScore {
		t.Fatalf("within a tier a fresher record must outrank a stale one: fresh %.2f <= stale %.2f", tokenFreshScore, tokenStaleScore)
	}

	zero := frecency(searchTierFuzzy, time.Time{}, now)
	if zero < 0 {
		t.Fatalf("zero updated_at must still yield a non-negative base, got %.2f", zero)
	}
}

// TestFtsWords verifies user keystrokes become safe bare space-separated
// words for the store to escape (special chars dropped, tokenizer boundaries
// respected), never a raw MATCH expression that could be a syntax error.
func TestFtsWords(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"re-arm cron", "re arm cron"},
		{"  spaced  out ", "spaced out"},
		{`"quoted" (paren)`, "quoted paren"},
		{"", ""},
		{"!!!", ""},
	}
	for _, tc := range cases {
		if got := ftsWords(tc.in); got != tc.want {
			t.Fatalf("ftsWords(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestTaskHoldsLease covers the live-lease shimmer predicate.
func TestTaskHoldsLease(t *testing.T) {
	now := time.Date(2026, 6, 3, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Minute)
	past := now.Add(-time.Minute)
	mk := func(assignee string, lease *time.Time) bool {
		return taskHoldsLease(&store.Task{AssigneeSessionID: assignee, LeaseExpiresAt: lease}, now)
	}
	if mk("sess_a", &future) != true {
		t.Fatal("assignee + future lease must be live")
	}
	if mk("sess_a", &past) != false {
		t.Fatal("expired lease must not be live")
	}
	if mk("", &future) != false {
		t.Fatal("no assignee must not be live even with a future lease")
	}
	if mk("sess_a", nil) != false {
		t.Fatal("nil lease must not be live")
	}
}
