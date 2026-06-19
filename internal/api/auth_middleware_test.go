package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newAuthHarness(t *testing.T, token string) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/secrets", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("alive"))
	})
	mux.HandleFunc("GET /api/v1/oauth/callback", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("callback"))
	})
	mux.HandleFunc("GET /not-api", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("public"))
	})
	mux.HandleFunc("GET /api/p2p/peers", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("peers"))
	})
	mux.HandleFunc("POST /api/skills/demo/run", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("skill"))
	})
	mux.HandleFunc("POST /api/v1/googlechat/events", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("googlechat"))
	})
	return apiTokenAuthMiddleware(token)(mux)
}

func TestAuthMiddleware_Rejects_Unauthenticated(t *testing.T) {
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/secrets", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("missing WWW-Authenticate Bearer header, got %q", got)
	}
}

func TestAuthMiddleware_Accepts_BearerHeader(t *testing.T) {
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/secrets", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200; body = %q", rec.Code, rec.Body.String())
	}
}

func TestAuthMiddleware_Accepts_Cookie(t *testing.T) {
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/secrets", nil)
	req.AddCookie(&http.Cookie{Name: SessionCookieName, Value: testToken})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestAuthMiddleware_Accepts_QueryParam_ForSSE(t *testing.T) {
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/secrets?api_token="+testToken, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 for query-param auth (used by EventSource)", rec.Code)
	}
}

func TestAuthMiddleware_Rejects_WrongToken(t *testing.T) {
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/secrets", nil)
	req.Header.Set("Authorization", "Bearer wrong-token-value")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestAuthMiddleware_Exempts_Health(t *testing.T) {
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("/api/v1/health should be public, got %d", rec.Code)
	}
}

func TestAuthMiddleware_Exempts_OAuthCallback(t *testing.T) {
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/oauth/callback", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("oauth callback should be public, got %d", rec.Code)
	}
}

func TestAuthMiddleware_NonAPIPathSkipsAuth(t *testing.T) {
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodGet, "/not-api", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("non-API path should not require auth, got %d", rec.Code)
	}
}

func TestAuthMiddleware_GuardsP2PRoutes(t *testing.T) {
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodGet, "/api/p2p/peers", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/api/p2p/peers should require auth, got %d", rec.Code)
	}
}

func TestAuthMiddleware_GuardsSkillRunRoutes(t *testing.T) {
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodPost, "/api/skills/demo/run", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/api/skills routes should require auth, got %d", rec.Code)
	}
}

func TestAuthMiddleware_GoogleChatEvents_RequiresAPITokenWhenJWTValidationExplicitlyDisabled(t *testing.T) {
	t.Setenv("GOOGLECHAT_DISABLE_JWT_VALIDATION", "true")
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/googlechat/events", nil)
	req.Header.Set("Authorization", "Bearer google-signed-jwt")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("googlechat events should require api token when JWT validation is disabled, got %d", rec.Code)
	}
}

func TestAuthMiddleware_GoogleChatEvents_AuthExemptByDefault(t *testing.T) {
	// Default: JWT validation is ON, so the events endpoint is auth-exempt.
	// The handler's own JWT verifier will reject the call later, but the
	// auth middleware should let it through.
	h := newAuthHarness(t, testToken)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/googlechat/events", nil)
	req.Header.Set("Authorization", "Bearer google-signed-jwt")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("googlechat events should be auth-exempt by default (fail-closed), got %d", rec.Code)
	}
}
