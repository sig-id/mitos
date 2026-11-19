//go:build darwin

package procsys

import (
	"bytes"
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

// Argv reads a process argv through kern.procargs2. The kernel requires the
// caller to own the process or hold appropriate privileges; other processes
// surface an error and callers skip them.
func Argv(pid int) ([]string, error) {
	// CTL_KERN=1, KERN_PROCARGS2=49.
	mib := [4]int32{1, 49, int32(pid)}
	buf := make([]byte, 1<<16)
	n := uintptr(len(buf))
	_, _, errno := syscall.Syscall6(syscall.SYS___SYSCTL,
		uintptr(unsafe.Pointer(&mib[0])), 3,
		uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)), 0, 0)
	if errno != 0 {
		return nil, errno
	}
	buf = buf[:n]
	if len(buf) < 4 {
		return nil, errors.New("short procargs")
	}
	// Recent macOS kernels omit the historical pointer arrays and lay out
	// [argc][executable path, NUL-padded to a pointer-aligned boundary]
	// [argv strings, packed NUL-terminated][environment strings]. The pad
	// between the executable path and argv can hold several NUL bytes
	// depending on the path length, so every run of NULs is skipped.
	argc := int(int32(buf[0]) | int32(buf[1])<<8 | int32(buf[2])<<16 | int32(buf[3])<<24)
	if argc <= 0 || argc > 4096 {
		return nil, fmt.Errorf("bad argc %d", argc)
	}
	pos := 4
	if end := bytes.IndexByte(buf[pos:], 0); end >= 0 {
		pos += end + 1
	}
	for pos < len(buf) && buf[pos] == 0 {
		pos++
	}
	argv := make([]string, 0, argc)
	for i := 0; i < argc && pos < len(buf); i++ {
		end := bytes.IndexByte(buf[pos:], 0)
		if end < 0 {
			end = len(buf) - pos
		}
		argv = append(argv, string(buf[pos:pos+end]))
		pos += end + 1
	}
	if len(argv) != argc {
		return nil, errors.New("incomplete argv")
	}
	return argv, nil
}
