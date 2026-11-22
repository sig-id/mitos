//go:build linux

package procsys

import "syscall"

// SetParentDeathSignal marks the child to be killed when its parent dies.
func SetParentDeathSignal(attr *syscall.SysProcAttr) { attr.Pdeathsig = syscall.SIGKILL }
