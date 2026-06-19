package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// notifyStoreAdapter implements notify.Store on top of the sqlite DB's
// notification methods. The names diverged so other DB-method callers
// don't collide; this adapter narrows the surface to what notify
// consumers actually need.
type notifyStoreAdapter struct {
	db *sqlite.DB
}

func newNotifyStore(db *sqlite.DB) notify.Store {
	return &notifyStoreAdapter{db: db}
}

func (a *notifyStoreAdapter) Insert(ctx context.Context, e notify.Event) (int64, error) {
	return a.db.InsertNotification(ctx, e)
}

func (a *notifyStoreAdapter) List(ctx context.Context, f notify.ListFilter) ([]notify.StoredEvent, error) {
	return a.db.ListNotifications(ctx, f)
}

func (a *notifyStoreAdapter) MarkRead(ctx context.Context, ids []int64) error {
	return a.db.MarkNotificationsRead(ctx, ids)
}

func (a *notifyStoreAdapter) MarkAllRead(ctx context.Context) error {
	return a.db.MarkAllNotificationsRead(ctx)
}

func (a *notifyStoreAdapter) UnreadCount(ctx context.Context) (int, error) {
	return a.db.UnreadNotificationCount(ctx)
}

func (a *notifyStoreAdapter) Prune(ctx context.Context, cap int) (int, error) {
	return a.db.PruneNotifications(ctx, cap)
}

// notificationRetentionCap is the MVP default. Oldest-read rows get
// evicted first, then oldest period.
const notificationRetentionCap = 500

// pruneNotificationsLoop runs every 5 minutes, keeping the notifications
// table bounded. Non-fatal: errors are logged, the loop continues.
func pruneNotificationsLoop(ctx context.Context, db *sqlite.DB) {
	store := newNotifyStore(db)
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			n, err := store.Prune(c, notificationRetentionCap)
			cancel()
			if err != nil {
				slog.Warn("notify prune failed", "error", err)
				continue
			}
			if n > 0 {
				slog.Info("notify pruned", "deleted", n, "cap", notificationRetentionCap)
			}
		}
	}
}
