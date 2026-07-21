//go:build linux

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemdUserServicePathUsesXDGConfigHome(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)

	want := filepath.Join(configHome, "systemd", "user", "mcplexer.service")
	if got := systemdUserServicePath(); got != want {
		t.Fatalf("systemdUserServicePath()=%q, want %q", got, want)
	}
}

func TestRenderSystemdUserService(t *testing.T) {
	unit, err := renderSystemdUserService(
		"/home/me/.mcplexer/bin/mcplexer",
		"127.0.0.1:3333",
		"/tmp/mcplexer.sock",
		"/home/me/.mcplexer/mcplexer.log",
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Description=MCPlexer daemon",
		"ExecStart=/home/me/.mcplexer/bin/mcplexer serve --mode=http --addr=127.0.0.1:3333 --socket=/tmp/mcplexer.sock --p2p",
		"Restart=on-failure",
		"Environment=MCPLEXER_LOG_PATH=/home/me/.mcplexer/mcplexer.log",
		"StandardOutput=journal",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
}

func TestReadSystemdUserAddr(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configHome)
	path := systemdUserServicePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	unit, err := renderSystemdUserService(
		"/home/me/.mcplexer/bin/mcplexer",
		"0.0.0.0:13333",
		"/tmp/mcplexer.sock",
		"/home/me/.mcplexer/mcplexer.log",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readSystemdUserAddr(); got != "0.0.0.0:13333" {
		t.Fatalf("readSystemdUserAddr()=%q, want 0.0.0.0:13333", got)
	}
}
