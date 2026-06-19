package clock

import (
	"sort"
	"sync"
	"testing"
	"time"
)

// TestNowMonotonic asserts that 1000 rapid Now() calls strictly
// increase even when they all fall in the same millisecond. This is the
// core HLC contract — without it the gossip watermark (max(hlc_at) per
// workspace) would skip events.
func TestNowMonotonic(t *testing.T) {
	c := New()
	const n = 1000
	stamps := make([]string, n)
	for i := 0; i < n; i++ {
		stamps[i] = c.Now()
	}
	for i := 1; i < n; i++ {
		if stamps[i] <= stamps[i-1] {
			t.Fatalf("stamp %d (%q) <= stamp %d (%q) — HLC broke monotonicity",
				i, stamps[i], i-1, stamps[i-1])
		}
	}
}

// TestNowMonotonicFrozenWall pins behaviour when the wall clock never
// advances — the counter alone must keep stamps increasing. This is the
// exact code path the production hot loop hits when many mutations
// happen in the same millisecond.
func TestNowMonotonicFrozenWall(t *testing.T) {
	t0 := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	c := NewWithSource(func() time.Time { return t0 })
	const n = 5000
	stamps := make([]string, n)
	for i := 0; i < n; i++ {
		stamps[i] = c.Now()
	}
	for i := 1; i < n; i++ {
		if stamps[i] <= stamps[i-1] {
			t.Fatalf("frozen-wall stamp %d (%q) <= %d (%q)",
				i, stamps[i], i-1, stamps[i-1])
		}
	}
}

// TestNowMonotonicClockSkew pins behaviour when the wall clock JUMPS
// BACKWARDS (NTP step). Monotonicity must survive — at the cost of HLC
// briefly drifting from wall time.
func TestNowMonotonicClockSkew(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	c := NewWithSource(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	})
	first := c.Now()
	// Step the wall clock backwards by 5 seconds — simulates an NTP
	// correction.
	mu.Lock()
	now = now.Add(-5 * time.Second)
	mu.Unlock()
	second := c.Now()
	if second <= first {
		t.Fatalf("post-skew stamp %q must exceed pre-skew %q", second, first)
	}
}

// TestNowConcurrent asserts the Clock is goroutine-safe and that no
// concurrent caller observes a duplicate stamp. Each goroutine collects
// its own slice; the merged set must have unique strings.
func TestNowConcurrent(t *testing.T) {
	c := New()
	const goroutines = 16
	const perG = 500
	out := make(chan []string, goroutines)
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]string, perG)
			for i := 0; i < perG; i++ {
				local[i] = c.Now()
			}
			out <- local
		}()
	}
	wg.Wait()
	close(out)
	all := make([]string, 0, goroutines*perG)
	for batch := range out {
		all = append(all, batch...)
	}
	sort.Strings(all)
	for i := 1; i < len(all); i++ {
		if all[i] == all[i-1] {
			t.Fatalf("duplicate stamp %q at index %d", all[i], i)
		}
	}
}

// TestFormatParseRoundTrip pins the wire format: Format then Parse must
// recover the exact (wall, counter) pair. Gossip relies on the textual
// stamp staying byte-stable across machines.
func TestFormatParseRoundTrip(t *testing.T) {
	cases := []struct {
		wall    int64
		counter uint16
	}{
		{0, 0},
		{1, 0},
		{1748000000000, 0},
		{1748000000000, 12345},
		{1748000000000, ^uint16(0)},
	}
	for _, tc := range cases {
		s := Format(tc.wall, tc.counter)
		if len(s) != 32 {
			t.Fatalf("Format(%d,%d) = %q (len=%d), want 32", tc.wall, tc.counter, s, len(s))
		}
		wall, ctr, err := Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q): %v", s, err)
		}
		if wall != tc.wall || ctr != tc.counter {
			t.Fatalf("Parse round-trip: got (%d,%d), want (%d,%d)",
				wall, ctr, tc.wall, tc.counter)
		}
	}
}

