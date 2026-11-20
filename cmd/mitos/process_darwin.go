//go:build darwin

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"mitos/internal/procsys"

	"golang.org/x/sys/unix"
)

// macOS has no /proc/locks, so identify the server by intersecting the
// processes holding the lock file open (lsof) with mitos serve command lines
// read through kern.procargs2. The lock is already known to be held when this
// runs; the argv check keeps the signal aimed at a mitos server even if an
// unrelated process has the file open.
func signalStoreServer(file *os.File) error {
	// BSD lsof has no end-of-options marker; an absolute path never starts
	// with a dash, so it cannot be mistaken for a flag.
	lockPath, err := filepath.Abs(file.Name())
	if err != nil {
		return err
	}
	out, lsofErr := exec.Command("/usr/sbin/lsof", "-F", "p", lockPath).Output()
	if lsofErr != nil {
		return fmt.Errorf("mitos stop on macOS needs /usr/sbin/lsof to identify the lock holder: %w", lsofErr)
	}
	holders := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		if pid, err := strconv.Atoi(strings.TrimPrefix(line, "p")); err == nil && pid > 0 {
			holders[pid] = true
		}
	}
	delete(holders, os.Getpid())
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return err
	}
	var matched []int
	for _, p := range procs {
		pid := int(p.Proc.P_pid)
		if pid <= 0 || pid == os.Getpid() || !holders[pid] {
			continue
		}
		argv, err := procsys.Argv(pid)
		if err != nil || len(argv) < 1 {
			continue
		}
		isServe := len(argv) > 1 && argv[1] == "serve"
		isDefault := len(argv) == 1 && filepath.Base(argv[0]) == "mitos"
		if !isServe && !isDefault {
			continue
		}
		matched = append(matched, pid)
	}
	if len(matched) != 1 {
		return fmt.Errorf("could not identify a single mitos process holding %s: candidates %v", file.Name(), matched)
	}
	process, err := os.FindProcess(matched[0])
	if err != nil {
		return err
	}
	defer process.Release()
	if err := process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
