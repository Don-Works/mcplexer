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

// cursorTokenRe is the charset for a journald opaque cursor token, whose
// wire form is "s=<hex>;i=<hex>;b=<hex>;m=<hex>;t=<hex>;x=<hex>". It must
// admit ';' and '=' — both are inert inside the single quotes this package
// always applies — while still excluding quotes, whitespace, backslash and
// every other shell metacharacter, so the ADR's two-layer no-injection
// guarantee is unchanged.
//
// This token is the only command input DERIVED FROM REMOTE OUTPUT rather
// than from local config, so it is validated on the way back in and capped:
// a compromised or malfunctioning host must not be able to widen the next
// command line it is handed.
var cursorTokenRe = regexp.MustCompile(`^[A-Za-z0-9;=]+$`)

// maxCursorLen bounds the remote-derived cursor. Real journald cursors are
// ~160 chars; this leaves headroom without admitting an unbounded token.
const maxCursorLen = 512

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

func quoteCursor(tok string) (string, error) {
	if len(tok) > maxCursorLen {
		return "", fmt.Errorf("sshx: journal cursor token is %d bytes, over the %d cap", len(tok), maxCursorLen)
	}
	if !cursorTokenRe.MatchString(tok) {
		return "", fmt.Errorf("sshx: journal cursor token %q fails charset validation", tok)
	}
	return "'" + tok + "'", nil
}

// CommandForSource builds the fixed read-only pull command for a
// source's kind. This is the ONLY place a remote command string is
// constructed; every kind has a literal template with quoted variable
// tokens, so no source config can inject a mutating command.
//
// cursor is the opaque journald cursor from the previous pull and is used
// only by the journald kind; the docker/compose/swarm CLIs expose no
// equivalent and stay on their --since windows.
func CommandForSource(src *store.LogSource, since time.Time, cursor string) (string, error) {
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
		return JournaldCommand(src.Selector, since, cursor)
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
// --raw is required, but NOT for byte-0 reasons: verified on a live swarm,
// `docker service logs --timestamps` keeps the RFC3339 stamp at byte zero
// even without --raw (the "<service>.<slot>.<id>@<node> | " decoration lands
// AFTER the timestamp — unlike compose, whose prefix lands before it). --raw
// is needed because it strips the volatile per-task id out of the message
// BODY: without it the rotating task id pollutes template mining and
// destabilises the cursor line-hash on every task replacement. Output with
// --raw --timestamps is a clean "<timestamp> <message>".
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
//	journalctl -u '<unit>' -q -o short-iso-precise --no-pager --utc \
//	  --show-cursor [--after-cursor '<cursor>' | --since '<ts>']
//
// short-iso-precise gives a leading RFC3339-ish timestamp the collector
// parses.
//
// -q (--quiet) is required: without it journalctl prints "-- Journal begins
// at … --" and "-- Reboot --" banner lines that carry no leading timestamp,
// so the collector would parse them as zero-timestamp lines and fire a false
// "source discontinuity" on every pull.
//
// --after-cursor is the steady-state window and is EXCLUSIVE: journald never
// re-returns the cursor entry. --since cannot be, and that is why it is only
// the first-pull bootstrap. --since takes whole seconds while the journal
// (and our cursor) carries microseconds, so a --since window truncated to
// its second re-returns every earlier line sharing that second. Any unit
// logging more than once per second — which includes ssh.service, because
// each monitoring pull's own login writes ~4 lines into one second — would
// then hand the collector a first line that is not the stored tail and fire
// a false discontinuity on EVERY pull, forever. The opaque cursor removes
// the timestamp-precision coupling entirely rather than narrowing it.
//
// --show-cursor appends a trailing "-- cursor: <c>" line, emitted even for a
// window with no entries; collect.splitJournalCursor strips it before the
// lines are parsed.
func JournaldCommand(unit string, since time.Time, cursor string) (string, error) {
	u, err := quoteSelector(unit)
	if err != nil {
		return "", err
	}
	cmd := "journalctl -u " + u + " -q -o short-iso-precise --no-pager --utc --show-cursor"
	// An exact cursor always wins over a lossy timestamp window.
	if cursor != "" {
		c, err := quoteCursor(cursor)
		if err != nil {
			return "", err
		}
		return cmd + " --after-cursor " + c, nil
	}
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
