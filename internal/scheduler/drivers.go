package scheduler

import (
	"context"

	"github.com/don-works/mcplexer/internal/store"
)

// Driver is the SurviveDaemonDown promotion target — when a job has
// survive_daemon_down=1, the scheduler installs a host-native timer
// that fires the job even when mcplexer is down. The timer's payload
// is `mcplexer run-job <job-id>` which contacts the daemon via UDS
// (or falls through to direct exec when the daemon is unreachable).
type Driver interface {
	Name() string    // "systemd_timer" | "launchd_label"
	Available() bool // host supports this driver
	Install(ctx context.Context, job store.ScheduledJob) (nativeID string, err error)
	Uninstall(ctx context.Context, nativeID string) error
}

// noopDriver is returned by SelectDriver on unsupported platforms (e.g.
// Windows). Available() returns false so admin code can detect the lack
// of native-survive support without nil-checking.
type noopDriver struct{}

// Name reports a synthetic name so callers logging the driver get a
// useful value even when survive-daemon-down isn't supported.
func (noopDriver) Name() string    { return "unsupported" }
func (noopDriver) Available() bool { return false }
func (noopDriver) Install(context.Context, store.ScheduledJob) (string, error) {
	return "", errDriverUnavailable
}
func (noopDriver) Uninstall(context.Context, string) error { return nil }

// errDriverUnavailable is returned when Install is called on a host that
// has no native scheduler driver.
var errDriverUnavailable = driverErr("scheduler: native driver unavailable on this host")

type driverErr string

func (d driverErr) Error() string { return string(d) }

// sanitizeID normalises a job ID for use as a native-scheduler unit name.
// launchd labels and systemd unit names share a permissive character set
// (alphanumeric + hyphen + underscore + dot); anything outside that gets
// replaced with '-'. Empty input → "job".
func sanitizeID(id string) string {
	out := make([]byte, 0, len(id))
	for i := 0; i < len(id); i++ {
		c := id[i]
		ok := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.'
		if ok {
			out = append(out, c)
		} else {
			out = append(out, '-')
		}
	}
	if len(out) == 0 {
		return "job"
	}
	return string(out)
}
