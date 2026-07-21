//go:build !darwin && !linux

package sandbox

// candidates on unsupported GOOS (windows, freebsd, ...) returns nil so
// SelectDriver yields nil and callers can surface ErrUnsupportedOS.
func candidates() []Driver { return nil }
