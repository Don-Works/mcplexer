package config

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/don-works/mcplexer/internal/store"
)

// DefaultBrwctlPath is the brwctl binary used by LoadBrwRoster when the
// caller passes an empty path. Resolved on $PATH.
const DefaultBrwctlPath = "brwctl"

// ReconcileBrwProfiles is the apply-mode entry point used by the gateway's
// in-process auto-discovery. It is a thin wrapper around SyncBrwProfiles
// that forces opts.DryRun=false (a reconcile always writes) and returns the
// resulting plan so the caller can drive the make-it-live side effects
// (route invalidation + downstream instance reload) from the cmd layer.
//
// The make-it-live side effects are deliberately kept OUT of internal/config
// so this package never imports internal/downstream or internal/routing; the
// dependency direction stays one-way (cmd -> internal). Callers read the
// returned SyncPlan to learn which servers changed and reload only those.
func ReconcileBrwProfiles(ctx context.Context, svc *Service, st store.Store, roster []BrwDaemon, opts SyncOptions) (SyncPlan, error) {
	opts.DryRun = false
	return SyncBrwProfiles(ctx, svc, st, roster, opts)
}

// LoadBrwRoster execs `<brwctlPath> daemons` and parses the JSON array into
// the BrwDaemon roster. An empty brwctlPath falls back to DefaultBrwctlPath.
// The command's own discovery (which daemons are live) is authoritative; this
// helper only shells out and parses.
func LoadBrwRoster(ctx context.Context, brwctlPath string) ([]BrwDaemon, error) {
	bin := strings.TrimSpace(brwctlPath)
	if bin == "" {
		bin = DefaultBrwctlPath
	}
	cmd := exec.CommandContext(ctx, bin, "daemons")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("exec %q daemons: %w: %s", bin, err, msg)
		}
		return nil, fmt.Errorf("exec %q daemons: %w", bin, err)
	}
	return ParseBrwDaemons(stdout.Bytes())
}

// ParseBrwDaemons unmarshals a `brwctl daemons` JSON array. Whitespace is
// trimmed first so a trailing newline from the CLI doesn't trip the decoder.
// A null / empty document yields a nil slice with no error (an empty roster
// is a valid state — every daemon was shut down).
func ParseBrwDaemons(raw []byte) ([]BrwDaemon, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, nil
	}
	var daemons []BrwDaemon
	if err := json.Unmarshal(trimmed, &daemons); err != nil {
		return nil, fmt.Errorf("parse brwctl daemons JSON: %w", err)
	}
	return daemons, nil
}
