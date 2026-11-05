//go:build freebsd

package daemon

import (
	"strconv"

	"golang.org/x/sys/unix"
)

func bootTime() string {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return ""
	}
	return strconv.FormatInt(tv.Sec, 10)
}
