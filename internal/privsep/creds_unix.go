//go:build unix

package privsep

import "syscall"

// dropPrivileges makes the child run as uid/gid in its own process group.
func dropPrivileges(uid, gid int) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}, Setpgid: true}
}
