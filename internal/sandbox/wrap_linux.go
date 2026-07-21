//go:build linux

package sandbox

import (
	"os/exec"
	"strings"
)

// linuxWrapFailureProgram is executed when the bwrap binary vanishes
// between the availability probe and the wrap. Running a guaranteed
// non-success command fails the requested spawn closed rather than
// silently executing unsandboxed.
const linuxWrapFailureProgram = "/bin/false"

// sandboxWrapAvailable reports whether bubblewrap is installed. bwrap
// is the only driver wired through the wrapper path: unshare's weaker
// isolation is not an acceptable silent fallback for the
// credential-denying guarantees callers rely on.
func sandboxWrapAvailable() bool {
	_, err := exec.LookPath("bwrap")
	return err == nil
}

// wrapForPlatform substitutes program/args with a bwrap invocation
// built from cfg. No transient on-disk artefacts are created — bwrap
// takes its whole policy as argv — so the caller's noop cleanup is
// returned unchanged.
func wrapForPlatform(
	cfg Config, home, program string, args []string, noop func(),
) (string, []string, func()) {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return linuxWrapFailureProgram, nil, noop
	}
	return bwrapPath, bwrapArgv(cfg, home, program, args), noop
}

// describeForPlatform renders the active sandbox config as a short
// identifier for the dashboard. Mirrors the darwin driver's format.
func describeForPlatform(cfg Config) string {
	parts := []string{"deny-creds"}
	if cfg.Network != NetworkHost {
		parts = append(parts, "deny-net")
	}
	return "bwrap(" + strings.Join(parts, ",") + ")"
}
