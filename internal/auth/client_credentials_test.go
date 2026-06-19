package auth

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestClientCredentialsCache(t *testing.T) {
	const scope = "scope-1"

	tests := []struct {
		name      string
		expiresAt time.Time // relative offset applied to time.Now at set
		set       bool
		wantToken string
		wantHit   bool
	}{
		{
			name:      "valid token returned",
			set:       true,
			expiresAt: time.Now().Add(10 * time.Minute),
			wantToken: "tok-valid",
			wantHit:   true,
		},
		{
			name:      "token inside 60s buffer is a miss and evicted",
			set:       true,
			expiresAt: time.Now().Add(30 * time.Second),
			wantHit:   false,
		},
		{
			name:      "expired token is a miss",
			set:       true,
			expiresAt: time.Now().Add(-1 * time.Minute),
			wantHit:   false,
		},
		{
			name:    "absent token is a miss",
			set:     false,
			wantHit: false,
		},
		{
			name:      "token just past 60s buffer is a hit",
			set:       true,
			expiresAt: time.Now().Add(61 * time.Second),
			wantToken: "tok-edge",
			wantHit:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClientCredentialsCache()
			if tt.set {
				tok := tt.wantToken
				if tok == "" {
					tok = "tok-x"
				}
				c.set(scope, tok, tt.expiresAt)
			}
			got, ok := c.get(scope)
			if ok != tt.wantHit {
				t.Fatalf("get hit = %v, want %v", ok, tt.wantHit)
			}
			if ok && got != tt.wantToken {
				t.Fatalf("get token = %q, want %q", got, tt.wantToken)
			}
			// On a buffer/expiry miss the entry must be evicted.
			if tt.set && !tt.wantHit {
				c.mu.Lock()
				_, present := c.entries[scope]
				c.mu.Unlock()
				if present {
					t.Fatalf("expected expired entry to be evicted")
				}
			}
		})
	}

	t.Run("set overwrites existing entry", func(t *testing.T) {
		c := newClientCredentialsCache()
		c.set(scope, "old", time.Now().Add(10*time.Minute))
		c.set(scope, "new", time.Now().Add(10*time.Minute))
		got, ok := c.get(scope)
		if !ok || got != "new" {
			t.Fatalf("get = (%q, %v), want (new, true)", got, ok)
		}
	})
}

func TestExchangeClientCredentials(t *testing.T) {
	tests := []struct {
		name        string
		scopes      string
		status      int
		body        string
		wantErr     string
		wantToken   string
		wantExpires bool // whether ExpiresAt should be in the future
	}{
		{
			name:        "happy path returns token and expiry",
			scopes:      "a:read b:write",
			status:      http.StatusOK,
			body:        `{"access_token":"at-123","token_type":"Bearer","expires_in":3600}`,
			wantToken:   "at-123",
			wantExpires: true,
		},
		{
			name:      "non-200 wraps status and body",
			status:    http.StatusUnauthorized,
			body:      `{"error":"invalid_client"}`,
			wantErr:   "token endpoint returned 401",
			wantToken: "",
		},
		{
			name:      "server error wraps status",
			status:    http.StatusInternalServerError,
			body:      "boom",
			wantErr:   "token endpoint returned 500",
			wantToken: "",
		},
		{
			name:      "empty access_token errors",
			status:    http.StatusOK,
			body:      `{"access_token":"","token_type":"Bearer","expires_in":60}`,
			wantErr:   "empty access_token",
			wantToken: "",
		},
		{
			name:      "malformed json errors",
			status:    http.StatusOK,
			body:      `not json`,
			wantErr:   "parse token response",
			wantToken: "",
		},
	}

	const (
		clientID     = "cid"
		clientSecret = "csecret"
	)
	wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte(clientID+":"+clientSecret))

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotAuth, gotContentType string
			var gotForm string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuth = r.Header.Get("Authorization")
				gotContentType = r.Header.Get("Content-Type")
				b, _ := io.ReadAll(r.Body)
				gotForm = string(b)
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()

			token, expiresAt, err := exchangeClientCredentials(
				context.Background(), srv.URL, clientID, clientSecret, tt.scopes,
			)

			// Header construction is asserted on every call (the request
			// fires regardless of the canned response status).
			if gotAuth != wantBasic {
				t.Errorf("Authorization = %q, want %q", gotAuth, wantBasic)
			}
			if gotContentType != "application/x-www-form-urlencoded" {
				t.Errorf("Content-Type = %q", gotContentType)
			}
			if !strings.Contains(gotForm, "grant_type=client_credentials") {
				t.Errorf("form %q missing grant_type", gotForm)
			}
			if tt.scopes != "" && !strings.Contains(gotForm, "scope=") {
				t.Errorf("form %q missing scope param", gotForm)
			}
			if tt.scopes == "" && strings.Contains(gotForm, "scope=") {
				t.Errorf("form %q should omit empty scope", gotForm)
			}

			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want contains %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if token != tt.wantToken {
				t.Fatalf("token = %q, want %q", token, tt.wantToken)
			}
			if tt.wantExpires && !expiresAt.After(time.Now()) {
				t.Fatalf("expiresAt = %v, want future", expiresAt)
			}
		})
	}

	t.Run("omits scope param when empty", func(t *testing.T) {
		var gotForm string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			gotForm = string(b)
			_, _ = io.WriteString(w, `{"access_token":"at","expires_in":60}`)
		}))
		defer srv.Close()
		if _, _, err := exchangeClientCredentials(context.Background(), srv.URL, "c", "s", ""); err != nil {
			t.Fatalf("exchange: %v", err)
		}
		if strings.Contains(gotForm, "scope") {
			t.Fatalf("form %q should not contain scope", gotForm)
		}
	})

	t.Run("expires_in math approximates expires_in seconds", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, `{"access_token":"at","expires_in":120}`)
		}))
		defer srv.Close()
		before := time.Now()
		_, expiresAt, err := exchangeClientCredentials(context.Background(), srv.URL, "c", "s", "")
		if err != nil {
			t.Fatalf("exchange: %v", err)
		}
		delta := expiresAt.Sub(before)
		if delta < 119*time.Second || delta > 121*time.Second {
			t.Fatalf("expiry delta = %v, want ~120s", delta)
		}
	})
}
