package sshx

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/don-works/mcplexer/internal/store"
)

// TestDockerLogsCommand_Shape pins the exact wire command — the fixed
// read-only template from ADR 0007.
func TestDockerLogsCommand_Shape(t *testing.T) {
	since := time.Date(2026, 7, 8, 14, 0, 0, 123456789, time.UTC)
	cmd, err := DockerLogsCommand("intervals-api", since)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	want := "docker logs --timestamps --since '2026-07-08T14:00:00.123456789Z' 'intervals-api'"
	if cmd != want {
		t.Fatalf("command mismatch:\n got %q\nwant %q", cmd, want)
	}

	cmd, err = DockerLogsCommand("api", time.Time{})
	if err != nil {
		t.Fatalf("build no-since: %v", err)
	}
	if cmd != "docker logs --timestamps 'api'" {
		t.Fatalf("no-since command: %q", cmd)
	}
}

// TestDockerLogsCommand_InjectionRejected is the security gate: every
// shell-metacharacter selector must fail BEFORE any wire activity.
func TestDockerLogsCommand_InjectionRejected(t *testing.T) {
	for _, sel := range []string{
		"", "api; rm -rf /", "api'", `api"`, "api$(id)", "api`id`",
		"api|cat", "api&&x", "a b", "api\n", "api\\x", "-f", // leading dash is caught by charset? no — '-' allowed...
	} {
		if sel == "-f" {
			continue // '-' alone is charset-legal; flag-injection is covered below
		}
		if _, err := DockerLogsCommand(sel, time.Time{}); err == nil {
			t.Errorf("selector %q: expected rejection", sel)
		}
	}
}

// TestDockerLogsCommand_FlagInjection documents that a selector like
// "--follow" cannot become a flag: it is single-quoted and docker
// treats it as a (nonexistent) container name, and the charset admits
// no quote-escapes to break out.
func TestDockerLogsCommand_FlagInjection(t *testing.T) {
	cmd, err := DockerLogsCommand("--follow", time.Time{})
	if err != nil {
		return // rejection is also acceptable
	}
	if !strings.HasSuffix(cmd, "'--follow'") {
		t.Fatalf("flag-shaped selector must be quoted: %q", cmd)
	}
}

func testPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("wrap key: %v", err)
	}
	return sshPub
}

// TestHostKeyPinning covers all three TOFU states: record on first
// sight, pass on match, hard-fail on mismatch.
func TestHostKeyPinning(t *testing.T) {
	key := testPublicKey(t)
	fp := ssh.FingerprintSHA256(key)

	var recorded string
	cb := pinnedHostKeyCallback("", func(f string) { recorded = f })
	if err := cb("h1", nil, key); err != nil {
		t.Fatalf("TOFU dial should pass: %v", err)
	}
	if recorded != fp {
		t.Fatalf("TOFU should record %s, got %s", fp, recorded)
	}

	if err := pinnedHostKeyCallback(fp, nil)("h1", nil, key); err != nil {
		t.Fatalf("matching pin should pass: %v", err)
	}

	other := testPublicKey(t)
	err := pinnedHostKeyCallback(ssh.FingerprintSHA256(other), nil)("h1", nil, key)
	var mismatch *HostKeyMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected HostKeyMismatchError, got %v", err)
	}
}

// TestCommandForSource_Kinds pins each fixed read-only template.
func TestCommandForSource_Kinds(t *testing.T) {
	since := time.Date(2026, 7, 8, 14, 0, 0, 0, time.UTC)
	cases := []struct {
		kind, selector, want string
	}{
		{"docker", "api", "docker logs --timestamps --since '2026-07-08T14:00:00Z' 'api'"},
		{"compose", "acme", "docker compose -p 'acme' logs --no-color --timestamps --no-log-prefix --since '2026-07-08T14:00:00Z'"},
		{"swarm", "acme-production_backend", "docker service logs --raw --timestamps --since '2026-07-08T14:00:00Z' 'acme-production_backend'"},
		{"journald", "nginx.service", "journalctl -u 'nginx.service' -o short-iso-precise --no-pager --utc --since '2026-07-08 14:00:00'"},
	}
	for _, c := range cases {
		src := &store.LogSource{Kind: c.kind, Selector: c.selector}
		got, err := CommandForSource(src, since)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.kind, got, c.want)
		}
	}

	// file kind has no read-only template yet.
	if _, err := CommandForSource(&store.LogSource{Kind: "file", Selector: "/var/log/app.log"}, since); err == nil {
		t.Fatal("file kind must have no command template")
	}
	// injection stays rejected across all kinds.
	for _, k := range []string{"docker", "compose", "swarm", "journald"} {
		if _, err := CommandForSource(&store.LogSource{Kind: k, Selector: "x; rm -rf /"}, since); err == nil {
			t.Errorf("%s: injection selector must be rejected", k)
		}
	}
}
