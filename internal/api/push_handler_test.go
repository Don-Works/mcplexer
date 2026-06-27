package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/notify"
)

type fakePushStore struct {
	mu   sync.Mutex
	keys notify.WebPushVAPIDKeys
	subs map[string]notify.WebPushSubscription
}

func newFakePushStore() *fakePushStore {
	return &fakePushStore{
		keys: notify.WebPushVAPIDKeys{
			PublicKey:  "public-key",
			PrivateKey: "private-key",
			CreatedAt:  time.Now().UTC(),
			UpdatedAt:  time.Now().UTC(),
		},
		subs: map[string]notify.WebPushSubscription{},
	}
}

func (s *fakePushStore) EnsureVAPIDKeys(context.Context) (notify.WebPushVAPIDKeys, error) {
	return s.keys, nil
}

func (s *fakePushStore) UpsertPushSubscription(_ context.Context, sub notify.WebPushSubscription) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs[sub.Endpoint] = sub
	return nil
}

func (s *fakePushStore) DeletePushSubscription(_ context.Context, endpoint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subs, endpoint)
	return nil
}

func (s *fakePushStore) ListPushSubscriptions(context.Context) ([]notify.WebPushSubscription, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]notify.WebPushSubscription, 0, len(s.subs))
	for _, sub := range s.subs {
		out = append(out, sub)
	}
	return out, nil
}

func (s *fakePushStore) MarkPushSubscriptionSuccess(context.Context, string) error {
	return nil
}

func (s *fakePushStore) MarkPushSubscriptionError(context.Context, string, string, bool) error {
	return nil
}

func TestPushHandlerLifecycleAndTestEvent(t *testing.T) {
	store := newFakePushStore()
	bus := notify.NewBus()
	ch := bus.Subscribe()
	defer bus.Unsubscribe(ch)

	h := &pushHandler{store: store, bus: bus}

	rec := httptest.NewRecorder()
	h.publicKey(rec, httptest.NewRequest(http.MethodGet, "/api/v1/push/public-key", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("publicKey status = %d, body = %q", rec.Code, rec.Body.String())
	}
	var keyResp struct {
		PublicKey string `json:"public_key"`
		Supported bool   `json:"supported"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &keyResp); err != nil {
		t.Fatalf("decode publicKey: %v", err)
	}
	if keyResp.PublicKey != "public-key" || !keyResp.Supported {
		t.Fatalf("publicKey response = %+v", keyResp)
	}

	body := `{"subscription":{"endpoint":"https://push.example/sub","keys":{"p256dh":"p256","auth":"auth"}},"device_label":"Phone"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/push/subscribe", strings.NewReader(body))
	req.Header.Set("Origin", "https://app.example")
	rec = httptest.NewRecorder()
	h.subscribe(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("subscribe status = %d, body = %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.status(rec, httptest.NewRequest(http.MethodGet, "/api/v1/push/status", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"subscription_count":1`) {
		t.Fatalf("status after subscribe = %d %q", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.test(rec, httptest.NewRequest(http.MethodPost, "/api/v1/push/test", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("test status = %d, body = %q", rec.Code, rec.Body.String())
	}
	select {
	case evt := <-ch:
		if evt.Kind != "push_test" || evt.Priority != "high" || evt.Link != "/app" {
			t.Fatalf("test event = %+v", evt)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for push test event")
	}

	rec = httptest.NewRecorder()
	h.unsubscribe(rec, httptest.NewRequest(http.MethodPost, "/api/v1/push/unsubscribe", strings.NewReader(`{"endpoint":"https://push.example/sub"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("unsubscribe status = %d, body = %q", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	h.status(rec, httptest.NewRequest(http.MethodGet, "/api/v1/push/status", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"subscription_count":0`) {
		t.Fatalf("status after unsubscribe = %d %q", rec.Code, rec.Body.String())
	}
}

func TestPushHandlerSubscribeRequiresEndpointAndKeys(t *testing.T) {
	h := &pushHandler{store: newFakePushStore(), bus: notify.NewBus()}
	rec := httptest.NewRecorder()
	h.subscribe(rec, httptest.NewRequest(http.MethodPost, "/api/v1/push/subscribe", strings.NewReader(`{"subscription":{"endpoint":"https://push.example/sub"}}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("subscribe missing keys status = %d, want 400", rec.Code)
	}
}
