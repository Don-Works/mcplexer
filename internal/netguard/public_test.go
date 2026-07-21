package netguard

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIsPublicIP(t *testing.T) {
	cases := []struct {
		ip     string
		public bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"127.0.0.1", false},
		{"127.1.2.3", false},
		{"10.0.0.1", false},
		{"172.16.5.5", false},
		{"172.31.255.255", false},
		{"172.32.0.1", true},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"100.127.255.255", false},
		{"100.128.0.1", true},
		{"::1", false},
		{"fe80::1", false},
		{"fc00::1", false},
		{"fd00::1", false},
		{"2606:4700:4700::1111", true},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Errorf("parse %q: invalid", c.ip)
			continue
		}
		if got := IsPublicIP(ip); got != c.public {
			t.Errorf("IsPublicIP(%s) = %v, want %v", c.ip, got, c.public)
		}
	}
}

func TestAssertPublicHost_RejectsLocalhost(t *testing.T) {
	cases := []string{
		"localhost",
		"LOCALHOST",
	}
	for _, h := range cases {
		if err := AssertPublicHost(context.Background(), h); err == nil {
			t.Errorf("AssertPublicHost(%q) = nil, want error", h)
		}
	}
}

func TestAssertPublicHost_AllowsPrivateFetchWhenExplicitlyEnabled(t *testing.T) {
	t.Setenv(AllowPrivateFetchEnv, "1")
	if err := AssertPublicHost(context.Background(), "127.0.0.1"); err != nil {
		t.Fatalf("AssertPublicHost with %s=1: %v", AllowPrivateFetchEnv, err)
	}
}

func TestPublicHTTPClientRejectsPrivateHostOnDial(t *testing.T) {
	t.Setenv(AllowPrivateFetchEnv, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should not be reached"))
	}))
	defer srv.Close()

	resp, err := NewPublicHTTPClient(0).Get(srv.URL)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if err == nil {
		t.Fatal("expected private host to be rejected")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("error = %v, want non-public host rejection", err)
	}
}

func TestPublicHTTPClientAllowsPrivateFetchWhenEnabled(t *testing.T) {
	t.Setenv(AllowPrivateFetchEnv, "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	resp, err := NewPublicHTTPClient(0).Get(srv.URL)
	if err != nil {
		t.Fatalf("Get with %s=1: %v", AllowPrivateFetchEnv, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}

func TestPublicHTTPClient_CheckRedirectRejectsPrivateHost(t *testing.T) {
	client := NewPublicHTTPClient(5 * time.Second)
	privateReq := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/somewhere", nil)
	err := client.CheckRedirect(privateReq, nil)
	if err == nil {
		t.Fatal("expected CheckRedirect to reject private host redirect")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Errorf("error = %q, want containing 'non-public'", err)
	}
}

func TestPublicHTTPClient_CheckRedirectRejectsLocalhost(t *testing.T) {
	client := NewPublicHTTPClient(5 * time.Second)
	req := httptest.NewRequest(http.MethodGet, "http://localhost/admin", nil)
	err := client.CheckRedirect(req, nil)
	if err == nil {
		t.Fatal("expected CheckRedirect to reject localhost redirect")
	}
}

func TestPinPublicHosts_RejectsPrivateIP(t *testing.T) {
	_, err := PinPublicHosts(context.Background(), "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for private IP")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Errorf("error = %q, want containing 'non-public'", err)
	}
}

func TestPinPublicHosts_RejectsMissingHost(t *testing.T) {
	_, err := PinPublicHosts(context.Background(), "")
	if err == nil {
		t.Fatal("expected error for empty host")
	}
}

func TestPinPublicHosts_RejectsLocalhost(t *testing.T) {
	_, err := PinPublicHosts(context.Background(), "localhost")
	if err == nil {
		t.Fatal("expected error for localhost")
	}
}

func TestPublicHTTPClient_AllowsPublicHost(t *testing.T) {
	t.Setenv(AllowPrivateFetchEnv, "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := NewPublicHTTPClient(5 * time.Second)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestPinnedResolver_PinsIPsFromContext(t *testing.T) {
	t.Setenv(AllowPrivateFetchEnv, "1")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	hostPort := strings.TrimPrefix(srv.URL, "http://")
	host, _, err := net.SplitHostPort(hostPort)
	if err != nil {
		t.Fatalf("split host:port: %v", err)
	}
	ip := net.ParseIP(host)
	if ip == nil {
		t.Fatalf("parse IP from %q: nil", host)
	}
	addrs := []net.IPAddr{{IP: ip}}

	ctx := context.WithValue(context.Background(), pinnedIPsKey, addrs)
	pinned := &pinnedResolver{}
	conn, err := pinned.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		t.Fatalf("DialContext with pinned IPs: %v", err)
	}
	_ = conn.Close()
}

func TestPinnedResolver_RejectsPrivateFallbackDial(t *testing.T) {
	pinned := &pinnedResolver{}
	_, err := pinned.DialContext(context.Background(), "tcp", "127.0.0.1:80")
	if err == nil {
		t.Fatal("expected error for private fallback dial")
	}
	if !strings.Contains(err.Error(), "non-public") {
		t.Errorf("error = %q, want containing 'non-public'", err)
	}
}
