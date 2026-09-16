package desk

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Process struct {
	CPUSeconds *float64 `json:"cpu_seconds"`
	PID        int      `json:"pid"`
	SessionID  string   `json:"session_id"`
	Name       string   `json:"name"`
	RSS        int64    `json:"rss_bytes"`
	CPUTicks   uint64   `json:"cpu_ticks"`
	OpenFiles  []string `json:"open_files"`
	OpenCount  int      `json:"open_count"`
	sid        int
}

var tickOnce sync.Once
var ticksPerSecond float64

func clockTickRate() float64 {
	tickOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		b, err := exec.CommandContext(ctx, "getconf", "CLK_TCK").Output()
		if err == nil {
			value, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
			if err == nil && value > 0 {
				ticksPerSecond = value
			}
		}
	})
	return ticksPerSecond
}

type Resources struct {
	Scratch      []Scratch     `json:"scratch"`
	GitActivity  []GitActivity `json:"git_activity"`
	At           time.Time     `json:"at"`
	Coverage     string        `json:"coverage"`
	Processes    []Process     `json:"processes"`
	ChangedFiles []string      `json:"changed_files"`
	GitStatus    string        `json:"git_status"`
	Diff         string        `json:"diff"`
	Worktrees    string        `json:"worktrees"`
	DiskBytes    int64         `json:"disk_bytes"`
	DiskFiles    int           `json:"disk_files"`
	DiskPartial  bool          `json:"disk_partial"`
}

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
func killSession(pid int) {
	// A shell's foreground jobs may have their own process group but share its
	// session. This is best-effort local cleanup, not cgroup confinement.
	for _, p := range processSnapshot() {
		if p.sid == pid {
			_ = syscall.Kill(p.PID, syscall.SIGKILL)
		}
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
}
func (m *Manager) Resources(ctx context.Context, project string, details bool) (Resources, error) {
	p, err := m.Project(project)
	if err != nil {
		return Resources{}, err
	}
	r := Resources{At: time.Now().UTC(), Coverage: "Sampled local processes; file authorship unknown. Shared commands outside managed jobs are not intercepted.", Processes: []Process{}, ChangedFiles: []string{}}
	sessions := m.Sessions(project)
	owners := map[int]string{}
	for _, s := range sessions {
		if s.PID > 0 {
			owners[s.PID] = s.ID
		}
	}
	m.mu.Lock()
	for id, j := range m.jobs {
		if j.project == project && j.pid > 0 {
			owners[j.pid] = id
		}
	}
	m.mu.Unlock()
	for _, proc := range processSnapshot() {
		owner, ok := owners[proc.sid]
		if !ok {
			continue
		}
		proc.SessionID = owner
		if rate := clockTickRate(); rate > 0 {
			seconds := float64(proc.CPUTicks) / rate
			proc.CPUSeconds = &seconds
		}
		fds, _ := os.ReadDir(fmt.Sprintf("/proc/%d/fd", proc.PID))
		proc.OpenCount = len(fds)
		for _, fd := range fds[:min(len(fds), 32)] {
			path, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", proc.PID, fd.Name()))
			if err == nil && strings.HasPrefix(path, p.Directory+"/") {
				proc.OpenFiles = append(proc.OpenFiles, path)
			}
		}
		r.Processes = append(r.Processes, proc)
		if len(r.Processes) >= 128 {
			break
		}
	}
	if !details {
		return r, nil
	}
	r.Scratch, r.GitActivity = m.artifactViews(project)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	r.GitStatus = gitView(ctx, p.Directory, "status", "--short", "--untracked-files=normal")
	r.Diff = gitView(ctx, p.Directory, "diff", "--no-ext-diff", "--no-textconv", "HEAD", "--")
	r.Worktrees = gitView(ctx, p.Directory, "worktree", "list", "--porcelain")
	deadline := time.Now().Add(time.Second)
	_ = filepath.WalkDir(p.Directory, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			r.DiskPartial = true
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) || r.DiskFiles >= 20000 {
			r.DiskPartial = true
			return fs.SkipAll
		}
		if d.IsDir() {
			if path == m.dir {
				return fs.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			r.DiskPartial = true
			return nil
		}
		r.DiskFiles++
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			r.DiskBytes += st.Blocks * 512
		} else {
			r.DiskBytes += info.Size()
		}
		return nil
	})
	return r, nil
}

type boundedBuffer struct{ data []byte }

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(b.data) < 128<<10 {
		b.data = append(b.data, p[:min(len(p), (128<<10)-len(b.data))]...)
	}
	return n, nil
}
func gitView(ctx context.Context, dir string, args ...string) string {
	cmd := exec.CommandContext(ctx, "git", append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_PAGER=cat"}
	var out boundedBuffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	_ = cmd.Run()
	return string(out.data)
}
