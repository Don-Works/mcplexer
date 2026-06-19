// Package sandbox: CommandWrapper is the cross-OS surface for
// transparently routing a process spawn through the host's sandbox
// driver. The darwin build adds the sandbox-exec wrap; non-darwin
// builds use the identity transform so callers don't need build tags.

package sandbox

import "os"

// CommandWrapper transforms a program+args into a wrapped invocation
// that runs under a sandbox driver. Callers (downstream.Manager) hand
// it (cmdPath, args) and substitute the returned (program, args) into
// the exec.Cmd they were about to build. The returned cleanup func is
// safe to call after the wrapped process has exited; it removes any
// transient on-disk artefacts (sandbox profile tempfile).
//
// A nil wrapper means "do not sandbox" — callers MUST treat a nil
// receiver as the identity transform so the same code path works
// when sandbox is disabled.
type CommandWrapper struct {
	cfg     Config
	enabled bool
	home    string
}

// NewCommandWrapper returns a CommandWrapper backed by the OS-native
// sandbox driver. When the host has no usable driver (Linux without
// bwrap/unshare, Windows, sandbox-exec missing on a damaged macOS
// install) it returns a wrapper whose Wrap is a no-op so callers can
// pass it through unconditionally — no nil-checks in the hot path.
func NewCommandWrapper(cfg Config) *CommandWrapper {
	home, _ := os.UserHomeDir()
	return &CommandWrapper{
		cfg:     cfg,
		enabled: sandboxWrapAvailable(),
		home:    home,
	}
}

// Enabled reports whether Wrap will actually substitute a sandbox
// driver. Useful so the dashboard can show "sandbox: active" vs
// "sandbox: unavailable on this OS".
func (w *CommandWrapper) Enabled() bool {
	if w == nil {
		return false
	}
	return w.enabled
}

// Wrap transforms (program, args) into the sandboxed equivalent.
// Returns (program, args, cleanup): cleanup MUST be called once the
// caller is done with the resulting exec.Cmd (after Wait()). The
// identity transform is used when the wrapper isn't enabled — caller
// always invokes cleanup so it's safe to defer unconditionally.
func (w *CommandWrapper) Wrap(program string, args []string) (string, []string, func()) {
	noop := func() {}
	if w == nil || !w.enabled {
		return program, args, noop
	}
	return wrapForPlatform(w.cfg, w.home, program, args, noop)
}

// Describe returns a short identifier suitable for audit / dashboard
// display. Format: "sandbox-exec(deny-creds[,deny-net])" or "off".
func (w *CommandWrapper) Describe() string {
	if !w.Enabled() {
		return "off"
	}
	return describeForPlatform(w.cfg)
}
