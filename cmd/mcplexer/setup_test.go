package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestSetupClientURLFromAddr(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "empty", in: "", want: ""},
		{name: "wildcard ipv4", in: "0.0.0.0:13333", want: "http://127.0.0.1:13333"},
		{name: "port only", in: ":13333", want: "http://127.0.0.1:13333"},
		{name: "loopback", in: "127.0.0.1:3333", want: "http://127.0.0.1:3333"},
		{name: "already url", in: "http://localhost:3333", want: "http://localhost:3333"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := setupClientURLFromAddr(tt.in); got != tt.want {
				t.Fatalf("setupClientURLFromAddr(%q)=%q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestReadLaunchdAddr(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/example/.mcplexer/bin/mcplexer</string>
    <string>serve</string>
    <string>--addr=0.0.0.0:13333</string>
  </array>
</dict>
</plist>`
	if err := os.WriteFile(filepath.Join(dir, "com.mcplexer.daemon.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readLaunchdAddr(); got != "0.0.0.0:13333" {
		t.Fatalf("readLaunchdAddr()=%q, want 0.0.0.0:13333", got)
	}
}

func TestParseAddrArg(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plist string",
			in:   "<string>--addr=0.0.0.0:13333</string>",
			want: "0.0.0.0:13333",
		},
		{
			name: "systemd exec start",
			in:   "ExecStart=/home/me/.mcplexer/bin/mcplexer serve --mode=http --addr=127.0.0.1:4444 --socket=/tmp/mcplexer.sock",
			want: "127.0.0.1:4444",
		},
		{
			name: "absent",
			in:   "ExecStart=/home/me/.mcplexer/bin/mcplexer serve",
			want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseAddrArg(tt.in); got != tt.want {
				t.Fatalf("parseAddrArg()=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestSetupHTTPBaseURLUsesRespondingEnvAddr(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("MCPLEXER_HTTP_ADDR", srv.URL)

	if got := setupHTTPBaseURL(); got != srv.URL {
		t.Fatalf("setupHTTPBaseURL()=%q, want %q", got, srv.URL)
	}
}
