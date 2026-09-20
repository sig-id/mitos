package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Identify the process by its kernel-held event-log lock, which remains valid
// across binary rebuilds and cannot leave a stale PID file after a crash.
func signalStoreServer(file *os.File) error {
	info, err := file.Stat()
	if err != nil {
		return err
	}
	stat := info.Sys().(*syscall.Stat_t)
	dev := uint64(stat.Dev)
	major := (dev >> 8 & 0xfff) | (dev >> 32 & 0xfffff000)
	minor := (dev & 0xff) | (dev >> 12 & 0xffffff00)
	identity := fmt.Sprintf("%02x:%02x:%d", major, minor, stat.Ino)
	locks, err := os.ReadFile("/proc/locks")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(locks), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 8 || fields[1] != "FLOCK" || fields[3] != "WRITE" || fields[5] != identity {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err != nil || pid <= 0 || pid == os.Getpid() {
			break
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			return err
		}
		defer process.Release()
		cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
		if err != nil {
			return err
		}
		args := strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00")
		isServe := len(args) > 1 && args[1] == "serve"
		isDefault := len(args) == 1 && filepath.Base(args[0]) == "mitos"
		if !isServe && !isDefault {
			break
		}
		return process.Signal(syscall.SIGTERM)
	}
	return fmt.Errorf("could not identify the mitos process holding %s", file.Name())
}
