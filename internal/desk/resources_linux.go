//go:build linux

package desk

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processSnapshot() []Process {
	entries, _ := os.ReadDir("/proc")
	out := []Process{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			continue
		}
		end := strings.LastIndexByte(string(b), ')')
		start := strings.IndexByte(string(b), '(')
		if end < 0 || start < 0 {
			continue
		}
		f := strings.Fields(string(b[end+1:]))
		if len(f) < 22 {
			continue
		}
		sid, _ := strconv.Atoi(f[3])
		u, _ := strconv.ParseUint(f[11], 10, 64)
		s, _ := strconv.ParseUint(f[12], 10, 64)
		rss, _ := strconv.ParseInt(f[21], 10, 64)
		out = append(out, Process{PID: pid, Name: string(b[start+1 : end]), sid: sid, CPUTicks: u + s, RSS: rss * int64(os.Getpagesize())})
	}
	return out
}
