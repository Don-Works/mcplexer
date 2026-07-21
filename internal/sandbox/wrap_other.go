//go:build !darwin && !linux

package sandbox

// sandboxWrapAvailable is always false on platforms without a wired
// sandbox driver (Windows, BSDs). Rather than half-enable an untested
// driver and silently produce a partial sandbox, the wrapper stays
// disabled and the dashboard surfaces "unsupported on this OS" via
// Describe().
func sandboxWrapAvailable() bool { return false }

// wrapForPlatform is the identity transform where no driver is wired;
// callers invoke it through the disabled path and never reach this
// body today, but it's defined so the package builds everywhere
// without build-tag gymnastics in the wrapper struct.
func wrapForPlatform(
	_ Config, _, program string, args []string, noop func(),
) (string, []string, func()) {
	return program, args, noop
}

// describeForPlatform reports the platform gap. The dashboard already
// gates the "Enable" toggle behind Enabled(), so this string is only
// shown when somebody hits the API directly.
func describeForPlatform(_ Config) string {
	return "unsupported-os"
}
