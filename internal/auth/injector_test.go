package auth

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/oauth"
	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// fakeAuthScopeStore is an in-memory store.AuthScopeStore for exercising the
// Injector without a real database. It holds the encrypted blob written by a
// secrets.Manager so List/Get round-trip through real age decryption.
type fakeAuthScopeStore struct{ scopes map[string]*store.AuthScope }

func newFakeAuthScopeStore() *fakeAuthScopeStore {
	return &fakeAuthScopeStore{scopes: make(map[string]*store.AuthScope)}
}

func (f *fakeAuthScopeStore) CreateAuthScope(_ context.Context, a *store.AuthScope) error {
	f.scopes[a.ID] = a
	return nil
}

func (f *fakeAuthScopeStore) GetAuthScope(_ context.Context, id string) (*store.AuthScope, error) {
	s, ok := f.scopes[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	return s, nil
}

func (f *fakeAuthScopeStore) GetAuthScopeByName(_ context.Context, _ string) (*store.AuthScope, error) {
	return nil, store.ErrNotFound
}

func (f *fakeAuthScopeStore) ListAuthScopes(_ context.Context) ([]store.AuthScope, error) {
	out := make([]store.AuthScope, 0, len(f.scopes))
	for _, s := range f.scopes {
		out = append(out, *s)
	}
	return out, nil
}

func (f *fakeAuthScopeStore) UpdateAuthScope(_ context.Context, a *store.AuthScope) error {
	f.scopes[a.ID] = a
	return nil
}

func (f *fakeAuthScopeStore) DeleteAuthScope(_ context.Context, id string) error {
	delete(f.scopes, id)
	return nil
}

func (f *fakeAuthScopeStore) UpdateAuthScopeTokenData(_ context.Context, id string, data []byte) error {
	if s, ok := f.scopes[id]; ok {
		s.OAuthTokenData = data
	}
	return nil
}

func (f *fakeAuthScopeStore) UpdateAuthScopeEncryptedData(_ context.Context, id string, data []byte) error {
	if s, ok := f.scopes[id]; ok {
		s.EncryptedData = data
	}
	return nil
}

// newTestManager builds a real secrets.Manager backed by the given fake store
// and a fresh ephemeral age key, plus seeds a scope with the supplied secrets.
func newTestManager(t *testing.T, st *fakeAuthScopeStore, scopeID, scopeType string, seed map[string]string) *secrets.Manager {
	t.Helper()
	enc, err := secrets.NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("ephemeral encryptor: %v", err)
	}
	if _, ok := st.scopes[scopeID]; !ok {
		st.scopes[scopeID] = &store.AuthScope{ID: scopeID, Name: scopeID, Type: scopeType}
	}
	sm := secrets.NewManager(st, enc)
	for k, v := range seed {
		if err := sm.Put(context.Background(), scopeID, k, []byte(v)); err != nil {
			t.Fatalf("seed secret %s: %v", k, err)
		}
	}
	return sm
}

func TestEnvForDownstream(t *testing.T) {
	t.Run("empty scope id returns nil,nil", func(t *testing.T) {
		inj := NewInjector(nil, nil, nil)
		env, err := inj.EnvForDownstream(context.Background(), "")
		if err != nil || env != nil {
			t.Fatalf("got (%v, %v), want (nil, nil)", env, err)
		}
	})

	t.Run("env scope dumps all keys", func(t *testing.T) {
		st := newFakeAuthScopeStore()
		sm := newTestManager(t, st, "scope-env", "env", map[string]string{
			"API_KEY": "k1",
			"REGION":  "eu",
		})
		inj := NewInjector(sm, nil, st)
		env, err := inj.EnvForDownstream(context.Background(), "scope-env")
		if err != nil {
			t.Fatalf("EnvForDownstream: %v", err)
		}
		if env["API_KEY"] != "k1" || env["REGION"] != "eu" {
			t.Fatalf("unexpected env map: %v", env)
		}
	})

	t.Run("unknown scope type errors and never dumps secrets", func(t *testing.T) {
		st := newFakeAuthScopeStore()
		sm := newTestManager(t, st, "scope-unknown", "totally-made-up", map[string]string{
			"TOKEN": "t1",
		})
		inj := NewInjector(sm, nil, st)
		env, err := inj.EnvForDownstream(context.Background(), "scope-unknown")
		if err == nil {
			t.Fatalf("expected error, got env=%v", env)
		}
		if env != nil {
			t.Fatalf("expected nil env on error, got %v", env)
		}
		if !strings.Contains(err.Error(), "unknown auth scope type") {
			t.Fatalf("error = %q, want mention of unknown auth scope type", err)
		}
	})

	// Regression for issue #1: an oauth2 scope with a nil flowManager MUST
	// error, never fall through to the raw-secret env dump (which would leak
	// client_id / client_secret / refresh_token to the downstream process).
	t.Run("oauth2 without flow manager errors and never dumps secrets", func(t *testing.T) {
		st := newFakeAuthScopeStore()
		sm := newTestManager(t, st, "scope-oauth", "oauth2", map[string]string{
			"client_id":     "leak-me-not",
			"client_secret": "secret-leak",
			"refresh_token": "rt-leak",
		})
		inj := NewInjector(sm, nil /* flowManager */, st)
		env, err := inj.EnvForDownstream(context.Background(), "scope-oauth")
		if err == nil {
			t.Fatalf("expected error, got env=%v", env)
		}
		if env != nil {
			t.Fatalf("expected nil env on error, got %v", env)
		}
		if !strings.Contains(err.Error(), "no flow manager") {
			t.Fatalf("error = %q, want mention of missing flow manager", err)
		}
	})

	t.Run("oauth2 with flow manager returns ACCESS_TOKEN", func(t *testing.T) {
		inj, scopeID := newOAuth2InjectorWithToken(t, "tok-xyz")
		env, err := inj.EnvForDownstream(context.Background(), scopeID)
		if err != nil {
			t.Fatalf("EnvForDownstream: %v", err)
		}
		if env["ACCESS_TOKEN"] != "tok-xyz" || len(env) != 1 {
			t.Fatalf("env = %v, want only ACCESS_TOKEN=tok-xyz", env)
		}
	})
}

