// monitoring_channel_health.go — sqlite impl of the channel delivery-health
// slice of store.MonitoringStore (migration 148).
//
// Both writes are single UPDATE statements that derive the new state from the
// stored one. That is not a micro-optimisation: the dispatcher fans out to
// channels concurrently with the renotify sweep, and a read-modify-write of
// consecutive_failures across two statements loses increments under exactly the
// burst of failures that means a route has died. The counter has to be right
// precisely when it is moving fastest.
package sqlite

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// RecordMonitoringChannelFailure extends the channel's current failure run.
//
// first_failure_at uses COALESCE so it marks the start of the RUN, not the most
// recent failure: it is set by the first failure after a success and then left
// alone until the next success clears it. That is what makes "broken since
// 06:12" survive the 191 subsequent attempts.
func (d *DB) RecordMonitoringChannelFailure(
	ctx context.Context, id string, at time.Time, reason string,
) error {
	if id == "" {
		return errors.New("RecordMonitoringChannelFailure: id required")
	}
	ts := formatTime(at.UTC())
	res, err := d.q.ExecContext(ctx, `
		UPDATE monitoring_channels SET
			consecutive_failures = consecutive_failures + 1,
			first_failure_at = COALESCE(first_failure_at, ?),
			last_failure_at = ?,
			last_error = ?,
			updated_at = ?
		WHERE id = ?`,
		ts, ts, store.RedactChannelError(reason), ts, id)
	if err != nil {
		return fmt.Errorf("record monitoring channel failure: %w", err)
	}
	return requireRowAffected(res, store.ErrMonitoringChannelNotFound)
}

// RecordMonitoringChannelSuccess clears the failure run and stamps the delivery.
//
// It clears last_error and first_failure_at as well as the counter. A recovered
// route that keeps its old error text reads as broken to every human who scans
// the list, which is the same failure this work exists to fix, pointed the
// other way: a channel that recovers must stop looking dead. last_failure_at is
// deliberately KEPT — "working now, last failed on the 14th" is useful history
// and, unlike the run state, it does not misrepresent the present.
func (d *DB) RecordMonitoringChannelSuccess(ctx context.Context, id string, at time.Time) error {
	if id == "" {
		return errors.New("RecordMonitoringChannelSuccess: id required")
	}
	ts := formatTime(at.UTC())
	res, err := d.q.ExecContext(ctx, `
		UPDATE monitoring_channels SET
			consecutive_failures = 0,
			targeted_since_success = 0,
			first_failure_at = NULL,
			last_error = '',
			last_success_at = ?,
			updated_at = ?
		WHERE id = ?`,
		ts, ts, id)
	if err != nil {
		return fmt.Errorf("record monitoring channel success: %w", err)
	}
	return requireRowAffected(res, store.ErrMonitoringChannelNotFound)
}

// RecordMonitoringChannelTargeted marks each channel as owed one notification.
//
// This runs before the throttle, on every notification, so it is the one health
// input a suppression decision cannot erase. It is a single statement over the
// whole eligible set: fanning out one UPDATE per channel would put N round
// trips on the notification path, and a partial failure would leave some routes
// counted and others not — skewing exactly the comparison health is derived
// from.
//
// A missing row is deliberately NOT an error here, unlike the failure and
// success writes. Those are told about one specific channel the dispatcher just
// used; this one is handed a set that was read moments earlier and may have had
// a channel deleted from under it. A concurrent delete is normal operation, not
// a wiring bug, and must not fail a notification.
func (d *DB) RecordMonitoringChannelTargeted(
	ctx context.Context, ids []string, at time.Time,
) error {
	if len(ids) == 0 {
		return nil
	}
	ts := formatTime(at.UTC())
	args := make([]any, 0, len(ids)+2)
	args = append(args, ts, ts)
	for _, id := range ids {
		args = append(args, id)
	}
	_, err := d.q.ExecContext(ctx, `
		UPDATE monitoring_channels SET
			targeted_since_success = targeted_since_success + 1,
			last_targeted_at = ?,
			updated_at = ?
		WHERE id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return fmt.Errorf("record monitoring channel targeted: %w", err)
	}
	return nil
}
