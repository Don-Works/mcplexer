package main

import (
	"context"
	"errors"
	"log/slog"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/don-works/mcplexer/internal/notify"
	"github.com/don-works/mcplexer/internal/store"
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

func (a *notifyStoreAdapter) EnsureVAPIDKeys(ctx context.Context) (notify.WebPushVAPIDKeys, error) {
	keys, err := a.db.GetWebPushVAPIDKeys(ctx)
	if err == nil && keys.PublicKey != "" && keys.PrivateKey != "" {
		return keys, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return notify.WebPushVAPIDKeys{}, err
	}
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return notify.WebPushVAPIDKeys{}, err
	}
	now := time.Now().UTC()
	generated := notify.WebPushVAPIDKeys{
		PublicKey:  publicKey,
		PrivateKey: privateKey,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := a.db.InsertWebPushVAPIDKeys(ctx, generated); err != nil {
		return notify.WebPushVAPIDKeys{}, err
	}
	return a.db.GetWebPushVAPIDKeys(ctx)
}

func (a *notifyStoreAdapter) UpsertPushSubscription(ctx context.Context, sub notify.WebPushSubscription) error {
	return a.db.UpsertWebPushSubscription(ctx, sub)
}

func (a *notifyStoreAdapter) DeletePushSubscription(ctx context.Context, endpoint string) error {
	return a.db.DeleteWebPushSubscription(ctx, endpoint)
}

func (a *notifyStoreAdapter) ListPushSubscriptions(ctx context.Context) ([]notify.WebPushSubscription, error) {
	return a.db.ListWebPushSubscriptions(ctx)
}

func (a *notifyStoreAdapter) MarkPushSubscriptionSuccess(ctx context.Context, endpoint string) error {
	return a.db.MarkWebPushSubscriptionSuccess(ctx, endpoint)
}

func (a *notifyStoreAdapter) MarkPushSubscriptionError(ctx context.Context, endpoint, message string, disable bool) error {
	return a.db.MarkWebPushSubscriptionError(ctx, endpoint, message, disable)
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
