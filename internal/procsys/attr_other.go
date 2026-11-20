//go:build !linux

package procsys

import "syscall"

// Child returns the SysProcAttr for launched worker processes. Platforms
// without PDEATHSIG cannot tie children to the parent's lifetime, so cleanup
// depends on the callers' cancellation, Cancel, and WaitDelay paths.
func Child(setsid bool) *syscall.SysProcAttr {
	if setsid {
		return &syscall.SysProcAttr{Setsid: true}
	}
	return &syscall.SysProcAttr{}
}
