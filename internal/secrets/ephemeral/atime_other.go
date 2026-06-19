//go:build !darwin && !linux && !freebsd && !openbsd && !netbsd

package ephemeral

import (
	"os"
	"time"
)

// atimeOf is a stub on platforms we don't actively support; the polling
// watcher will simply never delete on this branch (sweeper still cleans up).
func atimeOf(_ os.FileInfo) time.Time { return time.Time{} }
