package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/don-works/mcplexer/internal/notify"
)

type webPushDispatcher struct {
	store notify.PushStore
}

var webPushTopicUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func newWebPushDispatcher(store notify.PushStore) *webPushDispatcher {
	if store == nil {
		return nil
	}
	return &webPushDispatcher{store: store}
}

func (d *webPushDispatcher) Dispatch(ctx context.Context, evt notify.Event) {
	if d == nil || d.store == nil || !webPushEligible(evt) {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	keys, err := d.store.EnsureVAPIDKeys(ctx)
	if err != nil {
		slog.Warn("web push: load vapid keys failed", "error", err)
		return
	}
	subs, err := d.store.ListPushSubscriptions(ctx)
	if err != nil {
		slog.Warn("web push: list subscriptions failed", "error", err)
		return
	}
	if len(subs) == 0 {
		return
	}

	payload, err := json.Marshal(webPushPayload(evt))
	if err != nil {
		slog.Warn("web push: encode payload failed", "message_id", evt.MessageID, "error", err)
		return
	}
	for _, sub := range subs {
		d.sendOne(ctx, keys, sub, payload, evt)
	}
}

func (d *webPushDispatcher) sendOne(
	ctx context.Context,
	keys notify.WebPushVAPIDKeys,
	sub notify.WebPushSubscription,
	payload []byte,
	evt notify.Event,
) {
	resp, err := webpush.SendNotificationWithContext(
		ctx,
		payload,
		&webpush.Subscription{
			Endpoint: sub.Endpoint,
			Keys: webpush.Keys{
				P256dh: sub.P256DH,
				Auth:   sub.Auth,
			},
		},
		&webpush.Options{
			Subscriber:      "mailto:notifications@mcplexer.local",
			TTL:             webPushTTL(evt),
			Topic:           webPushTopic(evt),
			Urgency:         webPushUrgency(evt),
			VAPIDPublicKey:  keys.PublicKey,
			VAPIDPrivateKey: keys.PrivateKey,
		},
	)
	if err != nil {
		_ = d.store.MarkPushSubscriptionError(ctx, sub.Endpoint, err.Error(), false)
		slog.Warn("web push: send failed", "endpoint", endpointHost(sub.Endpoint), "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = d.store.MarkPushSubscriptionSuccess(ctx, sub.Endpoint)
		return
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	disable := resp.StatusCode == http.StatusGone || resp.StatusCode == http.StatusNotFound
	msg := fmt.Sprintf("push service status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	_ = d.store.MarkPushSubscriptionError(ctx, sub.Endpoint, msg, disable)
	if disable {
		slog.Info("web push: disabled expired subscription", "endpoint", endpointHost(sub.Endpoint), "status", resp.StatusCode)
		return
	}
	slog.Warn("web push: send rejected", "endpoint", endpointHost(sub.Endpoint), "status", resp.StatusCode)
}

func webPushEligible(evt notify.Event) bool {
	priority := strings.ToLower(evt.Priority)
	source := strings.ToLower(evt.Source)
	kind := strings.ToLower(evt.Kind)
	if kind == "push_test" {
		return true
	}
	if kind == "approval_pending" {
		return true
	}
	if source == "task" && (kind == "task_created" || kind == "task_updated") {
		return true
	}
	return priority == "critical" || priority == "high"
}

type webPushMessage struct {
	ID       string `json:"id,omitempty"`
	Title    string `json:"title"`
	Body     string `json:"body,omitempty"`
	Summary  string `json:"summary,omitempty"`
	Tag      string `json:"tag,omitempty"`
	Icon     string `json:"icon,omitempty"`
	Badge    string `json:"badge,omitempty"`
	URL      string `json:"url,omitempty"`
	Source   string `json:"source,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Priority string `json:"priority,omitempty"`
}

func webPushPayload(evt notify.Event) webPushMessage {
	title := strings.TrimSpace(evt.Title)
	if title == "" {
		title = "MCPlexer"
	}
	body := strings.TrimSpace(evt.Body)
	if body == "" {
		body = "New notification"
	}
	url := strings.TrimSpace(evt.Link)
	if url == "" {
		url = "/app"
	}
	return webPushMessage{
		ID:       evt.MessageID,
		Title:    title,
		Body:     body,
		Summary:  body,
		Tag:      webPushTopic(evt),
		Icon:     "/icon-192.png",
		Badge:    "/icon-192.png",
		URL:      url,
		Source:   evt.Source,
		Kind:     evt.Kind,
		Priority: evt.Priority,
	}
}

func webPushTTL(evt notify.Event) int {
	switch strings.ToLower(evt.Priority) {
	case "critical":
		return 60 * 60
	case "high":
		return 30 * 60
	default:
		return 10 * 60
	}
}

func webPushUrgency(evt notify.Event) webpush.Urgency {
	switch strings.ToLower(evt.Priority) {
	case "critical":
		return webpush.UrgencyHigh
	case "high":
		return webpush.UrgencyHigh
	case "low":
		return webpush.UrgencyLow
	default:
		return webpush.UrgencyNormal
	}
}

func webPushTopic(evt notify.Event) string {
	source := strings.TrimSpace(evt.Source)
	if source == "" {
		source = "signal"
	}
	id := strings.TrimSpace(evt.MessageID)
	if id == "" {
		id = strings.TrimSpace(evt.Kind)
	}
	if source == "task" && strings.HasPrefix(id, "task_") {
		parts := strings.Split(id, ":")
		if len(parts) >= 2 {
			id = parts[1]
		}
	}
	topic := source + ":" + id
	topic = strings.Trim(webPushTopicUnsafe.ReplaceAllString(topic, "_"), "_")
	if topic == "" {
		topic = "mcplexer"
	}
	if len(topic) > 32 {
		return topic[:32]
	}
	return topic
}

func endpointHost(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	parts := strings.SplitN(strings.TrimPrefix(strings.TrimPrefix(endpoint, "https://"), "http://"), "/", 2)
	return parts[0]
}
