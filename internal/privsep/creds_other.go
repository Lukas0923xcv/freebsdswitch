//go:build !unix

package privsep

import "syscall"

// dropPrivileges: privilege separation only exists on FreeBSD; on other
// systems (Windows, for the simulator) bsdswitch-privd is not used.
func dropPrivileges(uid, gid int) *syscall.SysProcAttr { return nil }
