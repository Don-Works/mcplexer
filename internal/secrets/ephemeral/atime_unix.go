//go:build darwin || linux || freebsd || openbsd || netbsd

package ephemeral

import (
	"os"
	"syscall"
	"time"
)

// atimeOf extracts the access time from a unix stat_t. Used by the polling
// fallback and darwin kqueue path; linux inotify may not reference it on
// that GOOS, so unused still reports it under multi-platform packages.
//
//nolint:unused // referenced from pollForRead / kqueue paths under build tags
func atimeOf(info os.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return atimeFromStat(st)
	}
	return time.Time{}
}
