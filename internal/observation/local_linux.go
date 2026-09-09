package observation

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// LocalActivity watches real workspace mutations with inotify. Process argv is
// sampled from procfs: short-lived execs can be missed, so this reader must not
// be represented as the engine's complete mediated C1 exec surface.
type LocalActivity struct {
	fd      int
	root    string
	watches map[int]string
	writes  uint64
	pid     int
}

func NewLocalActivity(workspace string) (*LocalActivity, error) {
	fd, err := syscall.InotifyInit1(syscall.IN_NONBLOCK | syscall.IN_CLOEXEC)
	if err != nil {
		return nil, err
	}
	a := &LocalActivity{fd: fd, root: workspace, watches: map[int]string{}}
	if err = a.addTree(workspace); err != nil {
		a.Close()
		return nil, err
	}
	return a, nil
}
func (a *LocalActivity) SetPID(pid int) { a.pid = pid }
func (a *LocalActivity) Close() error   { return syscall.Close(a.fd) }
func (a *LocalActivity) addTree(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		wd, err := syscall.InotifyAddWatch(a.fd, path, syscall.IN_MODIFY|syscall.IN_ATTRIB|syscall.IN_CREATE|syscall.IN_DELETE|syscall.IN_MOVED_FROM|syscall.IN_MOVED_TO|syscall.IN_DELETE_SELF|syscall.IN_MOVE_SELF|syscall.IN_ONLYDIR|syscall.IN_DONT_FOLLOW)
		if errors.Is(err, syscall.ENOENT) {
			return nil
		}
		if err != nil {
			return err
		}
		a.watches[wd] = path
		return nil
	})
}

func (a *LocalActivity) Sample(outputOffset uint64) (Sample, error) {
	// Bound a drain under continuous writers; saturation is loss of observation,
	// never permission to conclude that the workspace is quiet.
	var buffer [64 * 1024]byte
	drained := false
	reconcile := false
	for range 128 {
		n, err := syscall.Read(a.fd, buffer[:])
		if errors.Is(err, syscall.EAGAIN) {
			drained = true
			break
		}
		if errors.Is(err, syscall.EINTR) {
			continue
		}
		if err != nil {
			return Sample{}, err
		}
		if n == 0 {
			return Sample{}, errors.New("workspace watcher closed")
		}
		for offset := 0; offset < n; {
			if n-offset < 16 {
				return Sample{}, errors.New("incomplete workspace event")
			}
			header := buffer[offset : offset+16]
			wd := int(int32(binary.NativeEndian.Uint32(header[0:4])))
			mask := binary.NativeEndian.Uint32(header[4:8])
			size := int(binary.NativeEndian.Uint32(header[12:16]))
			if size > n-offset-16 {
				return Sample{}, errors.New("invalid workspace event")
			}
			name := string(bytes.TrimRight(buffer[offset+16:offset+16+size], "\x00"))
			offset += 16 + size
			if mask&syscall.IN_Q_OVERFLOW != 0 {
				return Sample{}, errors.New("workspace observation overflow")
			}
			path, known := a.watches[wd]
			if mask&syscall.IN_IGNORED != 0 {
				delete(a.watches, wd)
				continue
			}
			if !known {
				return Sample{}, errors.New("unknown workspace watch")
			}
			a.writes++
			if mask&syscall.IN_ISDIR != 0 && mask&(syscall.IN_MOVED_FROM|syscall.IN_DELETE) != 0 {
				reconcile = true
			}
			if path == a.root && mask&(syscall.IN_DELETE_SELF|syscall.IN_MOVE_SELF) != 0 {
				return Sample{}, errors.New("workspace root moved or removed")
			}
			// Refresh watches after directory moves as wd identities survive renames.
			if mask&syscall.IN_ISDIR != 0 && mask&(syscall.IN_CREATE|syscall.IN_MOVED_TO) != 0 {
				if err := a.addTree(filepath.Join(path, name)); err != nil {
					return Sample{}, err
				}
			}
		}
	}
	if !drained {
		return Sample{}, errors.New("workspace observation saturated")
	}
	if reconcile {
		// inotify watches follow inodes across directory moves. Reconcile their
		// paths so later creates are attached correctly and moved-out trees stop
		// contributing workspace activity.
		current := map[string]bool{}
		err := filepath.WalkDir(a.root, func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if d.IsDir() {
				current[path] = true
			}
			return nil
		})
		if err != nil {
			return Sample{}, err
		}
		if err = a.addTree(a.root); err != nil {
			return Sample{}, err
		}
		for wd, path := range a.watches {
			if !current[path] {
				if _, err = syscall.InotifyRmWatch(a.fd, uint32(wd)); err != nil && !errors.Is(err, syscall.EINVAL) {
					return Sample{}, err
				}
				delete(a.watches, wd)
			}
		}
	}
	execs, size, err := processTree(a.pid)
	if err != nil {
		return Sample{}, err
	}
	return Sample{OutputOffset: outputOffset, WorkspaceWrites: a.writes, Execs: execs, TreeSize: size}, nil
}

func processTree(root int) ([]string, int, error) {
	if root <= 0 {
		return nil, 0, nil
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, 0, err
	}
	type process struct{ parent, group int }
	processes := map[int]process{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		// Other users' protected proc entries are not part of this local provider.
		if errors.Is(err, os.ErrPermission) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		end := bytes.LastIndexByte(b, ')')
		if end < 0 {
			return nil, 0, fmt.Errorf("invalid proc stat for %d", pid)
		}
		fields := strings.Fields(string(b[end+1:]))
		if len(fields) < 3 {
			return nil, 0, errors.New("incomplete proc stat")
		}
		parent, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, 0, err
		}
		group, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, 0, err
		}
		processes[pid] = process{parent, group}
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
		argv, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, 0, err
		}
		if len(argv) > 0 {
			execs = append(execs, ArgvDigest(argv))
		}
	}
	return execs, size, nil
}
