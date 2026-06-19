package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/don-works/mcplexer/internal/store"
)

// ExitCode is the conventional process exit value returned by RunJob.
type ExitCode int

const (
	// ExitOK signals the job ran to completion successfully (or was
	// handed off to the daemon).
	ExitOK ExitCode = 0
	// ExitNotFound is the exit code when the job id doesn't exist in
	// the store.
	ExitNotFound ExitCode = 64
	// ExitFailure is the exit code when the job ran but the command
	// returned non-zero.
	ExitFailure ExitCode = 1
)

// DaemonRunner is the narrow contract scheduler needs to ask a running
// daemon to fire a job on its behalf. RunNow returns nil when the
// daemon accepted the job (the daemon's own scheduler will execute it
// + audit it); any error means "daemon unreachable, fall through to
// direct exec".
type DaemonRunner interface {
	RunNow(ctx context.Context, jobID string) error
}

// daemonProbeTimeout caps how long RunJob waits to hear back from the
// daemon before falling through to a direct exec. 50ms is plenty on a
// local UDS; long enough to absorb a busy loop, short enough that
// users don't notice the gap when the daemon is genuinely down.
const daemonProbeTimeout = 50 * time.Millisecond

// RunJob is the entry-point for `mcplexer run-job <id>`. It loads the
// job, asks the running daemon to handle it (when daemonClient !=
// nil), and falls through to a direct exec when the daemon is
// unreachable. The direct-exec branch deliberately skips approval —
// this is the no-daemon escape hatch.
func RunJob(
	ctx context.Context,
	jobID string,
	s store.ScheduledJobStore,
	daemonClient DaemonRunner,
) (ExitCode, error) {
	j, err := s.GetScheduledJob(ctx, jobID)
	if err != nil {
		return ExitNotFound, fmt.Errorf("load job %q: %w", jobID, err)
	}
	if daemonClient != nil {
		probeCtx, cancel := context.WithTimeout(ctx, daemonProbeTimeout)
		defer cancel()
		if err := daemonClient.RunNow(probeCtx, jobID); err == nil {
			return ExitOK, nil
		}
	}
	return runDirectly(ctx, *j)
}

// runDirectly executes the job without going through the daemon's
// approval flow. Used only when the daemon is unreachable; reflects
// the survive-daemon-down contract: better to fire-and-log than to
// silently skip the cron tick.
func runDirectly(ctx context.Context, j store.ScheduledJob) (ExitCode, error) {
	args, err := decodeArgs(j.ArgsJSON)
	if err != nil {
		return ExitFailure, fmt.Errorf("decode args: %w", err)
	}
	env, err := decodeEnv(j.EnvJSON)
	if err != nil {
		return ExitFailure, fmt.Errorf("decode env: %w", err)
	}
	_, _, runErr := osCommandExecutor{}.Run(ctx, j.Command, args, env, j.CWD)
	if runErr != nil {
		return ExitFailure, runErr
	}
	return ExitOK, nil
}

// parseDurationFallback accepts a time.Duration string. Centralised so
// platform-specific driver files share one parser without re-importing
// time inside their template strings.
func parseDurationFallback(spec string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(spec))
	if err != nil {
		return 0, fmt.Errorf("duration %q: %w", spec, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("duration %q: must be > 0", spec)
	}
	return d, nil
}
