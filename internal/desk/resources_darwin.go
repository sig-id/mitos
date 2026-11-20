//go:build darwin

package desk

import (
	"bytes"

	"golang.org/x/sys/unix"
)

// processSnapshot samples the process table through sysctl. Session ids come
// from getsid(2) per process; entries of other users are skipped when the
// call is not permitted. CPU time, RSS, and open files are not exposed by
// kern.proc and stay zero on this platform.
func processSnapshot() []Process {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return []Process{}
	}
	out := make([]Process, 0, len(procs))
	for _, p := range procs {
		pid := int(p.Proc.P_pid)
		sid, err := unix.Getsid(pid)
		if err != nil {
			continue
		}
		out = append(out, Process{PID: pid, Name: string(bytes.TrimRight(p.Proc.P_comm[:], "\x00")), sid: sid})
	}
	return out
}
