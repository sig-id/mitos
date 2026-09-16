package desk

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

var ErrNotFound = errors.New("not found")
var ErrConflict = errors.New("operation conflicts with current state")

type Manager struct {
	lock      *os.File
	mu        sync.Mutex
	dir, root string
	state     state
	live      map[string]*terminal
	jobs      map[string]*runningJob
	closed    bool
	writeErr  error
	wg        sync.WaitGroup
}

func Open(dir, root string) (*Manager, error) {
	root, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("interactive workspace is already open")
	}
	opened := false
	defer func() {
		if !opened {
			_ = lock.Close()
		}
	}()
	m := &Manager{dir: dir, root: root, state: state{Version: 1}, live: map[string]*terminal{}, jobs: map[string]*runningJob{}}
	m.lock = lock
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err == nil {
		if err = json.Unmarshal(b, &m.state); err != nil {
			return nil, err
		}
		if m.state.Version != 1 {
			return nil, errors.New("unsupported workspace state version")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	// Never replay commands or signal a stale PID after a server restart.
	for i := range m.state.Sessions {
		s := &m.state.Sessions[i]
		if s.Status == "running" || s.Status == "starting" || s.Status == "stopping" {
			s.Status, s.PID, s.Detail = "interrupted", 0, "Server restarted. Inspect any surviving processes before starting a replacement."
		}
	}
	for i := range m.state.Jobs {
		j := &m.state.Jobs[i]
		if j.Status == "running" || j.Status == "queued" {
			j.Status, j.Detail = "interrupted", "Server restarted; the project build hold remains until you confirm the previous process stopped."
		}
	}
	if err = m.saveLocked(); err != nil {
		return nil, err
	}
	opened = true
	return m, nil
}

// Called under mu; an uncertain write poisons mutations until reopen.
func (m *Manager) saveLocked() error {
	if m.writeErr != nil {
		return m.writeErr
	}
	b, err := json.MarshalIndent(m.state, "", "  ")
	if err == nil {
		var f *os.File
		f, err = os.OpenFile(filepath.Join(m.dir, "state.next"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if err == nil {
			_, err = f.Write(b)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
		}
	}
	if err == nil {
		err = os.Rename(filepath.Join(m.dir, "state.next"), filepath.Join(m.dir, "state.json"))
	}
	if err == nil {
		var f *os.File
		f, err = os.Open(m.dir)
		if err == nil {
			err = f.Sync()
			_ = f.Close()
		}
	}
	m.writeErr = err
	return err
}
func (m *Manager) writable() error {
	if m.closed {
		return errors.New("workspace is shutting down")
	}
	return m.writeErr
}
func id(prefix string) string { return prefix + "_" + strings.ToLower(rand.Text()) }
func (m *Manager) projectLocked(id string) (Project, error) {
	for _, p := range m.state.Projects {
		if p.ID == id {
			return p, nil
		}
	}
	return Project{}, ErrNotFound
}
func (m *Manager) sessionLocked(id string) (*Session, error) {
	for i := range m.state.Sessions {
		if m.state.Sessions[i].ID == id {
			return &m.state.Sessions[i], nil
		}
	}
	return nil, ErrNotFound
}
func (m *Manager) Projects() []Project {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Project{}, m.state.Projects...)
}
func (m *Manager) Project(id string) (Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.projectLocked(id)
}
func (m *Manager) CreateProject(name, directory string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return Project{}, errors.New("project name must be 1–100 characters")
	}
	if !filepath.IsAbs(directory) {
		directory = filepath.Join(m.root, directory)
	}
	directory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return Project{}, err
	}
	rel, err := filepath.Rel(m.root, directory)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return Project{}, errors.New("project must be under the workspace root")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return Project{}, errors.New("choose an existing directory")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err = m.writable(); err != nil {
		return Project{}, err
	}
	for _, p := range m.state.Projects {
		if p.Directory == directory {
			return p, nil
		}
	}
	if len(m.state.Projects) >= 128 {
		return Project{}, errors.New("project limit reached")
	}
	p := Project{ID: id("project"), Name: name, Directory: directory, CreatedAt: time.Now().UTC()}
	m.state.Projects = append(m.state.Projects, p)
	return p, m.saveLocked()
}
func (m *Manager) Sessions(project string) []Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := []Session{}
	for _, s := range m.state.Sessions {
		if project == "" || s.ProjectID == project {
			result = append(result, s)
		}
	}
	return result
}
func (m *Manager) Session(id string) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, err := m.sessionLocked(id)
	if err != nil {
		return Session{}, err
	}
	return *s, nil
}
func (m *Manager) outputPath(id string) string { return filepath.Join(m.dir, id+".output") }
func (m *Manager) ReadOutput(id string, offset int64) (Output, error) {
	m.mu.Lock()
	status := ""
	if s, err := m.sessionLocked(id); err == nil {
		status = s.Status
	}
	for _, j := range m.state.Jobs {
		if j.ID == id {
			status = j.Status
		}
	}
	m.mu.Unlock()
	if status == "" {
		return Output{}, ErrNotFound
	}
	if offset < 0 {
		return Output{}, errors.New("negative output offset")
	}
	f, err := os.Open(m.outputPath(id))
	if errors.Is(err, os.ErrNotExist) {
		return Output{Next: 0, Status: status}, nil
	}
	if err != nil {
		return Output{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return Output{}, err
	}
	if offset > info.Size() {
		return Output{}, errors.New("output offset beyond recorded stream")
	}
	b := make([]byte, 64<<10)
	n, err := f.ReadAt(b, offset)
	if err != nil && err != io.EOF {
		return Output{}, err
	}
	return Output{Data: b[:n], Next: offset + int64(n), Status: status}, nil
}
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, t := range m.live {
		t.stop()
	}
	for _, j := range m.jobs {
		j.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
	_ = m.lock.Close()
}
func validText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && len(s) <= max && !strings.ContainsRune(s, 0)
}
func (m *Manager) authorLocked(project, author string) error {
	if author == "operator" {
		return nil
	}
	s, err := m.sessionLocked(author)
	if err != nil || s.ProjectID != project {
		return fmt.Errorf("author is not a session in this project")
	}
	return nil
}
