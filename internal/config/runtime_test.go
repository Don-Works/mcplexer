package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRuntimeInfoRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in := RuntimeInfo{HTTPAddr: "0.0.0.0:13333", PublicURL: "https://x.ts.net", SocketPath: "/tmp/m.sock", PID: 4242, Version: "v0.5.1"}
	if err := WriteRuntimeInfo(dir, in); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(RuntimeInfoPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("runtime.json mode = %o, want 600", fi.Mode().Perm())
	}
	got, err := ReadRuntimeInfo(dir)
	if err != nil || got == nil {
		t.Fatalf("read: %v, %v", got, err)
	}
	if got.HTTPAddr != in.HTTPAddr || got.PublicURL != in.PublicURL || got.PID != in.PID {
		t.Errorf("round trip mismatch: %+v", got)
	}
}

func TestReadRuntimeInfoMissingIsNil(t *testing.T) {
	got, err := ReadRuntimeInfo(t.TempDir())
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if got != nil {
		t.Fatalf("missing file should return nil, got %+v", got)
	}
}

func TestRemoveRuntimeInfo(t *testing.T) {
	dir := t.TempDir()
	_ = WriteRuntimeInfo(dir, RuntimeInfo{HTTPAddr: ":3333"})
	RemoveRuntimeInfo(dir)
	if _, err := os.Stat(RuntimeInfoPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("runtime.json should be gone, stat err = %v", err)
	}
	// Idempotent: removing a missing file is a no-op.
	RemoveRuntimeInfo(dir)
}

func TestDashboardURL(t *testing.T) {
	cases := []struct {
		name, addr, public, want string
	}{
		{"public wins", "0.0.0.0:13333", "https://x.ts.net", "https://x.ts.net"},
		{"public trailing slash", ":3333", "https://x.ts.net/", "https://x.ts.net"},
		{"wildcard to localhost", "0.0.0.0:13333", "", "http://localhost:13333"},
		{"ipv6 wildcard", "[::]:13333", "", "http://localhost:13333"},
		{"loopback keeps host", "127.0.0.1:3333", "", "http://127.0.0.1:3333"},
		{"bare colon port", ":13333", "", "http://localhost:13333"},
		{"empty addr falls back", "", "", "http://localhost:3333"},
		{"garbage falls back", "not-an-addr", "", "http://localhost:3333"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DashboardURL(c.addr, c.public); got != c.want {
				t.Errorf("DashboardURL(%q,%q) = %q, want %q", c.addr, c.public, got, c.want)
			}
		})
	}
}

func TestRuntimeInfoPathJoins(t *testing.T) {
	if got := RuntimeInfoPath("/data"); got != filepath.Join("/data", "runtime.json") {
		t.Errorf("path = %q", got)
	}
}
