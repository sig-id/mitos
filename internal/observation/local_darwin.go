//go:build darwin

package observation

import "golang.org/x/sys/unix"

// processTree reports the member count of the process tree rooted at root
// through sysctl. Membership mirrors the procfs reader: the root itself, the
// root's process group, and descendants by parent chain. Full argv is not
// exposed by kern.proc, so no exec digests are produced on this platform.
func processTree(root int) ([]string, int, error) {
	if root <= 0 {
		return nil, 0, nil
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, 0, err
	}
	type process struct{ parent, group int }
	processes := map[int]process{}
	for _, p := range procs {
		processes[int(p.Proc.P_pid)] = process{parent: int(p.Eproc.Ppid), group: int(p.Eproc.Pgid)}
	}
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
		if member {
			size++
		}
	}
	return nil, size, nil
}
