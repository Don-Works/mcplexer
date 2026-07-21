package notify

import (
	"context"
	"time"
)

// WebPushVAPIDKeys are the local protocol signing keys used by Web Push.
// PrivateKey never leaves backend code; the PWA only receives PublicKey.
type WebPushVAPIDKeys struct {
	PublicKey  string
	PrivateKey string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// WebPushSubscription is a browser PushSubscription persisted locally.
type WebPushSubscription struct {
	Endpoint      string
	P256DH        string
	Auth          string
	UserAgent     string
	Origin        string
	DeviceLabel   string
	Enabled       bool
	CreatedAt     time.Time
	UpdatedAt     time.Time
	LastSuccessAt *time.Time
	LastErrorAt   *time.Time
	LastError     string
}

// PushStore persists Web Push protocol keys and browser subscriptions.
type PushStore interface {
	EnsureVAPIDKeys(ctx context.Context) (WebPushVAPIDKeys, error)
	UpsertPushSubscription(ctx context.Context, sub WebPushSubscription) error
	DeletePushSubscription(ctx context.Context, endpoint string) error
	ListPushSubscriptions(ctx context.Context) ([]WebPushSubscription, error)
	MarkPushSubscriptionSuccess(ctx context.Context, endpoint string) error
	MarkPushSubscriptionError(ctx context.Context, endpoint, message string, disable bool) error
}
