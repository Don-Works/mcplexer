package escalate

// channel_health_persist.go — the durable half of channel health.
//
// channel_health.go decides WHEN a route is broken. This file records WHAT it
// decided somewhere an operator can actually reach. The split matters: the
// in-memory run drives the log cadence and dies with the process, which is why
// a webhook that had been dead for six days looked brand new after a restart
// and started its three-strike count again from zero. The row does not.
//
// The two are allowed to disagree, and the disagreement is meaningful. After a
// restart the in-memory run is 0 while the stored run may be 47; memory is
// asking "should I log about this again right now", the row is answering "has
// this route been delivering". Reconciling them would mean reading the DB on
// every send to decide whether to log, which buys nothing and puts a query on
// the delivery path.

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/logwatch/distill"
	"github.com/don-works/mcplexer/internal/store"
)

// channelBrokenThreshold is pinned to the store's constant so the ERROR log and
// the API cannot disagree about the same channel. See channel_health_test.go.
const channelBrokenThreshold = store.ChannelBrokenThreshold

// ChannelHealthRecorder persists per-route delivery health. It is the narrow
// slice of store.MonitoringStore the dispatcher needs, kept separate from
// Dispatcher.Store so that registering it is optional: a dispatcher without a
// recorder behaves exactly as before rather than failing to construct.
type ChannelHealthRecorder interface {
	RecordMonitoringChannelFailure(ctx context.Context, id string, at time.Time, reason string) error
	RecordMonitoringChannelSuccess(ctx context.Context, id string, at time.Time) error
	RecordMonitoringChannelTargeted(ctx context.Context, ids []string, at time.Time) error
}

// recordChannelTargeting marks every route eligible for this notification as
// owed a message, BEFORE the throttle has had a say.
//
// Placement is the entire point. deliverChannels runs after prepareNotification,
// so when the workspace hourly cap is spent no channel is consulted and nothing
// downstream of the throttle observes anything at all. On 2026-07-14 the budget
// was spent by OTHER traffic in the workspace — a dead route never succeeds, so
// it never writes a throttle mark, so it never suppresses itself — and the
// broken webhook simply stopped being attempted. Its failure count froze at one
// while it was owed 191 more notifications.
//
// Recording eligibility here makes that visible: the route accrues the debt
// whether or not the throttle lets anyone try to pay it.
//
// Eligibility mirrors deliverChannels exactly (enabled, and severity at or above
// the channel's floor). It has to: a channel counted here but skipped there
// would accrue a debt it can never clear and eventually report broken while
// working perfectly.
func (d *Dispatcher) recordChannelTargeting(ctx context.Context, n distill.Notification) {
	recorder := d.healthRecorder()
	if recorder == nil {
		return
	}
	channels, err := d.store.ListMonitoringChannels(ctx, n.WorkspaceID)
	if err != nil {
		// Non-fatal: the delivery path lists channels again and reports the
		// error properly there. Failing the notification here would let a
		// transient store blip cost an alert.
		slog.Warn("escalate: could not record channel targeting",
			"workspace", n.WorkspaceID, "error", err)
		return
	}
	rank := store.SeverityRank(n.Severity)
	ids := make([]string, 0, len(channels))
	for _, c := range channels {
		if !c.Enabled || store.SeverityRank(c.MinSeverity) > rank || c.ID == "" {
			continue
		}
		ids = append(ids, c.ID)
	}
	if len(ids) == 0 {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), channelHealthWriteTimeout)
	defer cancel()
	if err := recorder.RecordMonitoringChannelTargeted(writeCtx, ids, d.now()); err != nil {
		slog.Warn("escalate: could not record channel targeting",
			"workspace", n.WorkspaceID, "channels", len(ids), "error", err)
	}
}

// RegisterChannelHealthRecorder wires durable channel health. Optional; without
// it health is logged but not queryable.
func (d *Dispatcher) RegisterChannelHealthRecorder(r ChannelHealthRecorder) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.channelHealthRecorder = r
}

func (d *Dispatcher) healthRecorder() ChannelHealthRecorder {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.channelHealthRecorder
}

// persistChannelFailure records one failed delivery against the channel row.
//
// Errors are logged and swallowed. This is bookkeeping about a delivery that
// has already failed; turning a bookkeeping error into a delivery error would
// let a database hiccup mark a healthy route failed, and the caller has nothing
// useful to do with it either way.
func (d *Dispatcher) persistChannelFailure(
	ctx context.Context, channel *store.MonitoringChannel, reason string, err error,
) {
	recorder := d.healthRecorder()
	if recorder == nil || channel == nil || channel.ID == "" {
		return
	}
	// WithoutCancel because the commonest failure reason IS a cancelled or
	// timed-out context. Recording health on the caller's ctx would drop
	// exactly the failures most worth recording — a shutdown mid-send, or the
	// 10s webhook timeout — and leave the route looking healthy.
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), channelHealthWriteTimeout)
	defer cancel()
	if wErr := recorder.RecordMonitoringChannelFailure(
		writeCtx, channel.ID, d.now(), failureReason(reason, err),
	); wErr != nil {
		slog.Warn("escalate: could not persist channel failure",
			"channel", channel.Name, "kind", channel.Kind, "error", wErr)
	}
}

// persistChannelSuccess clears the stored failure run and stamps the delivery.
// Called on every success, not only on recovery: last_success_at is the field
// an operator reads first ("when did this last actually work?") and it is only
// truthful if every delivery writes it.
func (d *Dispatcher) persistChannelSuccess(ctx context.Context, channel *store.MonitoringChannel) {
	recorder := d.healthRecorder()
	if recorder == nil || channel == nil || channel.ID == "" {
		return
	}
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), channelHealthWriteTimeout)
	defer cancel()
	if err := recorder.RecordMonitoringChannelSuccess(
		writeCtx, channel.ID, d.now(),
	); err != nil {
		slog.Warn("escalate: could not persist channel success",
			"channel", channel.Name, "kind", channel.Kind, "error", err)
	}
}

// channelHealthWriteTimeout bounds the health write so a wedged database cannot
// stall the delivery path it is only observing.
const channelHealthWriteTimeout = 5 * time.Second

// failureReason builds the stored reason. The store redacts and truncates it;
// this only decides what is worth saying. The underlying error is included
// because "delivery failed" alone cannot distinguish a 400 bad token from a
// DNS failure, and that distinction is the whole diagnosis.
func failureReason(reason string, err error) string {
	if err == nil {
		return reason
	}
	return reason + ": " + err.Error()
}
