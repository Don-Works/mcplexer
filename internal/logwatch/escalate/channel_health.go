package escalate

// channel_health.go — per-channel delivery health, tracked ACROSS notifications.
//
// A channel that fails every attempt is not "throttled", it is broken, and the
// dispatcher's own suppression mechanisms actively hide the difference: the
// workspace hourly cap and the per-template cooldown withhold a whole
// notification before any channel is consulted, so a dead route is never
// retried and never logs again. That is how a gchat webhook rejecting every
// message with HTTP 400 sat unnoticed for six days after logging "send failed"
// exactly once — the failure happened once, the suppression happened 191 times,
// and only the suppression was visible.
//
// Health therefore cannot be inferred from any single notification. It is a run
// of consecutive outcomes for one route, reported on the transition into broken
// and then on a cadence, so a route dead for days cannot hide in the gaps.

import (
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

const (
	// channelUnhealthyThreshold is the run of consecutive failures at which a
	// route stops being a blip and starts being broken. Three survives a
	// transient endpoint wobble; a permanent rejection (bad token, wrong URL)
	// reaches it on the third attempt whatever the throttle is doing.
	//
	// Pinned to the store's constant so this ERROR log and the API's
	// `broken` field can never
	// disagree about the same channel — an API reporting healthy while the log
	// reports broken is worse than either surface being wrong alone.
	channelUnhealthyThreshold = channelBrokenThreshold
	// channelUnhealthyReportInterval re-states a broken route on a cadence
	// rather than once. Once is what let six days pass in silence; every
	// attempt would flood the log and get itself ignored.
	channelUnhealthyReportInterval = time.Hour
	maxTrackedChannels             = 4096
)

// channelHealth is one route's consecutive-failure run.
type channelHealth struct {
	consecutiveFailures int
	firstFailureAt      time.Time
	lastReportedAt      time.Time
}

// channelHealthKey identifies a route. The channel's ID is its stable identity:
// a route renamed in the UI is the same route, and its failure run must survive
// the rename — a run that resets reads as recovery, which is the exact opposite
// of the truth for a route that is still broken. ID is also what a persisted
// health row keys on, so tracking anything else would leave the in-memory and
// stored views to diverge. The composite fallback covers channels not loaded
// from the store, which carry no ID.
func channelHealthKey(workspaceID string, channel *store.MonitoringChannel) string {
	if channel.ID != "" {
		return channel.ID
	}
	return workspaceID + "/" + channel.Kind + "/" + channel.Name
}

// recordChannelFailure extends the failure run and reports whether this one is
// worth escalating — true on crossing the threshold and once per interval
// after, so a persistently broken route keeps saying so.
func (d *Dispatcher) recordChannelFailure(key string) (channelHealth, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if len(d.channelHealth) >= maxTrackedChannels {
		slog.Warn("escalate: channel-health map reset at cap", "cap", maxTrackedChannels)
		d.channelHealth = map[string]channelHealth{}
	}
	health := d.channelHealth[key]
	health.consecutiveFailures++
	if health.firstFailureAt.IsZero() {
		health.firstFailureAt = now
	}
	report := health.consecutiveFailures >= channelUnhealthyThreshold &&
		(health.lastReportedAt.IsZero() ||
			now.Sub(health.lastReportedAt) >= channelUnhealthyReportInterval)
	if report {
		health.lastReportedAt = now
	}
	d.channelHealth[key] = health
	return health, report
}

// recordChannelSuccess clears the run and reports whether the route had been
// declared broken — a recovery worth stating, as opposed to the ordinary case
// of a route that was never in trouble.
func (d *Dispatcher) recordChannelSuccess(key string) (channelHealth, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	health, tracked := d.channelHealth[key]
	if !tracked {
		return channelHealth{}, false
	}
	delete(d.channelHealth, key)
	return health, health.consecutiveFailures >= channelUnhealthyThreshold
}

// failingFor is how long this route has been failing without interruption.
func (h channelHealth) failingFor(now time.Time) time.Duration {
	if h.firstFailureAt.IsZero() {
		return 0
	}
	return now.Sub(h.firstFailureAt)
}
