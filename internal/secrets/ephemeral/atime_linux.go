//go:build linux || openbsd

package ephemeral

import (
	"syscall"
	"time"
)

func atimeFromStat(st *syscall.Stat_t) time.Time {
	return time.Unix(st.Atim.Sec, st.Atim.Nsec)
}