// TestParseRejectsBadInput pins error cases — short strings, non-hex,
// wrong length. Critical for wire safety: a malformed remote stamp must
// not silently parse to (0,0).
func TestParseRejectsBadInput(t *testing.T) {
	cases := []string{
		"",
		"abc",
		// 31 chars
		"0000000000000000000000000000000",
		// 33 chars
		"000000000000000000000000000000000",
		// non-hex
		"zzzzzzzzzzzzzzzz0000000000000000",
	}
	for _, s := range cases {
		if _, _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) returned nil error; expected one", s)
		}
	}
}

// TestObserve pins the HLC receive rule: after Observe(remote), the
// next Now() must be strictly greater than both the remote stamp and
// every previously-issued local stamp — even when the remote wall clock
// runs ahead of ours. Frozen local wall so the cases are deterministic.
func TestObserve(t *testing.T) {
	t0 := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	localMs := t0.UnixNano() / int64(time.Millisecond)
	cases := []struct {
		name    string
		remote  string
		wantErr bool
	}{
		{name: "remote wall ahead", remote: Format(localMs+60_000, 0)},
		{name: "remote wall far ahead with counter", remote: Format(localMs+3_600_000, 999)},
		{name: "remote same wall higher counter", remote: Format(localMs, 500)},
		{name: "remote behind", remote: Format(localMs-60_000, 0)},
		{name: "remote equal to local", remote: ""}, // filled below
		{name: "malformed short", remote: "abc", wantErr: true},
		{name: "malformed non-hex", remote: "zzzzzzzzzzzzzzzz0000000000000000", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := NewWithSource(func() time.Time { return t0 })
			before := c.Now()
			remote := tc.remote
			if tc.name == "remote equal to local" {
				remote = before
			}
			err := c.Observe(remote)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Observe(%q) = nil error, want parse error", remote)
				}
				// Clock must be untouched: next stamp still > before.
				if next := c.Now(); next <= before {
					t.Fatalf("post-error stamp %q <= %q", next, before)
				}
				return
			}
			if err != nil {
				t.Fatalf("Observe(%q): %v", remote, err)
			}
			next := c.Now()
			if next <= remote {
				t.Fatalf("after Observe, Now() %q <= remote %q — fast-clock peer would keep winning LWW", next, remote)
			}
			if next <= before {
				t.Fatalf("after Observe, Now() %q <= prior local %q — local monotonicity broke", next, before)
			}
		})
	}
}

// TestObserveIdempotent confirms repeated Observe of the same stamp
// doesn't keep inflating the clock — only the first merge moves it.
func TestObserveIdempotent(t *testing.T) {
	t0 := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	c := NewWithSource(func() time.Time { return t0 })
	remote := Format(t0.UnixNano()/int64(time.Millisecond)+10_000, 7)
	if err := c.Observe(remote); err != nil {
		t.Fatalf("observe 1: %v", err)
	}
	first := c.Now()
	if err := c.Observe(remote); err != nil {
		t.Fatalf("observe 2: %v", err)
	}
	second := c.Now()
	if second <= first {
		t.Fatalf("re-observe broke monotonicity: %q <= %q", second, first)
	}
	wall1, _, _ := Parse(first)
	wall2, _, _ := Parse(second)
	if wall1 != wall2 {
		t.Fatalf("re-observe of same stamp moved the wall: %d -> %d", wall1, wall2)
	}
}

// TestPackageNowSharedClock confirms the package-level Now() shares the
// Default clock across callers (so it preserves monotonicity).
func TestPackageNowSharedClock(t *testing.T) {
	const n = 500
	stamps := make([]string, n)
	for i := range stamps {
		stamps[i] = Now()
	}
	for i := 1; i < n; i++ {
		if stamps[i] <= stamps[i-1] {
			t.Fatalf("package Now stamp %d (%q) <= %d (%q)",
				i, stamps[i], i-1, stamps[i-1])
		}
	}
}
