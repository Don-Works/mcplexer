//go:build !darwin && !linux

package scheduler

// SelectDriver returns the noop driver on platforms with no
// native-survive support (Windows, *BSD).
func SelectDriver() Driver { return noopDriver{} }
