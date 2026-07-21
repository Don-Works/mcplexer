//go:build darwin || linux || freebsd || openbsd || netbsd

// cli_exec_unix.go — hard-stop process control for the CLI-backed model
// adapters (claude_cli, opencode_cli, grok_cli, mimo_cli). Every one of those
// adapters wraps the real model binary in a sandbox-exec / bwrap
// wrapper, so the immediate child pid exec.CommandContext knows about is
// the WRAPPER, not the model. The stock CommandContext cancel path sends
// SIGKILL only to that wrapper pid; the model process it launched can
// survive as an orphan and keep burning tokens — exactly what operator
// hard-stop must prevent.
//
// newSandboxedCLICmd closes that gap on Unix by:
//
//   - Setpgid: the child starts a fresh process group (pgid == child
//     pid). The whole CLI process tree (wrapper + model + any
//     grandchildren the model spawns) inherits that group.
//   - cmd.Cancel: on ctx cancellation we signal the ENTIRE group
//     (kill(-pgid, SIGKILL)) instead of just the wrapper pid, so the
//     model process actually dies.
//   - WaitDelay: bounds how long Wait blocks after Cancel before force-
//     closing the stdio pipes, so a wedged grandchild that inherits a
//     pipe can't pin the runner goroutine open forever.
package models

import (
	"context"
	"os/exec"
	"syscall"
	"time"
)

// cliHardStopWaitDelay bounds the post-cancel wait. Generous enough that
// a cleanly-exiting model flushes its final JSON, tight enough that a
// hung grandchild can't hold the runner hostage.
const cliHardStopWaitDelay = 5 * time.Second

// newSandboxedCLICmd builds an *exec.Cmd for a sandboxed CLI model
// subprocess with hard-stop semantics (process-group isolation + group
// kill on cancel + a bounded WaitDelay). See the file header for why a
// plain exec.CommandContext is insufficient for the wrapped CLIs.
func newSandboxedCLICmd(ctx context.Context, program string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Negative pid → deliver SIGKILL to the whole process group led
		// by the child (pgid == child pid because of Setpgid). Falls
		// back to a single-pid kill if the group send fails (e.g. ESRCH
		// when the group already exited between cancel and signal).
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	cmd.WaitDelay = cliHardStopWaitDelay
	return cmd
}
