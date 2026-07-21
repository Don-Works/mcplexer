//go:build darwin

package sandbox

import (
	"os"
	"strings"
)

// sandboxWrapFailureProgram is executed when the sandbox profile cannot be
// materialized. Running a guaranteed non-success command preserves the
// existing wrapper API while failing the requested spawn closed.
const sandboxWrapFailureProgram = "/usr/bin/false"

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
// a non-success command. A sandbox setup failure must never silently execute
// the requested model process without isolation.
func wrapForPlatform(
	cfg Config, home, program string, args []string, _ func(),
) (string, []string, func()) {
	return wrapForPlatformWithWriter(cfg, home, program, args, writeProfileTemp)
}

func wrapForPlatformWithWriter(
	cfg Config,
	home, program string,
	args []string,
	writeProfile func(string) (string, error),
) (string, []string, func()) {
	profile := buildSandboxExecProfile(cfg, home)
	pf, err := writeProfile(profile)
	if err != nil {
		return sandboxWrapFailureProgram, nil, func() {}
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
	if cfg.Network != NetworkHost {
		parts = append(parts, "deny-net")
	}
	return "sandbox-exec(" + strings.Join(parts, ",") + ")"
}
