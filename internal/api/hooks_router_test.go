package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/don-works/mcplexer/internal/approval"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

// TestHookRoutesRejectWrongMethodThroughRouter prevents the SPA fallback
// from swallowing method errors on the two non-/api hook endpoints.
func TestHookRoutesRejectWrongMethodThroughRouter(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "hooks.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	router := NewRouter(RouterDeps{
		Store:           db,
		ApprovalManager: approval.NewManager(db, approval.NewBus()),
	})
	for _, path := range []string{"/v1/hooks/pretool", "/v1/hooks/session"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "127.0.0.1:43210"
			req.Host = "127.0.0.1:3333"
			rr := httptest.NewRecorder()
			router.ServeHTTP(rr, req)
			if rr.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status: want 405, got %d; body=%q", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("Allow"); got != http.MethodPost {
				t.Fatalf("Allow header: want POST, got %q", got)
			}
		})
	}
}

func TestHookRoutesRejectNonLoopbackThroughRouter(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "hooks.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	router := NewRouter(RouterDeps{
		Store:           db,
		ApprovalManager: approval.NewManager(db, approval.NewBus()),
	})
	for _, path := range []string{"/v1/hooks/pretool", "/v1/hooks/session"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"hook_event_name":"test"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Forwarded-For", "127.0.0.1")
			req.RemoteAddr = "203.0.113.20:43210"
			req.Host = "127.0.0.1:3333"
			rr := httptest.NewRecorder()

			router.ServeHTTP(rr, req)

			if rr.Code != http.StatusForbidden {
				t.Fatalf("status: want 403, got %d; body=%q", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), "loopback-only") {
				t.Fatalf("body should contain stable rejection, got %q", rr.Body.String())
			}
		})
	}
}

func TestHookRoutesRejectLocallyForwardedPublicRequest(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "hooks.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	router := NewRouter(RouterDeps{
		Store:           db,
		ApprovalManager: approval.NewManager(db, approval.NewBus()),
	})
	req := httptest.NewRequest(http.MethodPost, "/v1/hooks/pretool",
		strings.NewReader(`{"tool_name":"Read"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", "203.0.113.20")
	req.RemoteAddr = "127.0.0.1:43210"
	req.Host = "gateway.example.com"
	rr := httptest.NewRecorder()

	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status: want 403 for reverse-proxied hook request, got %d; body=%q", rr.Code, rr.Body.String())
	}
}

func TestHookRoutesAllowDirectLoopbackRequests(t *testing.T) {
	db, err := sqlite.New(context.Background(), filepath.Join(t.TempDir(), "hooks.db"))
	if err != nil {
		t.Fatalf("sqlite.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	router := NewRouter(RouterDeps{
		Store:           db,
		ApprovalManager: approval.NewManager(db, approval.NewBus()),
	})
	tests := []struct {
		path string
		body string
	}{
		{path: "/v1/hooks/pretool", body: `{"tool_name":"Read"}`},
		{path: "/v1/hooks/session", body: `{"hook_event_name":"FutureEvent"}`},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "127.0.0.1:43210"
			req.Host = "127.0.0.1:3333"
			rr := httptest.NewRecorder()

			router.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status: want 200, got %d; body=%q", rr.Code, rr.Body.String())
			}
		})
	}
}

func TestIsLoopbackRemoteAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{name: "IPv4", addr: "127.0.0.1:9000", want: true},
		{name: "IPv6", addr: "[::1]:9000", want: true},
		{name: "bare IPv6", addr: "::1", want: true},
		{name: "remote", addr: "203.0.113.20:9000", want: false},
		{name: "hostname rejected", addr: "localhost:9000", want: false},
		{name: "missing", addr: "", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isLoopbackRemoteAddr(tt.addr); got != tt.want {
				t.Fatalf("isLoopbackRemoteAddr(%q) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}
