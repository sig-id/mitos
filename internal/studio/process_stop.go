package studio

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// processGroupAlive reports whether a process group still exists.
func processGroupAlive(pgid int) bool {
	err := syscall.Kill(-pgid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// stopProcessGroup kills a process group identified as an attempt's own.
func stopProcessGroup(pgid int) {
	_ = syscall.Kill(-pgid, syscall.SIGKILL)
}

// processGroupMatchesWorkspace verifies the group leader's working directory
// is the attempt workspace, so a recycled process group id is never stopped.
// Verification needs /proc; without it the group is treated as unverified and
// left untouched.
func processGroupMatchesWorkspace(pgid int, workspace string) bool {
	cwd, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pgid))
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(workspace); err == nil {
		workspace = resolved
	}
	return cwd == workspace
}
