// Package sshx is the Monitoring feature's SSH executor: bounded,
// host-key-pinned, READ-ONLY by construction. There is no generic exec
// API — callers pick a fixed command builder and every token is
// validated against a strict charset before it is quoted onto the
// wire. Adding a mutating command shape requires a new ADR (0007 §1).
package sshx

import (
	"fmt"
	"regexp"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// The SSH exec channel carries a single command string that the remote
// sshd hands to the login shell — a true argv exec does not exist in
// the protocol. The ADR's no-injection guarantee therefore rests on
// two layers: every variable token is (1) validated against a strict
// charset and (2) single-quoted. Constant flags are literals.

// tokenRe is the charset every selector token must match BEFORE
// quoting. Deliberately excludes quotes, whitespace, and every shell
// metacharacter.
var tokenRe = regexp.MustCompile(`^[A-Za-z0-9._:+/-]+$`)

// tsTokenRe is the charset for timestamp tokens. Wider than tokenRe (it
// admits a single space for the journald "2006-01-02 15:04:05" layout)
// but still excludes every shell metacharacter and quote.
var tsTokenRe = regexp.MustCompile(`^[A-Za-z0-9:.+ -]+$`)

func quoteToken(kind, tok string) (string, error) {
	if !tokenRe.MatchString(tok) {
		return "", fmt.Errorf("sshx: %s token %q fails charset validation", kind, tok)
	}
	return "'" + tok + "'", nil
}

func quoteTimestamp(tok string) (string, error) {
	if !tsTokenRe.MatchString(tok) {
		return "", fmt.Errorf("sshx: timestamp token %q fails charset validation", tok)
	}
	return "'" + tok + "'", nil
}

// CommandForSource builds the fixed read-only pull command for a
// source's kind. This is the ONLY place a remote command string is
// constructed; every kind has a literal template with quoted variable
// tokens, so no source config can inject a mutating command.
func CommandForSource(src *store.LogSource, since time.Time) (string, error) {
	if err := store.ValidateSelector(src.Selector); err != nil {
		return "", err
	}
	switch src.Kind {
	case store.LogSourceKindDocker:
		return DockerLogsCommand(src.Selector, since)
	case store.LogSourceKindCompose:
		return ComposeLogsCommand(src.Selector, since)
	case store.LogSourceKindSwarm:
		return SwarmLogsCommand(src.Selector, since)
	case store.LogSourceKindJournald:
		return JournaldCommand(src.Selector, since)
	default:
		return "", fmt.Errorf("sshx: source kind %q has no read-only command template", src.Kind)
	}
}

// SwarmLogsCommand pulls one Docker Swarm service's aggregated logs:
//
//	docker service logs --raw --timestamps --since '<cursor>' '<service>'
//
// The service name is stable across task/container replacement, unlike
// the generated task container name. This keeps monitoring attached
// across normal stack deploys without introducing a generic remote exec.
//
// --raw is required for the same reason compose needs --no-log-prefix:
// without it `docker service logs` decorates every line with a
// "<service>.<slot>.<id>@<node>    | " prefix, so the RFC3339 timestamp is
// no longer at byte zero, the collector's leading-timestamp parse fails
// for every line, and the cursor never advances (unbounded re-pull).
// --raw drops the decoration and emits "<timestamp> <message>".
func SwarmLogsCommand(service string, since time.Time) (string, error) {
	svc, err := quoteSelector(service)
	if err != nil {
		return "", err
	}
	cmd := "docker service logs --raw --timestamps"
	if !since.IsZero() {
		ts, err := quoteToken("since", since.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return "", err
		}
		cmd += " --since " + ts
	}
	return cmd + " " + svc, nil
}

// DockerLogsCommand builds the fixed read-only pull command:
//
//	docker logs --timestamps --since '<cursor>' '<selector>'
//
// since is optional (zero time = from the beginning). The selector is
// re-validated here even though the store validated it at CRUD time —
// dial-time defence in depth per ADR 0007.
func DockerLogsCommand(selector string, since time.Time) (string, error) {
	sel, err := quoteSelector(selector)
	if err != nil {
		return "", err
	}
	cmd := "docker logs --timestamps"
	if !since.IsZero() {
		ts, err := quoteToken("since", since.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return "", err
		}
		cmd += " --since " + ts
	}
	return cmd + " " + sel, nil
}

// ComposeLogsCommand pulls one compose project's aggregated logs:
//
//	docker compose -p '<project>' logs --no-color --timestamps --no-log-prefix --since '<cursor>'
//
// The selector is the compose project name. Requires the compose v2
// plugin on the remote host. --no-log-prefix is required because the
// collector expects the RFC3339 timestamp at byte zero of each line;
// Compose otherwise prepends "container | " and every line is skipped.
func ComposeLogsCommand(project string, since time.Time) (string, error) {
	proj, err := quoteSelector(project)
	if err != nil {
		return "", err
	}
	cmd := "docker compose -p " + proj + " logs --no-color --timestamps --no-log-prefix"
	if !since.IsZero() {
		ts, err := quoteToken("since", since.UTC().Format(time.RFC3339Nano))
		if err != nil {
			return "", err
		}
		cmd += " --since " + ts
	}
	return cmd, nil
}

// JournaldCommand pulls one systemd unit's journal:
//
//	journalctl -u '<unit>' -o short-iso-precise --no-pager --since '<cursor>'
//
// short-iso-precise gives a leading RFC3339-ish timestamp the collector
// parses. since uses journald's "2006-01-02 15:04:05" local-ish layout,
// which journalctl parses as UTC when TZ is unset on the pull.
func JournaldCommand(unit string, since time.Time) (string, error) {
	u, err := quoteSelector(unit)
	if err != nil {
		return "", err
	}
	cmd := "journalctl -u " + u + " -o short-iso-precise --no-pager --utc"
	if !since.IsZero() {
		ts, err := quoteTimestamp(since.UTC().Format("2006-01-02 15:04:05"))
		if err != nil {
			return "", err
		}
		cmd += " --since " + ts
	}
	return cmd, nil
}

// quoteSelector validates + quotes a selector token.
func quoteSelector(selector string) (string, error) {
	if err := store.ValidateSelector(selector); err != nil {
		return "", err
	}
	return quoteToken("selector", selector)
}
