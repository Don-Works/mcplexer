//go:build linux || openbsd

package ephemeral

import (
	"syscall"
	"time"
)

//nolint:unused // used via atimeOf when polling/kqueue paths are selected
func atimeFromStat(st *syscall.Stat_t) time.Time {
	return time.Unix(st.Atim.Sec, st.Atim.Nsec)
}
