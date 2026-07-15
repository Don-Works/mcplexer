package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/audit"
)

func TestSecurityHeadersMiddleware(t *testing.T) {
	h := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	req := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/health", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff header, got %q", got)
	}
	if got := rr.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected DENY frame header, got %q", got)
	}
	if got := rr.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("expected CSP header to be set")
	}
	if got := rr.Header().Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
		t.Fatalf("expected COOP header on localhost, got %q", got)
	}
}

func TestSecurityHeadersMiddlewareCOOPTrustworthyOrigins(t *testing.T) {
	h := securityHeadersMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	tests := []struct {
		name    string
		target  string
		headers map[string]string
		want    string
	}{
		{
			name:   "loopback ipv4 gets coop",
			target: "http://127.0.0.1:13333/api/v1/health",
			want:   "same-origin",
		},
		{
			name:   "loopback ipv6 gets coop",
			target: "http://[::1]:13333/api/v1/health",
			want:   "same-origin",
		},
		{
			name:   "https lan host gets coop",
			target: "https://ai-gateway:13333/api/v1/health",
			want:   "same-origin",
		},
		{
			name:    "forwarded https gets coop",
			target:  "http://ai-gateway:13333/api/v1/health",
			headers: map[string]string{"X-Forwarded-Proto": "https"},
			want:    "same-origin",
		},
		{
			name:   "plain http lan host skips coop",
			target: "http://ai-gateway:13333/api/v1/health",
			want:   "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tc.target, nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if got := rr.Header().Get("Cross-Origin-Opener-Policy"); got != tc.want {
				t.Fatalf("COOP header = %q, want %q", got, tc.want)
			}
			if got := rr.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
				t.Fatalf("CORP header = %q, want same-origin", got)
			}
		})
	}
}

func TestBrowserOriginProtectionMiddleware(t *testing.T) {
	noop := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := browserOriginProtectionMiddleware(nil)(noop)

	t.Run("allows localhost origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/workspaces", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("expected %d, got %d", http.StatusNoContent, rr.Code)
		}
	})

	t.Run("blocks non-local origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/workspaces", nil)
		req.Header.Set("Origin", "https://evil.example")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected %d, got %d", http.StatusForbidden, rr.Code)
		}
	})

	t.Run("blocks cross-site fetch hint", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/workspaces", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected %d, got %d", http.StatusForbidden, rr.Code)
		}
	})

	t.Run("allows cross-site top-level navigation to UI", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/monitoring", nil)
		req.Header.Set("Origin", "https://chat.google.com")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Dest", "document")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("expected %d, got %d", http.StatusNoContent, rr.Code)
		}
	})

	t.Run("allows UI navigation without fetch metadata", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/monitoring", nil)
		req.Header.Set("Origin", "chrome-extension://browser-controller")
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("expected %d, got %d", http.StatusNoContent, rr.Code)
		}
	})

	t.Run("blocks cross-site top-level navigation to API", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/workspaces", nil)
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Mode", "navigate")
		req.Header.Set("Sec-Fetch-Dest", "document")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected %d, got %d", http.StatusForbidden, rr.Code)
		}
	})

	t.Run("blocks cross-site subresource request to UI route", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/monitoring", nil)
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("Sec-Fetch-Dest", "empty")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected %d, got %d", http.StatusForbidden, rr.Code)
		}
	})

	t.Run("Accept text/html can't override explicit non-navigation fetch metadata", func(t *testing.T) {
		// A spoofable Accept header must not wave through a request the
		// browser itself labelled a cross-site fetch (mode=cors).
		req := httptest.NewRequest(http.MethodGet, "http://localhost/monitoring", nil)
		req.Header.Set("Origin", "https://evil.example")
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		req.Header.Set("Sec-Fetch-Mode", "cors")
		req.Header.Set("Sec-Fetch-Dest", "empty")
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected %d, got %d", http.StatusForbidden, rr.Code)
		}
	})

	t.Run("allows cross-site GET to oauth callback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/oauth/callback?state=abc&code=xyz", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("expected %d, got %d", http.StatusNoContent, rr.Code)
		}
	})

	t.Run("blocks cross-site POST to oauth callback", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/oauth/callback", nil)
		req.Header.Set("Sec-Fetch-Site", "cross-site")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected %d, got %d", http.StatusForbidden, rr.Code)
		}
	})

	t.Run("allows trusted-host origin", func(t *testing.T) {
		trusted := browserOriginProtectionMiddleware([]string{"ai-gateway", "  HOST.lan.  "})(noop)
		req := httptest.NewRequest(http.MethodGet, "http://ai-gateway:13333/api/v1/workspaces", nil)
		req.Header.Set("Origin", "http://ai-gateway:13333")
		rr := httptest.NewRecorder()
		trusted.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("trusted host blocked: expected %d, got %d", http.StatusNoContent, rr.Code)
		}
	})

	t.Run("trusted-host list still blocks others", func(t *testing.T) {
		trusted := browserOriginProtectionMiddleware([]string{"ai-gateway"})(noop)
		req := httptest.NewRequest(http.MethodGet, "http://ai-gateway:13333/api/v1/workspaces", nil)
		req.Header.Set("Origin", "https://evil.example")
		rr := httptest.NewRecorder()
		trusted.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected %d, got %d", http.StatusForbidden, rr.Code)
		}
	})
}

