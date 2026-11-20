//go:build linux

package procsys

import "syscall"

// Child returns the SysProcAttr for launched worker processes. On Linux a
// child is always killed when its parent dies; setsid additionally detaches
// it from the operator's terminal session.
func Child(setsid bool) *syscall.SysProcAttr {
	if setsid {
		return &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	}
	return &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
