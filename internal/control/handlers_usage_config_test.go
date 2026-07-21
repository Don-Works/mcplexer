package control

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/don-works/mcplexer/internal/config"
)

func TestUsageSourceConfigureAndRemove(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	b := NewInternalBackend(db, nil)

	configured, err := b.Call(context.Background(), "configure_usage_source", json.RawMessage(`{
		"provider":"openrouter",
		"harness":"opencode",
		"auth_scope_id":"scope-1",
		"secret_key":"api_key"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	assertUsageToolResultOK(t, configured)

	settings := config.NewSettingsService(db).Load(context.Background())
	if len(settings.UsageSources) != 1 || settings.UsageSources[0].Harness != "opencode" {
		t.Fatalf("usage sources = %#v", settings.UsageSources)
	}

	replaced, err := b.Call(context.Background(), "configure_usage_source", json.RawMessage(`{
		"provider":"openrouter",
		"harness":"mimo",
		"auth_scope_id":"scope-2",
		"secret_key":"api_key"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	assertUsageToolResultOK(t, replaced)
	settings = config.NewSettingsService(db).Load(context.Background())
	if len(settings.UsageSources) != 1 || settings.UsageSources[0].Harness != "mimo" {
		t.Fatalf("openrouter source was not replaced = %#v", settings.UsageSources)
	}

	removed, err := b.Call(context.Background(), "remove_usage_source", json.RawMessage(`{"provider":"openrouter"}`))
	if err != nil {
		t.Fatal(err)
	}
	assertUsageToolResultOK(t, removed)
	settings = config.NewSettingsService(db).Load(context.Background())
	if len(settings.UsageSources) != 0 {
		t.Fatalf("usage sources after remove = %#v", settings.UsageSources)
	}
}

func assertUsageToolResultOK(t *testing.T, raw json.RawMessage) {
	t.Helper()
	var result struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("unexpected error result: %s", raw)
	}
}

func TestConfigureUsageSourceRejectsUnsafeBaseURL(t *testing.T) {
	t.Parallel()
	b := NewInternalBackend(newTestDB(t), nil)
	got, err := b.Call(context.Background(), "configure_usage_source", json.RawMessage(`{
		"provider":"openrouter",
		"base_url":"https://127.0.0.1/private"
	}`))
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(got, &result); err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("expected error result: %s", got)
	}
}
