//go:build linux

package sandbox

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
)

// unshareDriver is the bwrap-less Linux fallback. It uses util-linux's
// `unshare(1)` to create new user / mount / pid / net namespaces and
// then execs the caller's program. There is no bind-mount layer here:
// the agent sees the host filesystem with the namespace's UID mapping,
// which is strictly weaker isolation than bwrap.
//
// Used only when bwrap is not installed. The Pi-appliance image always
// ships bwrap, so in production this driver should never be picked.
//
// NOTE: UNVERIFIED FROM macOS DEVELOPMENT MACHINE.
type unshareDriver struct{}

func (d *unshareDriver) Name() string { return "unshare" }

func (d *unshareDriver) Available() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	_, err := exec.LookPath("unshare")
	return err == nil
}

// Run runs `unshare --user --mount --pid --net --fork <program> <args>`.
// --fork is required so the new PID namespace gets a real init (PID 1);
// without it the kernel rejects exec inside a new PID ns.
func (d *unshareDriver) Run(
	ctx context.Context, cfg Config, program string, args []string,
) (ExitCode, error) {
	if program == "" {
		return -1, errors.New("sandbox: program is required")
	}

	unshareArgs := []string{"--user", "--mount", "--pid", "--fork"}
	if cfg.Network != NetworkHost {
		unshareArgs = append(unshareArgs, "--net")
	}
	unshareArgs = append(unshareArgs, program)
	unshareArgs = append(unshareArgs, args...)

	cmd := exec.CommandContext(ctx, "unshare", unshareArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if cfg.WorkingDir != "" {
		cmd.Dir = cfg.WorkingDir
	}

	err := cmd.Run()
	if cmd.ProcessState != nil {
		return ExitCode(cmd.ProcessState.ExitCode()), filterRunErr(err)
	}
	return -1, err
}
