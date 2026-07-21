package usage

import (
	"testing"

	"github.com/don-works/mcplexer/internal/store"
)

func TestApplyDefaultSourceConfigsAddsLocalAuthProviders(t *testing.T) {
	out := normalizeConfigs(nil)
	cases := []struct {
		provider string
		kind     string
		scope    string
		key      string
	}{
		{store.ProviderClaude, store.SourceKindCLI, "", ""},
		{store.ProviderCodex, store.SourceKindCLI, "", ""},
		{store.ProviderGrok, store.SourceKindCLI, "", ""},
		{store.ProviderMiniMax, store.SourceKindAPI, store.LocalAuthScopeOpenCode, store.LocalAuthKeyMiniMax},
		{store.ProviderZAI, store.SourceKindAPI, store.LocalAuthScopeOpenCode, store.LocalAuthKeyZAI},
		{store.ProviderMiMo, store.SourceKindCLI, store.LocalAuthScopeMiMo, store.LocalAuthKeyMiMoXiaomi},
		{store.ProviderOpenRouter, store.SourceKindAPI, store.LocalAuthScopeOpenCode, store.LocalAuthKeyOpenRouter},
	}
	for _, tc := range cases {
		cfg, ok := out[tc.provider]
		if !ok {
			t.Fatalf("provider %q missing from defaults", tc.provider)
		}
		if cfg.Kind != tc.kind || cfg.AuthScopeID != tc.scope || cfg.SecretKey != tc.key {
			t.Fatalf("%s = kind:%s scope:%q key:%q", tc.provider, cfg.Kind, cfg.AuthScopeID, cfg.SecretKey)
		}
	}
}

func TestExplicitConfigOverridesDefaultLocalAuth(t *testing.T) {
	explicit := store.SourceConfig{
		Provider:    store.ProviderMiniMax,
		AuthScopeID: "saved-scope",
		SecretKey:   "api_key",
		Enabled:     true,
	}
	out := normalizeConfigs([]store.SourceConfig{explicit})
	cfg := out[store.ProviderMiniMax]
	if cfg.AuthScopeID != "saved-scope" || cfg.SecretKey != "api_key" {
		t.Fatalf("explicit config overridden: %+v", cfg)
	}
}
