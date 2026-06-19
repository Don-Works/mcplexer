//go:build darwin || linux || freebsd || openbsd || netbsd

package ephemeral

import (
	"os"
	"syscall"
	"time"
)

// atimeOf extracts the access time from a unix stat_t. Used only by the
// polling fallback (kqueue/inotify build tags do not invoke it).
func atimeOf(info os.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return atimeFromStat(st)
	}
	return time.Time{}
}
