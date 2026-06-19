package sandbox

// SelectDriver returns the most appropriate driver for the current host.
// Order of preference: bubblewrap (Linux) → sandbox-exec (macOS) →
// unshare (Linux fallback) → nil if nothing usable.
//
// The selection is host-static (no flag plumbing): if you want to force
// a specific driver in tests, instantiate the driver directly. We
// resist exposing a "force driver X" knob in the public API because it
// becomes an attack surface — production callers should always get the
// strongest available option.
func SelectDriver() Driver {
	for _, d := range candidates() {
		if d != nil && d.Available() {
			return d
		}
	}
	return nil
}

// candidates is platform-specialized: the darwin file returns just the
// macOS driver, the linux file returns bwrap then unshare. Stub-OS
// builds (windows, other) return an empty slice so SelectDriver yields
// nil and callers surface ErrUnsupportedOS.
//
// Keeping the platform branch inside a thin helper (rather than calling
// runtime.GOOS in SelectDriver) means each driver file owns its own
// availability check and the imports stay clean per build tag.
//
// candidates is defined in the per-GOOS files:
//   selector_darwin.go, selector_linux.go, selector_other.go
