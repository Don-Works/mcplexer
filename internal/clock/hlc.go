// Package clock implements a Hybrid Logical Clock (HLC) used by the
// task-gossip layer to order mutations across machines without relying
// on a perfectly synchronized wall clock.
//
// An HLC stamp is a (wall_ms, counter) pair. The wall component is the
// caller's local Unix millisecond timestamp; the counter increments when
// two stamps fall in the same millisecond so successive Now() calls are
// strictly increasing even under burst-write load.
//
// We render stamps as a fixed-width hex string `%016x%016x` (32 chars,
// no separator) so they sort lexicographically — handy for SQLite
// ORDER BY and watermark comparisons that just need string-min/max.
// Callers SHOULD treat the string as opaque; the (wall, counter) split
// is an implementation detail.
//
// This package is goroutine-safe and pure stdlib — no test fixtures or
// global init beyond a single sync.Mutex around the monotonic counter.
package clock

import (
	"fmt"
	"sync"
	"time"
)

// Clock generates monotonically-increasing HLC stamps. Each instance
// owns its own counter; two clocks in the same process produce
// independent sequences. Most callers use the package-level Now() which
// is backed by a shared default clock.
type Clock struct {
	mu      sync.Mutex
	lastMs  int64
	counter uint16
	// nowFn is the wall-clock source, swappable for tests.
	nowFn func() time.Time
}

// New returns a fresh Clock seeded from the real wall clock.
func New() *Clock {
	return &Clock{nowFn: func() time.Time { return time.Now().UTC() }}
}

// NewWithSource returns a Clock that calls fn for wall time. Tests
// inject a frozen-or-monotonic source to assert tie-break behaviour
// without sleeping.
func NewWithSource(fn func() time.Time) *Clock {
	if fn == nil {
		fn = func() time.Time { return time.Now().UTC() }
	}
	return &Clock{nowFn: fn}
}

// Now returns the next HLC stamp. Strictly greater than the previous
// return value from this Clock — when the wall clock advances, the
// counter resets to zero; when two calls hit the same millisecond, the
// counter increments.
//
// If the wall clock goes BACKWARDS (NTP step, manual clock change), we
// hold lastMs and keep incrementing the counter so monotonicity is
// preserved at the cost of a brief HLC drift from wall time. The drift
// catches up when wall_ms > lastMs again.
//
// Returns a 32-char lowercase hex string `%016x%016x` (wall, counter).
func (c *Clock) Now() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	ms := c.nowFn().UnixNano() / int64(time.Millisecond)
	if ms <= c.lastMs {
		// Same ms (or wall went backwards) — bump counter.
		// uint16 wraps at 65535 which is plenty headroom for a single
		// millisecond; if the caller really burns through 65k+ stamps
		// in one ms we fall through to bump lastMs instead.
		if c.counter == ^uint16(0) {
			c.lastMs++
			c.counter = 0
		} else {
			c.counter++
		}
	} else {
		c.lastMs = ms
		c.counter = 0
	}
	return Format(c.lastMs, c.counter)
}

// Format builds the canonical 32-char hex representation. Exported so
// tests + the SQLite backfill can reconstruct a stamp from a known
// (wall_ms, counter) pair.
func Format(wallMs int64, counter uint16) string {
	// %016x on an int64 prints the two's-complement; we never pass
	// negative values, so this stays correct.
	return fmt.Sprintf("%016x%016x", uint64(wallMs), uint64(counter))
}

// Parse splits a hex stamp back into its (wall_ms, counter) pair.
// Returns an error if the string isn't exactly 32 lower-hex chars.
func Parse(s string) (wallMs int64, counter uint16, err error) {
	if len(s) != 32 {
		return 0, 0, fmt.Errorf("hlc: parse: expected 32-char hex, got %d", len(s))
	}
	var wall uint64
	var ctr uint64
	if _, err := fmt.Sscanf(s[:16], "%016x", &wall); err != nil {
		return 0, 0, fmt.Errorf("hlc: parse wall: %w", err)
	}
	if _, err := fmt.Sscanf(s[16:], "%016x", &ctr); err != nil {
		return 0, 0, fmt.Errorf("hlc: parse counter: %w", err)
	}
	return int64(wall), uint16(ctr), nil
}

// Observe merges a remote HLC stamp into this clock — the standard HLC
// receive rule. After Observe(remote) returns, the next Now() from this
// Clock is strictly greater than BOTH the remote stamp and every stamp
// previously issued locally, so a peer with a fast wall clock cannot
// permanently win last-writer-wins conflicts: one Observe and our
// subsequent local writes stamp ahead of it.
//
// Mechanics: lastMs/counter become max((local_wall, local_counter),
// (remote_wall, remote_counter)) under lexical (wall, counter) order.
// The wall-vs-now merge happens implicitly on the next Now() call —
// when real wall time has passed the held stamp, Now() resets to wall
// time; until then it increments the counter, preserving monotonicity.
//
// Malformed stamps (not 32-char hex) return the Parse error and leave
// the clock untouched — receivers treat that as best-effort and move on
// (the LWW comparison still runs on the raw string).
func (c *Clock) Observe(remote string) error {
	wall, counter, err := Parse(remote)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if wall > c.lastMs || (wall == c.lastMs && counter > c.counter) {
		c.lastMs = wall
		c.counter = counter
	}
	return nil
}

// Default is the package-level Clock. Most callers use Now() rather than
// constructing their own — the HLC contract is process-wide
// monotonicity, not per-Clock.
var Default = New()

// Now is the package-level shortcut.
func Now() string { return Default.Now() }

// Observe is the package-level shortcut onto Default.
func Observe(remote string) error { return Default.Observe(remote) }
