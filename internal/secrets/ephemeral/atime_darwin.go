//go:build darwin || freebsd || netbsd

package ephemeral

import (
	"syscall"
	"time"
)

func atimeFromStat(st *syscall.Stat_t) time.Time {
	return time.Unix(st.Atimespec.Sec, st.Atimespec.Nsec)
}
