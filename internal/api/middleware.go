package api

import (
	"context"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/audit"
	"github.com/google/uuid"
)

type contextKey string

const requestIDKey contextKey = "request_id"

const (
	maxRequestBodyBytes = int64(1 << 20) // 1 MiB
	defaultCSP          = "default-src 'self'; " +
		"base-uri 'self'; " +
		"frame-ancestors 'none'; " +
		"object-src 'none'; " +
		"script-src 'self'; " +
		"style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; " +
		"font-src 'self' data: https://fonts.gstatic.com; " +
		"img-src 'self' data:; " +
		"connect-src 'self'; " +
		"form-action 'self'"
)

// requestIDMiddleware injects a request ID into the request context
// and sets it as a response header. Honors an inbound X-Request-ID
// when the caller supplies a usable value (printable ASCII, ≤128
// chars) so upstream traces (e.g. a load balancer or browser fetch
// instrumentation) can join with gateway-side logs.
//
// The same id is stamped into ctx via audit.WithCorrelation, so every
// slog line and audit row produced by handlers downstream of this
// middleware shares the correlation_id key. Also echoed back on the
// X-Correlation-ID response header to make the join trivially
// inspectable in browser devtools.
func requestIDMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := normalizeRequestID(r.Header.Get("X-Request-ID"))
		if id == "" {
			id = uuid.New().String()
		}
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		ctx = audit.WithCorrelation(ctx, id)
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("X-Correlation-ID", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// maxRequestIDLen bounds inbound X-Request-ID values. A nonzero
// ceiling stops a misbehaving (or hostile) client from inflating slog
// + audit rows with megabyte-sized "ids". 128 is enough to fit a UUID,
// a ULID, or any sensible trace-id format with margin.
const maxRequestIDLen = 128

// normalizeRequestID trims and validates an inbound X-Request-ID. We
// accept only printable ASCII (no control chars, no high-bit bytes)
// because the value flows into log lines and HTTP response headers —
// neither tolerates arbitrary bytes safely. Returns "" when the input
// is unusable so the caller falls back to a fresh UUID.
func normalizeRequestID(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" || len(v) > maxRequestIDLen {
		return ""
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c < 0x20 || c > 0x7e {
			return ""
		}
	}
	return v
}

// loggingMiddleware logs each request with method, path, status, and duration.
func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		slog.Info("http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration_ms", time.Since(start).Milliseconds(),
			"request_id", r.Context().Value(requestIDKey),
		)
	})
}

// corsMiddleware allows requests from loopback origins plus any host listed
// in trustedHosts (bare hostnames; matched against the Origin's hostname).
func corsMiddleware(trustedHosts []string) func(http.Handler) http.Handler {
	allowed := normalizeHosts(trustedHosts)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if isAllowedOrigin(origin, allowed) {
				w.Header().Set("Vary", "Origin")
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
				w.Header().Set("Access-Control-Max-Age", "3600")
			}
			if r.Method == http.MethodOptions {
				if origin != "" && !isAllowedOrigin(origin, allowed) {
					writeError(w, http.StatusForbidden, "cross-origin browser request denied")
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeadersMiddleware applies hardened browser response headers.
func securityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", defaultCSP)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		if supportsCrossOriginOpenerPolicy(r) {
			h.Set("Cross-Origin-Opener-Policy", "same-origin")
		}
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		next.ServeHTTP(w, r)
	})
}

