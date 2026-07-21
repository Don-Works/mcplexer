package runner

import (
	"strings"
	"testing"
)

// TestIsSafeOutboundURL_BlockCategories exercises every block category
// the SSRF guard implements. Each public-IP / public-hostname case is
// expected to pass; everything else returns an error.
func TestIsSafeOutboundURL_BlockCategories(t *testing.T) {
	cases := []struct {
		name      string
		url       string
		wantBlock bool
		// wantSubstr is checked when wantBlock=true to make sure the
		// operator-visible error mentions the actual reason.
		wantSubstr string
	}{
		{"public https hostname", "https://hooks.example.com/run", false, ""},
		{"public http hostname", "http://example.com", false, ""},

		// Scheme blocks.
		{"file scheme", "file:///etc/passwd", true, "scheme"},
		{"gopher scheme", "gopher://example.com/", true, "scheme"},
		{"ftp scheme", "ftp://example.com/", true, "scheme"},

		// Loopback.
		{"loopback v4", "http://127.0.0.1/run", true, "loopback"},
		{"loopback v4 alt", "http://127.0.0.53/", true, "loopback"},
		{"loopback v6", "http://[::1]/", true, "loopback"},
		{"localhost hostname", "http://localhost/", true, "loopback"},
		{"localhost case", "http://LOCALHOST/", true, "loopback"},

		// Link-local (cloud metadata, IPv6 link-local).
		{"link-local v4", "http://169.254.169.254/latest/meta-data/", true, "link-local"},
		{"link-local v6", "http://[fe80::1]/", true, "link-local"},

		// RFC1918 / private.
		{"10/8 IP", "http://10.0.0.5/", true, "private"},
		{"172.16/12 IP", "http://172.16.5.6/", true, "private"},
		{"192.168/16 IP", "http://192.168.1.1/", true, "private"},
		{"ULA v6", "http://[fc00::1]/", true, "private"},

		// .local / .internal hostnames.
		{".local hostname", "http://homeassistant.local/", true, ".local"},
		{".internal hostname", "http://api.svc.internal/", true, ".internal"},

		// Unspecified.
		{"unspecified v4", "http://0.0.0.0/", true, "unspecified"},

		// Malformed.
		{"no host", "http:///run", true, ""},
		{"empty", "", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := isSafeOutboundURL(tc.url)
			if tc.wantBlock {
				if err == nil {
					t.Fatalf("expected block for %q", tc.url)
				}
				if tc.wantSubstr != "" && !strings.Contains(err.Error(), tc.wantSubstr) {
					t.Fatalf("error %q missing substring %q", err.Error(), tc.wantSubstr)
				}
				return
			}
			if err != nil {
				t.Fatalf("expected pass for %q, got %v", tc.url, err)
			}
		})
	}
}
