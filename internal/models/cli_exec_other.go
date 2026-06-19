//go:build !(darwin || linux || freebsd || openbsd || netbsd)

// cli_exec_other.go — non-Unix fallback for the CLI model adapters.
// Process-group kill relies on POSIX setpgid + kill(-pgid); platforms
// without it (notably Windows) get the stock CommandContext kill plus a
// bounded WaitDelay. The CLI adapters are gated behind env opt-ins and
// only ship on macOS/Linux in practice, so this path exists purely to
// keep the package building under `go build` for every GOOS.
package models

import (
	"context"
	"os/exec"
	"time"
)

const cliHardStopWaitDelay = 5 * time.Second

// newSandboxedCLICmd builds an *exec.Cmd with a bounded WaitDelay. No
// process-group isolation is available here, so a cancel only reaches
// the immediate child pid.
func newSandboxedCLICmd(ctx context.Context, program string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.WaitDelay = cliHardStopWaitDelay
	return cmd
}
