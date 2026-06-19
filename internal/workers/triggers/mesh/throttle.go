package mesh

import (
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// throttleKey is the bucket identity for per-(trigger, source) throttling.
// Different peers don't share a bucket — source_key = agent_name|peer_id.
func throttleKey(msg *store.MeshMessage, srcPeer string) string {
	source := srcPeer
	if source == "" {
		source = msg.AgentName
	}
	if source == "" {
		source = msg.SessionID
	}
	return source
}

// tryReserveThrottle returns true and bumps the bucket timestamp when
// the (trigger, source) pair is past its throttle window; returns false
// without mutating state when still in the window.
//
// throttleSeconds<=0 disables throttling for the trigger — every match
// fires. That's a deliberate (admin-validated) escape hatch for tests +
// the rare zero-throttle workflow.
func (d *Dispatcher) tryReserveThrottle(
	triggerID, sourceKey string, throttleSeconds int,
) bool {
	if throttleSeconds <= 0 {
		return true
	}
	key := triggerID + "|" + sourceKey
	now := d.clock.Now()
	window := time.Duration(throttleSeconds) * time.Second
	d.throttleMu.Lock()
	defer d.throttleMu.Unlock()
	if last, ok := d.throttle[key]; ok {
		if now.Sub(last) < window {
			return false
		}
	}
	d.throttle[key] = now
	// Periodically prune old entries — every 64th admit, evict anything
	// older than 24h so the map can't grow unbounded across uptime.
	if len(d.throttle)%64 == 0 {
		cutoff := now.Add(-24 * time.Hour)
		for k, v := range d.throttle {
			if v.Before(cutoff) {
				delete(d.throttle, k)
			}
		}
	}
	return true
}
