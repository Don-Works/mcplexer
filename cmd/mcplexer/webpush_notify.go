package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/don-works/mcplexer/internal/notify"
)

type webPushDispatcher struct {
	store      notify.PushStore
	subscriber string
}

var webPushTopicUnsafe = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func newWebPushDispatcher(store notify.PushStore, subscriber string) *webPushDispatcher {
	if store == nil {
		return nil
	}
	return &webPushDispatcher{store: store, subscriber: normalizeWebPushSubscriber(subscriber)}
}

func (d *webPushDispatcher) Dispatch(ctx context.Context, evt notify.Event) error {
	if d == nil || d.store == nil {
		return errors.New("web push store is not configured")
	}
	if !webPushEligible(evt) {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	keys, err := d.store.EnsureVAPIDKeys(ctx)
	if err != nil {
		return fmt.Errorf("load VAPID keys: %w", err)
	}
	subs, err := d.store.ListPushSubscriptions(ctx)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}
	if len(subs) == 0 {
		return errors.New("no enabled Web Push subscriptions")
	}

	payload, err := json.Marshal(webPushPayload(evt))
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	var failures []error
	delivered := 0
	for _, sub := range subs {
		if err := d.sendOne(ctx, keys, sub, payload, evt); err != nil {
			failures = append(failures, err)
			continue
		}
		delivered++
	}
	if delivered == 0 {
		return fmt.Errorf("no subscription accepted push: %w", errors.Join(failures...))
	}
	if len(failures) > 0 {
		slog.Warn("web push: partial delivery", "message_id", evt.MessageID,
			"delivered", delivered, "failed", len(failures))
	}
	return nil
}

func webPushSubscriber(explicit, publicURL string) string {
	if sub, ok := normalizeWebPushSubscriberCandidate(explicit); ok {
		return sub
	}
	if sub, ok := normalizeWebPushSubscriberCandidate(publicURL); ok {
		return sub
	}
	return defaultWebPushSubscriber
}

const defaultWebPushSubscriber = "https://github.com/Don-Works/mcplexer"

func normalizeWebPushSubscriber(raw string) string {
	if sub, ok := normalizeWebPushSubscriberCandidate(raw); ok {
		return sub
	}
	return defaultWebPushSubscriber
}

func normalizeWebPushSubscriberCandidate(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	if strings.HasPrefix(raw, "mailto:") || strings.HasPrefix(raw, "https://") {
		if validWebPushSubscriber(raw) {
			return raw, true
		}
		return "", false
	}
	if !strings.Contains(raw, "://") {
		candidate := "https://" + raw
		if validWebPushSubscriber(candidate) {
			return candidate, true
		}
	}
	return "", false
}

func validWebPushSubscriber(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch u.Scheme {
	case "mailto":
		return strings.TrimSpace(u.Opaque) != "" || strings.TrimSpace(u.Path) != ""
	case "https":
		return strings.TrimSpace(u.Hostname()) != ""
	default:
		return false
	}
}

func webPushEligible(evt notify.Event) bool {
	priority := strings.ToLower(strings.TrimSpace(evt.Priority))
	source := strings.ToLower(strings.TrimSpace(evt.Source))
	kind := strings.ToLower(strings.TrimSpace(evt.Kind))
	switch {
	case kind == "push_test":
		return true
	case source == "approval" && kind == "approval_pending":
		return true
	case source == "secret" && kind == "secret_prompt":
		return true
	case source == "task" && (kind == "task_assigned" || kind == "task_due"):
		return true
	case source == "memory" && kind == "memory_offer_received":
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
	case "urgent", "high":
		return 30 * 60
	default:
		return 10 * 60
	}
}

func webPushUrgency(evt notify.Event) webpush.Urgency {
	switch strings.ToLower(evt.Priority) {
	case "critical":
		return webpush.UrgencyHigh
	case "urgent", "high":
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
