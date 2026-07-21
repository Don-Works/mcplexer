package oauth

import (
	"context"
	"crypto/tls"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/don-works/mcplexer/internal/secrets"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

func TestParseOAuthURL(t *testing.T) {
	t.Run("accepts https url", func(t *testing.T) {
		if _, err := parseOAuthURL("https://example.com/oauth/authorize"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("accepts localhost http url", func(t *testing.T) {
		if _, err := parseOAuthURL("http://127.0.0.1:8080/authorize"); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("rejects javascript scheme", func(t *testing.T) {
		if _, err := parseOAuthURL("javascript:alert(1)"); err == nil {
			t.Fatal("expected error for javascript scheme")
		}
	})

	t.Run("rejects missing host", func(t *testing.T) {
		if _, err := parseOAuthURL("https:///authorize"); err == nil {
			t.Fatal("expected error for missing host")
		}
	})
}

func TestRequestCallbackURLIgnoresForwardedHeadersByDefault(t *testing.T) {
	fm := NewFlowManager(nil, nil, "http://fallback.local")
	req := httptest.NewRequest(http.MethodGet, "http://daemon.local/connect", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "attacker.example")

	got := fm.RequestCallbackURL(req)
	want := "http://daemon.local/api/v1/oauth/callback"
	if got != want {
		t.Fatalf("RequestCallbackURL = %q, want %q", got, want)
	}
}

func TestRequestCallbackURLHonorsForwardedHeadersFromTrustedProxy(t *testing.T) {
	fm := NewFlowManager(nil, nil, "http://fallback.local")
	if err := fm.SetTrustedProxies([]string{"127.0.0.1/32"}); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://daemon.local/connect", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-Proto", "https, http")
	req.Header.Set("X-Forwarded-Host", "app.example.com, daemon.local")

	got := fm.RequestCallbackURL(req)
	want := "https://app.example.com/api/v1/oauth/callback"
	if got != want {
		t.Fatalf("RequestCallbackURL = %q, want %q", got, want)
	}
}

func TestRequestCallbackURLIgnoresForwardedHeadersFromUntrustedIP(t *testing.T) {
	fm := NewFlowManager(nil, nil, "http://fallback.local")
	if err := fm.SetTrustedProxies([]string{"10.0.0.0/8"}); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://real-host.example.com/callback", nil)
	req.RemoteAddr = "192.168.1.1:12345"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "evil.example.com")

	got := fm.RequestCallbackURL(req)
	want := "http://real-host.example.com/api/v1/oauth/callback"
	if got != want {
		t.Fatalf("RequestCallbackURL = %q, want %q", got, want)
	}
}

func TestRequestCallbackURLPrefersConfiguredExternalURL(t *testing.T) {
	fm := NewFlowManager(nil, nil, "https://gateway.example")
	fm.SetPreferExternalURL(true)

	req := httptest.NewRequest(http.MethodGet, "http://peer.example:13333/connect", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "attacker.example")

	got := fm.RequestCallbackURL(req)
	want := "https://gateway.example/api/v1/oauth/callback"
	if got != want {
		t.Fatalf("RequestCallbackURL = %q, want %q", got, want)
	}
}

func TestRequestCallbackURLNilRequest(t *testing.T) {
	fm := NewFlowManager(nil, nil, "http://127.0.0.1:13333")
	got := fm.RequestCallbackURL(nil)
	want := "http://127.0.0.1:13333/api/v1/oauth/callback"
	if got != want {
		t.Fatalf("RequestCallbackURL(nil) = %q, want %q", got, want)
	}
}

func TestHasValidCachedToken(t *testing.T) {
	db, err := sqlite.New(t.Context(), t.TempDir()+"/oauth.db")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	enc, err := secrets.NewEphemeralEncryptor()
	if err != nil {
		t.Fatalf("encryptor: %v", err)
	}
	fm := NewFlowManager(db, enc, "http://127.0.0.1:13333")

	cases := []struct {
		name string
		td   *store.OAuthTokenData
		want bool
	}{
		{name: "valid non-expiring", td: &store.OAuthTokenData{AccessToken: "tok"}, want: true},
		{name: "valid future expiry", td: &store.OAuthTokenData{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Hour)}, want: true},
		{name: "nearly expired", td: &store.OAuthTokenData{AccessToken: "tok", ExpiresAt: time.Now().Add(time.Minute)}, want: false},
		{name: "missing access token", td: &store.OAuthTokenData{ExpiresAt: time.Now().Add(time.Hour)}, want: false},
		{name: "missing token data", want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			scope := &store.AuthScope{
				ID:   "scope-" + strings.ReplaceAll(c.name, " ", "-"),
				Name: c.name,
				Type: "oauth2",
			}
			if c.td != nil {
				sealed, err := fm.encryptTokenData(c.td)
				if err != nil {
					t.Fatalf("encrypt token: %v", err)
				}
				scope.OAuthTokenData = sealed
			}
			if err := db.CreateAuthScope(t.Context(), scope); err != nil {
				t.Fatalf("create scope: %v", err)
			}
			got, err := fm.HasValidCachedToken(t.Context(), scope.ID, 5*time.Minute)
			if err != nil {
				t.Fatalf("HasValidCachedToken: %v", err)
			}
			if got != c.want {
				t.Fatalf("valid = %v, want %v", got, c.want)
			}
		})
	}
}

func TestRequestCallbackURLTLS(t *testing.T) {
	fm := NewFlowManager(nil, nil, "http://fallback.local")
	req := httptest.NewRequest(http.MethodGet, "http://secure.example.com/callback", nil)
	req.TLS = &tls.ConnectionState{}

	got := fm.RequestCallbackURL(req)
	want := "https://secure.example.com/api/v1/oauth/callback"
	if got != want {
		t.Fatalf("RequestCallbackURL TLS = %q, want %q", got, want)
	}
}

func TestSetTrustedProxiesInvalidCIDR(t *testing.T) {
	fm := NewFlowManager(nil, nil, "http://fallback.local")
	if err := fm.SetTrustedProxies([]string{"not-a-cidr"}); err == nil {
		t.Fatal("expected invalid CIDR error")
	}
}

func TestSetTrustedProxiesBareIP(t *testing.T) {
	fm := NewFlowManager(nil, nil, "http://fallback.local")
	if err := fm.SetTrustedProxies([]string{"10.0.0.1"}); err != nil {
		t.Fatalf("bare IP should be accepted as /32: %v", err)
	}
	if len(fm.trustedProxies) != 1 {
		t.Fatalf("trustedProxies len = %d, want 1", len(fm.trustedProxies))
	}
}

func TestRedirectURIForStoredWins(t *testing.T) {
	fm := NewFlowManager(nil, nil, "http://fallback.local")
	p := &store.OAuthProvider{RedirectURI: "https://registered.example.com/callback"}

	got := fm.RedirectURIFor(p, nil)
	if got != p.RedirectURI {
		t.Fatalf("RedirectURIFor stored = %q, want %q", got, p.RedirectURI)
	}
}

func TestRedirectURIForFallbackWithoutStored(t *testing.T) {
	fm := NewFlowManager(nil, nil, "http://fallback.local")
	p := &store.OAuthProvider{}
	req := httptest.NewRequest(http.MethodGet, "http://myhost.example.com/callback", nil)

	got := fm.RedirectURIFor(p, req)
	want := "http://myhost.example.com/api/v1/oauth/callback"
	if got != want {
		t.Fatalf("RedirectURIFor fallback = %q, want %q", got, want)
	}
}

func TestStateStoreBindsRedirectURI(t *testing.T) {
	ss := NewStateStore()
	state, err := ss.Create("scope-1", "verifier-1", "https://app.example/callback")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	entry, ok := ss.Validate(state)
	if !ok {
		t.Fatal("Validate returned !ok")
	}
	if entry.RedirectURI != "https://app.example/callback" {
		t.Fatalf("RedirectURI = %q", entry.RedirectURI)
	}
	if entry.AuthScopeID != "scope-1" {
		t.Fatalf("AuthScopeID = %q, want scope-1", entry.AuthScopeID)
	}
	if entry.CodeVerifier != "verifier-1" {
		t.Fatalf("CodeVerifier = %q, want verifier-1", entry.CodeVerifier)
	}
}

func TestStateStoreTokenConsumedOnValidate(t *testing.T) {
	ss := NewStateStore()
	state, err := ss.Create("scope-1", "", "https://app.example/callback")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, ok := ss.Validate(state); !ok {
		t.Fatal("first Validate returned !ok")
	}
	if _, ok := ss.Validate(state); ok {
		t.Fatal("second Validate should fail after token consumption")
	}
}

func TestBuildAuthorizeURLUsesBoundRedirectURI(t *testing.T) {
	fm := NewFlowManager(nil, nil, "http://fallback.local")
	p := &store.OAuthProvider{
		AuthorizeURL: "https://provider.example/oauth/authorize",
		ClientID:     "client-1",
	}
	got, err := fm.buildAuthorizeURL(p, "state-1", "", "https://app.example/oauth/callback")
	if err != nil {
		t.Fatalf("buildAuthorizeURL: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse authorize URL: %v", err)
	}
	if u.Query().Get("redirect_uri") != "https://app.example/oauth/callback" {
		t.Fatalf("redirect_uri = %q", u.Query().Get("redirect_uri"))
	}
}

func TestExchangeCodeUsesBoundRedirectURI(t *testing.T) {
	var gotForm url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"access-1","token_type":"Bearer"}`)
	}))
	defer srv.Close()

	fm := NewFlowManager(nil, nil, "http://fallback.local")
	p := &store.OAuthProvider{
		TokenURL:    srv.URL,
		ClientID:    "client-1",
		RedirectURI: "https://stored.example/wrong",
	}
	_, err := fm.exchangeCode(context.Background(), p, "secret-1", "code-1", "verifier-1", "https://bound.example/callback")
	if err != nil {
		t.Fatalf("exchangeCode: %v", err)
	}
	if got := gotForm.Get("redirect_uri"); got != "https://bound.example/callback" {
		t.Fatalf("redirect_uri = %q", got)
	}
	for _, want := range []string{"client_secret=secret-1", "code_verifier=verifier-1"} {
		if !strings.Contains(gotForm.Encode(), want) {
			t.Fatalf("form missing %s: %s", want, gotForm.Encode())
		}
	}
}

func TestPostTokenRejectsSlackStyleOKFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":false,"error":"invalid_client"}`)
	}))
	defer srv.Close()

	fm := NewFlowManager(nil, nil, "http://fallback.local")
	_, err := fm.postToken(context.Background(), srv.URL, url.Values{"grant_type": {"authorization_code"}})
	if err == nil || !strings.Contains(err.Error(), "invalid_client") {
		t.Fatalf("postToken error = %v, want invalid_client", err)
	}
}

func TestPostTokenRejectsMissingAccessToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token_type":"Bearer"}`)
	}))
	defer srv.Close()

	fm := NewFlowManager(nil, nil, "http://fallback.local")
	_, err := fm.postToken(context.Background(), srv.URL, url.Values{"grant_type": {"authorization_code"}})
	if err == nil || !strings.Contains(err.Error(), "missing access_token") {
		t.Fatalf("postToken error = %v, want missing access_token", err)
	}
}
