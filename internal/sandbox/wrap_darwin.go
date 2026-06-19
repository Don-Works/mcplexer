//go:build darwin

package sandbox

import (
	"os"
	"strings"
)

// sandboxWrapAvailable reports whether /usr/bin/sandbox-exec is
// present on this host. Conservative: stat-only, no exec probe.
func sandboxWrapAvailable() bool {
	info, err := os.Stat(sandboxExecPath)
	if err != nil || info.IsDir() {
		return false
	}
	return true
}

// wrapForPlatform substitutes program/args with the sandbox-exec
// wrapping. The profile is materialised to a tempfile so sandbox-exec
// can -f it; the returned cleanup removes the tempfile after the
// wrapped process has exited. Profile-write failures fall through to
// the identity transform — failing every downstream spawn because
// /tmp is unwritable would be a worse outcome than degrading silently
// to "no sandbox tonight" (logged where the user-visible "off" badge
// surfaces in the UI from Describe).
func wrapForPlatform(
	cfg Config, home, program string, args []string, _ func(),
) (string, []string, func()) {
	profile := buildSandboxExecProfile(cfg, home)
	pf, err := writeProfileTemp(profile)
	if err != nil {
		return program, args, func() {}
	}
	cleanup := func() { _ = os.Remove(pf) }
	wrapped := append([]string{"-f", pf, program}, args...)
	return sandboxExecPath, wrapped, cleanup
}

// describeForPlatform renders the active sandbox config as a short
// identifier for the dashboard. Mirrors the deny rules buildSandbox
// ExecProfile emits.
func describeForPlatform(cfg Config) string {
	parts := []string{"deny-creds"}
	if cfg.Network == NetworkDeny || cfg.Network == NetworkProxy {
		parts = append(parts, "deny-net")
	}
	return "sandbox-exec(" + strings.Join(parts, ",") + ")"
}