func supportsCrossOriginOpenerPolicy(r *http.Request) bool {
	if r == nil {
		return false
	}
	if r.TLS != nil || strings.EqualFold(r.URL.Scheme, "https") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return true
	}
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = strings.Trim(h, "[]")
	} else {
		host = strings.Trim(host, "[]")
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// browserOriginProtectionMiddleware blocks browser requests from origins
// that are neither loopback nor in the trustedHosts allowlist. This
// mitigates CSRF and DNS rebinding abuse against an unauthenticated local
// API while still allowing the UI to be served on a deliberately exposed
// hostname (e.g. an internal LAN box). Safe top-level navigations to UI
// routes are allowed so links from another site can open the SPA; API
// routes and non-navigation requests remain protected. The OAuth callback
// path is exempt because it receives cross-site redirects from providers.
func browserOriginProtectionMiddleware(trustedHosts []string) func(http.Handler) http.Handler {
	allowed := normalizeHosts(trustedHosts)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isUIPageNavigation(r) {
				next.ServeHTTP(w, r)
				return
			}

			if r.URL.Path == "/api/v1/oauth/callback" && r.Method == http.MethodGet {
				next.ServeHTTP(w, r)
				return
			}

			origin := r.Header.Get("Origin")
			if origin != "" && !isAllowedOrigin(origin, allowed) {
				writeError(w, http.StatusForbidden, "cross-origin browser request denied")
				return
			}

			if origin == "" {
				site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
				if site == "cross-site" {
					writeError(w, http.StatusForbidden, "cross-origin browser request denied")
					return
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

func isUIPageNavigation(r *http.Request) bool {
	if r == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
		return false
	}
	// When the browser supplies Fetch Metadata, trust it: a real top-level
	// navigation is mode=navigate + dest=document. Any other mode (cors,
	// no-cors, same-origin) is a subresource/fetch — never a page open — and
	// must NOT be waved through on the strength of a spoofable Accept header.
	// Sec-Fetch-* are forbidden request headers, so a page can't forge them.
	if mode := strings.TrimSpace(r.Header.Get("Sec-Fetch-Mode")); mode != "" {
		return strings.EqualFold(mode, "navigate") &&
			strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Dest")), "document")
	}
	// Only when Fetch Metadata is entirely absent (browser controllers and
	// some embedded webviews omit it on a real document navigation) do we
	// fall back to the navigation Accept header, which still distinguishes
	// the static SPA shell from API fetches.
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/html")
}

// requestBodyLimitMiddleware applies a global max body size for request handlers.
func requestBodyLimitMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasRequestBody(r) {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBodyBytes)
		}
		next.ServeHTTP(w, r)
	})
}

// requireJSONContentTypeMiddleware enforces application/json for mutating requests
// that include a request body. File-upload routes (paths ending in
// /attachments) accept multipart/form-data as well — those handlers
// validate the multipart envelope themselves.
func requireJSONContentTypeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !expectsJSONBody(r.Method) || !hasRequestBody(r) {
			next.ServeHTTP(w, r)
			return
		}

		contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
		mediaType, _, err := mime.ParseMediaType(contentType)
		if err != nil {
			writeError(w, http.StatusUnsupportedMediaType, "content-type must be application/json")
			return
		}
		if mediaType == "application/json" {
			next.ServeHTTP(w, r)
			return
		}
		if mediaType == "multipart/form-data" && pathAcceptsMultipart(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		writeError(w, http.StatusUnsupportedMediaType, "content-type must be application/json")
	})
}

// pathAcceptsMultipart returns true for routes that legitimately accept
// multipart/form-data — today that's the attachment upload surface. Kept
// as an allowlist rather than a global exception so accidental opening
// of binary uploads on JSON endpoints stays caught.
func pathAcceptsMultipart(p string) bool {
	return strings.HasSuffix(p, "/attachments")
}

func expectsJSONBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch:
		return true
	default:
		return false
	}
}

func hasRequestBody(r *http.Request) bool {
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return false
	}
	if r.ContentLength > 0 {
		return true
	}
	return strings.TrimSpace(r.Header.Get("Transfer-Encoding")) != ""
}

// isAllowedOrigin reports whether the Origin header value is acceptable for
// browser requests against the local API. Loopback hosts are always
// allowed; additional bare hostnames may be passed in via the allowed list.
func isAllowedOrigin(origin string, allowed []string) bool {
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return true
	}
	return slices.Contains(allowed, host)
}

// normalizeHosts lowercases, trims, converts origin-ish values to bare
// hostnames, and drops empty entries. Config should already normalize env
// values, but tests and future callers may pass trusted hosts directly.
func normalizeHosts(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, h := range in {
		h = normalizeAllowedHost(h)
		if h == "" {
			continue
		}
		out = append(out, h)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeAllowedHost(raw string) string {
	h := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if h == "" {
		return ""
	}
	if strings.Contains(h, "://") {
		if u, err := url.Parse(h); err == nil && u.Hostname() != "" {
			return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
		}
		return ""
	}
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if u, err := url.Parse("http://" + h); err == nil && u.Hostname() != "" {
		return strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	}
	return strings.ToLower(strings.Trim(strings.TrimSuffix(h, "."), "[]"))
}

// statusWriter captures the HTTP status code for logging.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush delegates to the underlying ResponseWriter so SSE handlers work.
func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
