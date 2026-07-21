package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/don-works/mcplexer/internal/notify"
)

type pushHandler struct {
	store notify.PushStore
	bus   *notify.Bus
}

type pushSubscriptionWire struct {
	Endpoint string `json:"endpoint"`
	Keys     struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (h *pushHandler) publicKey(w http.ResponseWriter, r *http.Request) {
	keys, err := h.store.EnsureVAPIDKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "web push key: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"public_key": keys.PublicKey,
		"supported":  true,
	})
}

func (h *pushHandler) status(w http.ResponseWriter, r *http.Request) {
	subs, err := h.store.ListPushSubscriptions(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list push subscriptions: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"subscription_count": len(subs),
	})
}

func (h *pushHandler) subscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Subscription pushSubscriptionWire `json:"subscription"`
		DeviceLabel  string               `json:"device_label"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	endpoint := strings.TrimSpace(req.Subscription.Endpoint)
	p256dh := strings.TrimSpace(req.Subscription.Keys.P256DH)
	auth := strings.TrimSpace(req.Subscription.Keys.Auth)
	if endpoint == "" || p256dh == "" || auth == "" {
		writeError(w, http.StatusBadRequest, "subscription endpoint and keys are required")
		return
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "http://" + r.Host
	}
	if err := h.store.UpsertPushSubscription(r.Context(), notify.WebPushSubscription{
		Endpoint:    endpoint,
		P256DH:      p256dh,
		Auth:        auth,
		UserAgent:   r.UserAgent(),
		Origin:      origin,
		DeviceLabel: strings.TrimSpace(req.DeviceLabel),
		Enabled:     true,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "save push subscription: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *pushHandler) unsubscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if strings.TrimSpace(req.Endpoint) == "" {
		writeError(w, http.StatusBadRequest, "endpoint is required")
		return
	}
	if err := h.store.DeletePushSubscription(r.Context(), req.Endpoint); err != nil {
		writeError(w, http.StatusInternalServerError, "delete push subscription: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *pushHandler) test(w http.ResponseWriter, r *http.Request) {
	if h.bus == nil {
		writeError(w, http.StatusServiceUnavailable, "notification bus is not configured")
		return
	}
	evt := notify.Event{
		MessageID: uuid.NewString(),
		Source:    "system",
		AgentName: "mcplexer",
		Role:      "pwa",
		Kind:      "push_test",
		Priority:  "high",
		Title:     "MCPlexer push test",
		Body:      "PWA notifications are connected.",
		Tags:      "pwa,push,test",
		Link:      "/app",
		CreatedAt: time.Now().UTC(),
	}
	if err := h.bus.PublishDurable(r.Context(), evt, true); err != nil {
		writeError(w, http.StatusBadGateway, "push test was not accepted by an enabled subscription")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
