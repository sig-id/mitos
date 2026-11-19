//go:build !linux

package observation

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// rescanInterval throttles workspace rescans. Boundary waits poll far more
// often than a scan is meaningful, so repeated samples inside one interval
// reuse the previous counts.
const rescanInterval = 200 * time.Millisecond

// LocalActivity samples real workspace mutations by rescanning the tree.
//
// This is the portable fallback for platforms without inotify. Workspace
// writes are counted by comparing each regular file's size and modification
// time between scans, so real activity is still observed. The process tree
// comes from the platform's processTree implementation; without one, TreeSize
// stays zero and command polling never judges quiescence from an unseen
// process tree, waiting for process exit, cancellation, or timeouts instead.
// Quiet detection resolves at scan granularity.
type LocalActivity struct {
	root   string
	pid    int
	writes uint64
	last   time.Time
	stamps map[string]fileStamp
}

type fileStamp struct {
	size    int64
	modNano int64
}

func NewLocalActivity(workspace string) (*LocalActivity, error) {
	stamps, err := scanTree(workspace)
	if err != nil {
		return nil, err
	}
	return &LocalActivity{root: workspace, stamps: stamps, last: time.Now()}, nil
}

// SetPID points the process tree reader at the observed worker.
func (a *LocalActivity) SetPID(pid int) { a.pid = pid }

func (a *LocalActivity) Close() error { return nil }

func (a *LocalActivity) Sample(outputOffset uint64) (Sample, error) {
	now := time.Now()
	if now.Sub(a.last) < rescanInterval {
		execs, size, err := processTree(a.pid)
		if err != nil {
			return Sample{}, err
		}
		return Sample{OutputOffset: outputOffset, WorkspaceWrites: a.writes, Execs: execs, TreeSize: size}, nil
	}
	next, err := scanTree(a.root)
	if err != nil {
		return Sample{}, err
	}
	for path, stamp := range next {
		if prev, ok := a.stamps[path]; !ok || prev != stamp {
			a.writes++
		}
	}
	for path := range a.stamps {
		if _, ok := next[path]; !ok {
			a.writes++
		}
	}
	a.stamps = next
	a.last = now
	execs, size, err := processTree(a.pid)
	if err != nil {
		return Sample{}, err
	}
	return Sample{OutputOffset: outputOffset, WorkspaceWrites: a.writes, Execs: execs, TreeSize: size}, nil
}

// scanTree stamps every regular file under root. Entries that vanish or
// become unreadable mid-scan are skipped, mirroring the inotify reader's
// tolerance for a concurrently mutating workspace.
func scanTree(root string) (map[string]fileStamp, error) {
	stamps := map[string]fileStamp{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
			return nil
		}
		if err != nil {
			return err
		}
		stamps[path] = fileStamp{size: info.Size(), modNano: info.ModTime().UnixNano()}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stamps, nil
}
