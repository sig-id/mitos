//go:build !linux

package procsys

import "syscall"

// SetParentDeathSignal has no portable equivalent on this platform; cleanup
// relies on the explicit cancel and process-group kill paths.
func SetParentDeathSignal(*syscall.SysProcAttr) {}
