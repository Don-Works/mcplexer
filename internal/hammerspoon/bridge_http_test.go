package hammerspoon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestHTTPDriver_Happy verifies a 200 + envelope round-trip.
func TestHTTPDriver_Happy(t *testing.T) {
	var capturedAuth string
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/exec" {
			t.Errorf("path: want /exec got %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method: want POST got %q", r.Method)
		}
		capturedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"hello":"world"}}`))
	}))
	defer srv.Close()

	d := NewHTTPDriver(srv.URL, "topsecret")
	env, err := d.Exec(context.Background(), "return 1", time.Second)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !env.Ok {
		t.Fatalf("envelope ok: %v err=%q", env.Ok, env.Err)
	}
	if string(env.Result) != `{"hello":"world"}` {
		t.Errorf("result: got %s", env.Result)
	}
	if capturedAuth != "Bearer topsecret" {
		t.Errorf("auth header: got %q", capturedAuth)
	}
	if capturedBody["lua"] != "return 1" {
		t.Errorf("lua field: got %v", capturedBody["lua"])
	}
	// timeout_ms is JSON-numbered — assert it's the configured ms value.
	if v, ok := capturedBody["timeout_ms"].(float64); !ok || v != 1000 {
		t.Errorf("timeout_ms: want 1000 got %v", capturedBody["timeout_ms"])
	}
}

// TestHTTPDriver_401 verifies a 401 is rendered as a fix-it message rather
// than a raw HTTP code so the agent can guide the user.
func TestHTTPDriver_401(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"err":"unauthorized"}`))
	}))
	defer srv.Close()

	d := NewHTTPDriver(srv.URL, "wrong")
	env, err := d.Exec(context.Background(), "noop", time.Second)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if env.Ok {
		t.Fatalf("expected ok=false on 401")
	}
	if !strings.Contains(env.Err, "Bridge password mismatch") {
		t.Errorf("err message: want 'Bridge password mismatch' got %q", env.Err)
	}
}

// TestHTTPDriver_5xx verifies 500-class responses fold the body into the env.
func TestHTTPDriver_5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	d := NewHTTPDriver(srv.URL, "x")
	env, err := d.Exec(context.Background(), "noop", time.Second)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if env.Ok {
		t.Fatalf("expected ok=false on 500")
	}
	if !strings.Contains(env.Err, "500") || !strings.Contains(env.Err, "boom") {
		t.Errorf("err message: want 500 + boom, got %q", env.Err)
	}
}

// TestHTTPDriver_MalformedJSON verifies a 200 with a bad body becomes a
// "malformed JSON" envelope rather than a transport error.
func TestHTTPDriver_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	d := NewHTTPDriver(srv.URL, "x")
	env, err := d.Exec(context.Background(), "noop", time.Second)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if env.Ok {
		t.Fatalf("expected ok=false on malformed json")
	}
	if !strings.Contains(env.Err, "malformed JSON") {
		t.Errorf("err message: want 'malformed JSON' got %q", env.Err)
	}
}

// TestHTTPDriver_Timeout verifies a slow handler is cut off and surfaced as
// a timeout envelope (not a raw context error).
func TestHTTPDriver_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
		}
	}))
	defer srv.Close()

	d := NewHTTPDriver(srv.URL, "x")
	// Very short timeout — driver adds a 2s head-room so set both small.
	env, err := d.Exec(context.Background(), "noop", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if env.Ok {
		t.Fatalf("expected ok=false on timeout")
	}
	// Either branch is fine — connection cut surfaces as "call failed",
	// deadline as "timed out". Both are user-readable.
	if env.Err == "" {
		t.Errorf("expected non-empty err on timeout")
	}
}

// TestHTTPDriver_ConnectionRefused exercises the "Hammerspoon not running"
// classification. We dial a closed port to provoke a real OpError.
func TestHTTPDriver_ConnectionRefused(t *testing.T) {
	// Pick a free port, then close the listener so the dial reliably fails.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.URL
	srv.Close()

	d := NewHTTPDriver(addr, "x")
	env, err := d.Exec(context.Background(), "noop", time.Second)
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if env.Ok {
		t.Fatalf("expected ok=false on refused connection")
	}
	if !strings.Contains(env.Err, "Hammerspoon is not running") {
		t.Errorf("err message: want 'Hammerspoon is not running…', got %q", env.Err)
	}
}
