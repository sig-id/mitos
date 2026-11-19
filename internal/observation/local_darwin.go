//go:build darwin

package observation

import (
	"strings"

	"mitos/internal/procsys"

	"golang.org/x/sys/unix"
)

type darwinProcess struct{ pid, ppid, pgid int }

func sysctlProcesses() ([]darwinProcess, error) {
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]darwinProcess, 0, len(procs))
	for _, p := range procs {
		out = append(out, darwinProcess{pid: int(p.Proc.P_pid), ppid: int(p.Eproc.Ppid), pgid: int(p.Eproc.Pgid)})
	}
	return out, nil
}

// processTree reports the member count and argv digests of the process tree
// rooted at root through sysctl. Membership mirrors the procfs reader: the
// root itself, the root's process group, and descendants by parent chain.
// Argv comes from kern.procargs2, and each digest hashes the same
// NUL-separated argv bytes the procfs reader hashes.
func processTree(root int) ([]string, int, error) {
	if root <= 0 {
		return nil, 0, nil
	}
	procs, err := sysctlProcesses()
	if err != nil {
		return nil, 0, err
	}
	type process struct{ parent, group int }
	processes := map[int]process{}
	for _, p := range procs {
		processes[p.pid] = process{parent: p.ppid, group: p.pgid}
	}
	var execs []string
	size := 0
	for pid, p := range processes {
		member := pid == root || p.group == root
		visited := map[int]bool{}
		for parent := p.parent; !member && parent > 1 && !visited[parent]; {
			visited[parent] = true
			if parent == root {
				member = true
				break
			}
			ancestor, ok := processes[parent]
			if !ok {
				break
			}
			parent = ancestor.parent
		}
		if !member {
			continue
		}
		size++
		if argv, err := procsys.Argv(pid); err == nil && len(argv) > 0 {
			execs = append(execs, ArgvDigest([]byte(strings.Join(argv, "\x00")+"\x00")))
		}
	}
	return execs, size, nil
}
