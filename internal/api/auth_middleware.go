package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// SessionCookieName is the cookie set on SPA loads carrying the API token.
const SessionCookieName = "mcplexer_session"

// AuthExempt paths skip token authentication. Each entry is matched as an
// exact path. OAuth callbacks are exempt because they receive cross-site
// redirects from external IDPs; health is exempt so liveness probes work
// without secret material.
var authExemptPaths = map[string]struct{}{
	"/api/v1/oauth/callback": {},
	"/api/v1/health":         {},
	// Conventional probe alias; mirrors /api/v1/health without auth.
	"/healthz": {},
}

// apiTokenAuthMiddleware enforces that every /api/v1/* and /api/p2p/* request
// carries a valid API token, supplied either as an Authorization: Bearer
// header (for CLI/desktop callers) or as a session cookie (for the SPA).
//
// Non-API paths (e.g. the SPA HTML/JS bundle) are not gated here — the SPA
// fallback is unauthenticated so that the browser can load index.html and
// receive the session cookie that authenticates subsequent API calls.
func apiTokenAuthMiddleware(token string) func(http.Handler) http.Handler {
	tokenBytes := []byte(token)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isAPIPath(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method == http.MethodOptions {
				next.ServeHTTP(w, r)
				return
			}
			if isAuthExempt(r) {
				next.ServeHTTP(w, r)
				return
			}

			if presented, ok := extractToken(r); ok {
				if subtle.ConstantTimeCompare([]byte(presented), tokenBytes) == 1 {
					next.ServeHTTP(w, r)
					return
				}
			}

			w.Header().Set("WWW-Authenticate", `Bearer realm="mcplexer"`)
			writeError(w, http.StatusUnauthorized, "missing or invalid api token")
		})
	}
}

func isAuthExempt(r *http.Request) bool {
	if _, ok := authExemptPaths[r.URL.Path]; ok {
		return true
	}
	return r.Method == http.MethodPost &&
		r.URL.Path == "/api/v1/googlechat/events" &&
		requireJWTValidation()
}

func isAPIPath(p string) bool {
	return strings.HasPrefix(p, "/api/v1/") ||
		strings.HasPrefix(p, "/api/p2p/") ||
		strings.HasPrefix(p, "/api/skills/") ||
		p == "/api/v1" ||
		p == "/api/p2p" ||
		p == "/api/skills"
}

func extractToken(r *http.Request) (string, bool) {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if auth != "" {
		const prefix = "Bearer "
		if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
			tok := strings.TrimSpace(auth[len(prefix):])
			if tok != "" {
				return tok, true
			}
		}
	}
	if c, err := r.Cookie(SessionCookieName); err == nil && c.Value != "" {
		return c.Value, true
	}
	if q := r.URL.Query().Get("api_token"); q != "" {
		// Allowed only because EventSource has no header API; the browser
		// origin middleware blocks cross-site SSE attempts.
		return q, true
	}
	return "", false
}

// setSessionCookie attaches the API token as a session cookie. Used by the
// SPA fallback when serving index.html so that the browser can hit the API
// without explicitly handling the token in JS.
func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   false, // localhost-only listener
		SameSite: http.SameSiteStrictMode,
	})
}
