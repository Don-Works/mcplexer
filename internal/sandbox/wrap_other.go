//go:build !darwin

package sandbox

// sandboxWrapAvailable is always false on non-darwin until bwrap /
// unshare wiring lands. Today the Linux drivers exist in code but
// have not been live-tested through the downstream spawn path; rather
// than half-enable them and silently produce a partial sandbox, we
// keep the wrapper disabled. The dashboard surfaces this as
// "unsupported on this OS" via Describe().
func sandboxWrapAvailable() bool { return false }

// wrapForPlatform is the identity transform on non-darwin; callers
// invoke it through the disabled path and never reach this body
// today, but it's defined so the package builds on Linux/Windows
// without build-tag gymnastics in the wrapper struct.
func wrapForPlatform(
	_ Config, _, program string, args []string, noop func(),
) (string, []string, func()) {
	return program, args, noop
}

// describeForPlatform on non-darwin just reports the platform gap.
// The dashboard already gates the "Enable" toggle behind Enabled(),
// so this string is only shown when somebody hits the API directly.
func describeForPlatform(_ Config) string {
	return "unsupported-os"
}