func TestHeadersForDownstream(t *testing.T) {
	t.Run("empty scope id returns nil,nil", func(t *testing.T) {
		inj := NewInjector(nil, nil, nil)
		h, err := inj.HeadersForDownstream(context.Background(), "")
		if err != nil || h != nil {
			t.Fatalf("got (%v, %v), want (nil, nil)", h, err)
		}
	})

	t.Run("env scope sets all keys as headers", func(t *testing.T) {
		st := newFakeAuthScopeStore()
		sm := newTestManager(t, st, "scope-env-h", "env", map[string]string{
			"X-Api-Key": "k1",
		})
		inj := NewInjector(sm, nil, st)
		h, err := inj.HeadersForDownstream(context.Background(), "scope-env-h")
		if err != nil {
			t.Fatalf("HeadersForDownstream: %v", err)
		}
		if h.Get("X-Api-Key") != "k1" {
			t.Fatalf("unexpected headers: %v", h)
		}
	})

	// Regression for issue #1 (header path): oauth2 + nil flowManager errors.
	t.Run("oauth2 without flow manager errors and never dumps secrets", func(t *testing.T) {
		st := newFakeAuthScopeStore()
		sm := newTestManager(t, st, "scope-oauth-h", "oauth2", map[string]string{
			"client_secret": "secret-leak",
		})
		inj := NewInjector(sm, nil, st)
		h, err := inj.HeadersForDownstream(context.Background(), "scope-oauth-h")
		if err == nil {
			t.Fatalf("expected error, got headers=%v", h)
		}
		if h != nil {
			t.Fatalf("expected nil headers on error, got %v", h)
		}
		if !strings.Contains(err.Error(), "no flow manager") {
			t.Fatalf("error = %q, want mention of missing flow manager", err)
		}
	})

	t.Run("oauth2 with flow manager returns bearer header", func(t *testing.T) {
		inj, scopeID := newOAuth2InjectorWithToken(t, "tok-abc")
		h, err := inj.HeadersForDownstream(context.Background(), scopeID)
		if err != nil {
			t.Fatalf("HeadersForDownstream: %v", err)
		}
		if got := h.Get("Authorization"); got != "Bearer tok-abc" {
			t.Fatalf("Authorization = %q, want %q", got, "Bearer tok-abc")
		}
	})
}

func TestGetSecret(t *testing.T) {
	t.Run("nil secrets manager errors", func(t *testing.T) {
		inj := NewInjector(nil, nil, nil)
		_, err := inj.GetSecret(context.Background(), "scope", "KEY")
		if err == nil || !strings.Contains(err.Error(), "no secrets manager") {
			t.Fatalf("err = %v, want no secrets manager error", err)
		}
	})

	t.Run("happy path returns plaintext", func(t *testing.T) {
		st := newFakeAuthScopeStore()
		sm := newTestManager(t, st, "scope-gs", "env", map[string]string{
			"DB_PASSWORD": "hunter2",
		})
		inj := NewInjector(sm, nil, st)
		val, err := inj.GetSecret(context.Background(), "scope-gs", "DB_PASSWORD")
		if err != nil {
			t.Fatalf("GetSecret: %v", err)
		}
		if string(val) != "hunter2" {
			t.Fatalf("GetSecret = %q, want hunter2", val)
		}
	})

	t.Run("missing key returns ErrNotFound", func(t *testing.T) {
		st := newFakeAuthScopeStore()
		sm := newTestManager(t, st, "scope-gs2", "env", map[string]string{"A": "b"})
		inj := NewInjector(sm, nil, st)
		_, err := inj.GetSecret(context.Background(), "scope-gs2", "MISSING")
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("err = %v, want store.ErrNotFound", err)
		}
	})
}

// newOAuth2InjectorWithToken builds an Injector backed by a real sqlite store
// and FlowManager, with an oauth2 scope whose decrypted token data carries the
// supplied access token. Used to exercise the oauth2 happy path end-to-end.
func newOAuth2InjectorWithToken(t *testing.T, accessToken string) (*Injector, string) {
	t.Helper()
	db, err := sqlite.New(context.Background(), t.TempDir()+"/test.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	enc, err := secrets.NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("ephemeral encryptor: %v", err)
	}
	fm := oauth.NewFlowManager(db, enc, "http://127.0.0.1:13333")

	scope := &store.AuthScope{ID: "oauth-scope", Name: "oauth-scope", Type: "oauth2"}
	if err := db.CreateAuthScope(context.Background(), scope); err != nil {
		t.Fatalf("create auth scope: %v", err)
	}

	// Encrypt token data the same way the FlowManager stores it: an encrypted
	// JSON OAuthTokenData blob with a non-expiring (zero ExpiresAt) token.
	td := store.OAuthTokenData{AccessToken: accessToken, TokenType: "Bearer"}
	plaintext, err := json.Marshal(td)
	if err != nil {
		t.Fatalf("marshal token data: %v", err)
	}
	encrypted, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("encrypt token data: %v", err)
	}
	if err := db.UpdateAuthScopeTokenData(context.Background(), scope.ID, encrypted); err != nil {
		t.Fatalf("update token data: %v", err)
	}

	sm := secrets.NewManager(db, enc)
	return NewInjector(sm, fm, db), scope.ID
}
