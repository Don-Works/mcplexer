package collectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestOpenRouterFetchUsesOfficialKeyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/key" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("request path=%q auth=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		_, _ = w.Write([]byte(`{"data":{"usage":70,"limit":100,"limit_remaining":42,"usage_daily":0,"usage_weekly":9,"usage_monthly":30}}`))
	}))
	defer server.Close()
	secret := &recordingSecret{value: []byte("secret")}
	collector := OpenRouterCollector{Client: server.Client(), Secret: secret}
	result, err := collector.Fetch(context.Background(), store.SourceConfig{
		AuthScopeID: "workspace", SecretKey: "openrouter", BaseURL: server.URL,
	})
	if err != nil || result.Snapshot.Status != store.StatusOK {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if secret.scope != "workspace" || secret.key != "openrouter" {
		t.Fatalf("secret lookup scope=%q key=%q", secret.scope, secret.key)
	}
	requireNumber(t, result.Snapshot.Credits.Remaining, 42)
	requireNumber(t, result.Snapshot.Credits.UsageDaily, 0)
	if result.Snapshot.UpdatedAt == nil {
		t.Fatal("updated_at is nil")
	}
}

func TestOpenRouterUnlimitedPreservesUnknownLimit(t *testing.T) {
	credits, err := parseORCredits([]byte(`{"data":{"usage":12.5,"limit":null,"limit_remaining":null}}`))
	if err != nil {
		t.Fatal(err)
	}
	requireNumber(t, credits.Usage, 12.5)
	if credits.Limit != nil || credits.Remaining != nil {
		t.Fatalf("unlimited key gained a limit: %+v", credits)
	}
	encoded, err := json.Marshal(credits)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"usage":12.5}` {
		t.Fatalf("JSON = %s", encoded)
	}
}

func TestOpenRouterDerivesRemainingOnlyWhenLimited(t *testing.T) {
	credits, err := parseORCredits([]byte(`{"data":{"usage":7,"limit":10}}`))
	if err != nil {
		t.Fatal(err)
	}
	requireNumber(t, credits.Remaining, 3)
}

func TestOpenRouterRejectsMissingKeyData(t *testing.T) {
	if _, err := parseORCredits([]byte(`{"data":{}}`)); err == nil {
		t.Fatal("expected missing key usage error")
	}
}

func TestOpenRouterNilSecretIsUnconfigured(t *testing.T) {
	result, err := (&OpenRouterCollector{}).Fetch(context.Background(), store.SourceConfig{
		AuthScopeID: "scope", SecretKey: "key",
	})
	if err != nil || result.Snapshot.Status != store.StatusUnconfigured {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
