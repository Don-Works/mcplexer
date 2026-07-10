package usage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/usage/collectors"
)

func TestSnapshotEligibleLocalAuthProvidersUseDefaultRefs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch {
		case strings.Contains(r.URL.Path, "token_plan"):
			_, _ = w.Write([]byte(`{"data":{"windows":[{"label":"Token grant","used_percent":12}]}}`))
		case strings.Contains(r.URL.Path, "quota"):
			_, _ = w.Write([]byte(`{"data":{"windows":[{"label":"Coding plan","used_percent":8}]}}`))
		case strings.Contains(r.URL.Path, "openrouter"):
			_, _ = w.Write([]byte(`{"data":{"usage":1.5,"limit":10,"remaining":8.5}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	const token = "live-token"
	secret := &recordingLocalSecret{value: []byte(token)}
	client := server.Client()
	service := &Service{
		Store: &fakeUsageStore{},
		Collectors: map[string]ProviderCollector{
			store.ProviderMiniMax: &collectors.MiniMaxCollector{
				Client: client, Secret: secret,
			},
			store.ProviderZAI: &collectors.ZAICollector{
				Client: client, Secret: secret,
			},
			store.ProviderMiMo: &collectors.MiMoCollector{
				Run: func(_ context.Context, _ string) ([]byte, error) {
					return []byte(`{"provider":"xiaomi"}`), nil
				},
			},
		},
		ORCollector: &collectors.OpenRouterCollector{
			Client: client, Secret: secret,
		},
	}
	configs := []store.SourceConfig{
		withTestBaseURL(localAPIConfig(store.ProviderMiniMax, store.LocalAuthKeyMiniMax), server.URL),
		withTestBaseURL(localAPIConfig(store.ProviderZAI, store.LocalAuthKeyZAI), server.URL),
		{
			Provider:    store.ProviderOpenRouter,
			Kind:        store.SourceKindAPI,
			AuthScopeID: store.LocalAuthScopeOpenCode,
			SecretKey:   store.LocalAuthKeyOpenRouter,
			BaseURL:     server.URL,
			Enabled:     true,
		},
		{
			Provider:    store.ProviderMiMo,
			Kind:        store.SourceKindCLI,
			AuthScopeID: store.LocalAuthScopeMiMo,
			SecretKey:   store.LocalAuthKeyMiMoXiaomi,
			Enabled:     true,
		},
	}
	snapshot, err := service.Snapshot(context.Background(), configs, 30, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{
		store.ProviderMiniMax, store.ProviderZAI, store.ProviderMiMo,
	} {
		row := providerByName(t, snapshot, provider)
		if row.AllowanceStatus == store.StatusUnconfigured {
			t.Fatalf("%s remained unconfigured", provider)
		}
	}
	if snapshot.OpenRouter.Status == store.StatusUnconfigured {
		t.Fatalf("openrouter remained unconfigured: %+v", snapshot.OpenRouter)
	}
	secret.mu.Lock()
	lastKey := secret.lastKey
	secret.mu.Unlock()
	if !store.IsLocalAuthRef(store.LocalAuthScopeOpenCode, lastKey) {
		t.Fatalf("unexpected local auth key = %q", lastKey)
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), token) {
		t.Fatalf("snapshot leaked token")
	}
}

type recordingLocalSecret struct {
	mu      sync.Mutex
	value   []byte
	lastKey string
}

func (s *recordingLocalSecret) Get(_ context.Context, scopeID, key string) ([]byte, error) {
	s.mu.Lock()
	s.lastKey = key
	s.mu.Unlock()
	if store.IsLocalAuthRef(scopeID, key) {
		return s.value, nil
	}
	return nil, errMissingLocalSecret{}
}

type errMissingLocalSecret struct{}

func (errMissingLocalSecret) Error() string { return "missing local secret" }

func withTestBaseURL(cfg store.SourceConfig, baseURL string) store.SourceConfig {
	cfg.BaseURL = baseURL
	return cfg
}