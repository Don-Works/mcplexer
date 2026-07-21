package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/don-works/mcplexer/internal/notify"
)

const (
	webPushSendAttempts = 3
	webPushRetryDelay   = 200 * time.Millisecond
)

func (d *webPushDispatcher) sendOne(
	ctx context.Context,
	keys notify.WebPushVAPIDKeys,
	sub notify.WebPushSubscription,
	payload []byte,
	evt notify.Event,
) error {
	var err error
	for attempt := 1; attempt <= webPushSendAttempts; attempt++ {
		var retry bool
		err, retry = d.sendOneAttempt(ctx, keys, sub, payload, evt)
		if err == nil || !retry || attempt == webPushSendAttempts {
			return err
		}
		slog.Warn("web push: transient failure; retrying",
			"endpoint", endpointHost(sub.Endpoint), "attempt", attempt)
		if err := waitWebPushRetry(ctx, webPushRetryDelay*time.Duration(attempt)); err != nil {
			return err
		}
	}
	return err
}

func (d *webPushDispatcher) sendOneAttempt(
	ctx context.Context,
	keys notify.WebPushVAPIDKeys,
	sub notify.WebPushSubscription,
	payload []byte,
	evt notify.Event,
) (error, bool) {
	resp, err := webpush.SendNotificationWithContext(
		ctx, payload, webPushSubscription(sub), webPushOptions(d.subscriber, keys, evt),
	)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err, false
		}
		safe := scrubPushEndpoint(err.Error(), sub.Endpoint)
		_ = d.store.MarkPushSubscriptionError(ctx, sub.Endpoint, safe, false)
		slog.Warn("web push: send failed", "endpoint", endpointHost(sub.Endpoint), "error", safe)
		return fmt.Errorf("%s: transport failure", endpointHost(sub.Endpoint)), true
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = d.store.MarkPushSubscriptionSuccess(ctx, sub.Endpoint)
		return nil, false
	}
	return d.recordPushRejection(ctx, sub, resp.StatusCode)
}

func webPushSubscription(sub notify.WebPushSubscription) *webpush.Subscription {
	return &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256DH, Auth: sub.Auth},
	}
}

func webPushOptions(
	subscriber string, keys notify.WebPushVAPIDKeys, evt notify.Event,
) *webpush.Options {
	return &webpush.Options{
		Subscriber: subscriber, TTL: webPushTTL(evt), Topic: webPushTopic(evt),
		Urgency: webPushUrgency(evt), VAPIDPublicKey: keys.PublicKey,
		VAPIDPrivateKey: keys.PrivateKey,
	}
}

func (d *webPushDispatcher) recordPushRejection(
	ctx context.Context, sub notify.WebPushSubscription, status int,
) (error, bool) {
	disable := status == http.StatusGone || status == http.StatusNotFound
	message := fmt.Sprintf("push service status %d", status)
	_ = d.store.MarkPushSubscriptionError(ctx, sub.Endpoint, message, disable)
	if disable {
		slog.Info("web push: disabled expired subscription",
			"endpoint", endpointHost(sub.Endpoint), "status", status)
		return fmt.Errorf("%s: subscription expired (status %d)", endpointHost(sub.Endpoint), status), false
	}
	slog.Warn("web push: send rejected", "endpoint", endpointHost(sub.Endpoint), "status", status)
	return fmt.Errorf("%s: push service status %d", endpointHost(sub.Endpoint), status),
		webPushRetryableStatus(status)
}

func webPushRetryableStatus(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly ||
		status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func scrubPushEndpoint(message, endpoint string) string {
	if endpoint == "" {
		return message
	}
	return strings.ReplaceAll(message, endpoint, "[redacted-push-endpoint]")
}

func waitWebPushRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
