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

// tokenRe is the charset every variable token must match BEFORE
// quoting. Deliberately excludes quotes, whitespace, and every shell
// metacharacter. Timestamps (RFC3339Nano) fit: letters, digits,
// ':', '.', '-', 'T', 'Z', '+'.
var tokenRe = regexp.MustCompile(`^[A-Za-z0-9._:+/-]+$`)

func quoteToken(kind, tok string) (string, error) {
	if !tokenRe.MatchString(tok) {
		return "", fmt.Errorf("sshx: %s token %q fails charset validation", kind, tok)
	}
	return "'" + tok + "'", nil
}

// DockerLogsCommand builds the fixed read-only pull command:
//
//	docker logs --timestamps --since '<cursor>' '<selector>'
//
// since is optional (zero time = from the beginning). The selector is
// re-validated here even though the store validated it at CRUD time —
// dial-time defence in depth per ADR 0007.
func DockerLogsCommand(selector string, since time.Time) (string, error) {
	if err := store.ValidateSelector(selector); err != nil {
		return "", err
	}
	sel, err := quoteToken("selector", selector)
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
