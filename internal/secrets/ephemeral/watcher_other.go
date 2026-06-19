//go:build !darwin && !linux

package ephemeral

// On platforms where we don't ship a kqueue/inotify implementation, fall
// back to the polling watcher. Files are still hard-deleted via the
// background sweeper at expiry.
func newWatcher() watcher { return newPollingWatcher() }