func TestRequireJSONContentTypeMiddleware(t *testing.T) {
	h := requireJSONContentTypeMiddleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	t.Run("rejects non-json content type", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/workspaces", strings.NewReader(`{"name":"x"}`))
		req.Header.Set("Content-Type", "text/plain")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnsupportedMediaType {
			t.Fatalf("expected %d, got %d", http.StatusUnsupportedMediaType, rr.Code)
		}
	})

	t.Run("allows json content type", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/workspaces", strings.NewReader(`{"name":"x"}`))
		req.Header.Set("Content-Type", "application/json; charset=utf-8")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("expected %d, got %d", http.StatusNoContent, rr.Code)
		}
	})

	t.Run("allows empty post body without content type", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "http://localhost/api/v1/cache/flush", nil)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("expected %d, got %d", http.StatusNoContent, rr.Code)
		}
	})
}

func TestCORSMiddleware(t *testing.T) {
	noop := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	h := corsMiddleware(nil)(noop)

	t.Run("local origin gets cors headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/workspaces", nil)
		req.Header.Set("Origin", "http://localhost:5173")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://localhost:5173" {
			t.Fatalf("unexpected allow-origin header: %q", got)
		}
	})

	t.Run("blocks non-local preflight", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "http://localhost/api/v1/workspaces", nil)
		req.Header.Set("Origin", "https://evil.example")
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("expected %d, got %d", http.StatusForbidden, rr.Code)
		}
	})

	t.Run("trusted host preflight allowed", func(t *testing.T) {
		trusted := corsMiddleware([]string{"ai-gateway"})(noop)
		req := httptest.NewRequest(http.MethodOptions, "http://ai-gateway:13333/api/v1/workspaces", nil)
		req.Header.Set("Origin", "http://ai-gateway:13333")
		rr := httptest.NewRecorder()
		trusted.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("expected %d, got %d", http.StatusNoContent, rr.Code)
		}
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://ai-gateway:13333" {
			t.Fatalf("unexpected allow-origin header: %q", got)
		}
	})

	t.Run("trusted browser origin form is normalized", func(t *testing.T) {
		trusted := corsMiddleware([]string{"https://my-mac.tailnet-name.ts.net:3333/app"})(noop)
		req := httptest.NewRequest(http.MethodOptions, "http://my-mac.tailnet-name.ts.net:3333/api/v1/push/status", nil)
		req.Header.Set("Origin", "https://my-mac.tailnet-name.ts.net")
		rr := httptest.NewRecorder()
		trusted.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("expected %d, got %d", http.StatusNoContent, rr.Code)
		}
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://my-mac.tailnet-name.ts.net" {
			t.Fatalf("unexpected allow-origin header: %q", got)
		}
	})
}

func TestRouterPreflightBypassesAuth(t *testing.T) {
	h := NewRouter(RouterDeps{
		APIToken:     "secret-token",
		TrustedHosts: []string{"https://my-mac.tailnet-name.ts.net:13333/app"},
	})
	req := httptest.NewRequest(http.MethodOptions, "http://my-mac.tailnet-name.ts.net:13333/api/v1/push/subscribe", nil)
	req.Header.Set("Origin", "https://my-mac.tailnet-name.ts.net")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected %d, got %d body=%q", http.StatusNoContent, rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "https://my-mac.tailnet-name.ts.net" {
		t.Fatalf("unexpected allow-origin header: %q", got)
	}
}

func TestRequestIDMiddleware(t *testing.T) {
	tests := []struct {
		name      string
		inbound   string
		wantSeeds bool   // true when we expect the value to be honored
		wantValue string // when wantSeeds, the exact value to expect
	}{
		{
			name:      "no inbound mints fresh",
			inbound:   "",
			wantSeeds: false,
		},
		{
			name:      "honors X-Request-ID input",
			inbound:   "client-trace-abc",
			wantSeeds: true,
			wantValue: "client-trace-abc",
		},
		{
			name:      "rejects control chars",
			inbound:   "bad\x00id",
			wantSeeds: false,
		},
		{
			name:      "rejects whitespace-only",
			inbound:   "   ",
			wantSeeds: false,
		},
		{
			name:      "rejects oversize",
			inbound:   strings.Repeat("a", 129),
			wantSeeds: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var sawCorrelation string
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sawCorrelation = audit.FromCtx(r.Context())
				w.WriteHeader(http.StatusNoContent)
			})
			req := httptest.NewRequest(http.MethodGet, "http://localhost/api/v1/health", nil)
			if tc.inbound != "" {
				req.Header.Set("X-Request-ID", tc.inbound)
			}
			rr := httptest.NewRecorder()
			requestIDMiddleware(next).ServeHTTP(rr, req)

			if rr.Code != http.StatusNoContent {
				t.Fatalf("status = %d", rr.Code)
			}
			got := rr.Header().Get("X-Request-ID")
			gotCorr := rr.Header().Get("X-Correlation-ID")
			if got == "" {
				t.Fatal("X-Request-ID response header empty")
			}
			if gotCorr != got {
				t.Fatalf("X-Correlation-ID (%q) != X-Request-ID (%q)", gotCorr, got)
			}
			if sawCorrelation != got {
				t.Fatalf("ctx correlation_id (%q) != response id (%q)", sawCorrelation, got)
			}
			if tc.wantSeeds && got != tc.wantValue {
				t.Fatalf("honored id mismatch: got %q want %q", got, tc.wantValue)
			}
			if !tc.wantSeeds && got == tc.inbound && tc.inbound != "" {
				t.Fatalf("unexpectedly honored bad inbound %q", tc.inbound)
			}
		})
	}
}

func TestNormalizeRequestID(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"  abc  ", "abc"},
		{"ok-123_ABC", "ok-123_ABC"},
		{"bad\nline", ""},
		{"bad\x7fchar", ""},
		{strings.Repeat("x", 128), strings.Repeat("x", 128)},
		{strings.Repeat("x", 129), ""},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizeRequestID(tc.in); got != tc.want {
				t.Fatalf("normalizeRequestID(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
