package desk

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"syscall"
	"time"
)

type runningJob struct {
	project string
	pid     int
	cancel  context.CancelFunc
}

func (m *Manager) Jobs(project string) []Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Job{}
	for _, j := range m.state.Jobs {
		if j.ProjectID == project {
			j.Consumers = append([]string{}, j.Consumers...)
			j.Command = append([]string{}, j.Command...)
			out = append(out, j)
		}
	}
	return out
}
func (m *Manager) jobLocked(id string) (*Job, error) {
	for i := range m.state.Jobs {
		if m.state.Jobs[i].ID == id {
			return &m.state.Jobs[i], nil
		}
	}
	return nil, ErrNotFound
}
func (m *Manager) Build(project, author string, args []string) (Job, error) {
	if len(args) > 32 {
		return Job{}, errors.New("too many build arguments")
	}
	// Only a managed cargo build; configuration and path overrides would obscure
	// the resource being coordinated. No shell interpolation.
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--release", "--workspace", "--all-targets", "--all-features", "--no-default-features", "--locked", "--offline", "--frozen":
		case "-p", "--package", "--features", "--target", "--profile":
			i++
			if i >= len(args) || !validText(args[i], 200) || strings.HasPrefix(args[i], "-") {
				return Job{}, errors.New("build flag requires a value")
			}
		default:
			return Job{}, errors.New("unsupported build argument: " + a)
		}
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		return Job{}, errors.New("cargo is not available on the server PATH")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return Job{}, err
	}
	if _, err := m.projectLocked(project); err != nil {
		return Job{}, err
	}
	if err := m.authorLocked(project, author); err != nil {
		return Job{}, err
	}
	if len(m.state.Jobs) >= 2048 {
		return Job{}, errors.New("job history limit reached")
	}
	for _, j := range m.state.Jobs {
		if j.ProjectID == project && j.Status == "interrupted" {
			return Job{}, errors.New("inspect and release the interrupted build hold before starting another build")
		}
	}
	j := Job{ID: id("job"), ProjectID: project, Initiator: author, Consumers: []string{author}, Command: append([]string{"cargo", "build"}, args...), Status: "queued", CreatedAt: time.Now().UTC(), Detail: "Managed builds serialize per project. Live-directory output does not establish which concurrent edits passed."}
	m.state.Jobs = append(m.state.Jobs, j)
	if err := m.saveLocked(); err != nil {
		return Job{}, err
	}
	m.dispatchJobLocked(project)
	stored, _ := m.jobLocked(j.ID)
	return *stored, nil
}
func (m *Manager) Join(job, author string) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return Job{}, err
	}
	j, err := m.jobLocked(job)
	if err != nil {
		return Job{}, err
	}
	if err = m.authorLocked(j.ProjectID, author); err != nil {
		return Job{}, err
	}
	if j.Status != "running" && j.Status != "queued" {
		return Job{}, ErrConflict
	}
	if !slices.Contains(j.Consumers, author) {
		j.Consumers = append(j.Consumers, author)
	}
	err = m.saveLocked()
	out := *j
	out.Consumers = append([]string{}, j.Consumers...)
	return out, err
}
func (m *Manager) Release(job, author string, confirmed bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	j, err := m.jobLocked(job)
	if err != nil {
		return err
	}
	if err = m.authorLocked(j.ProjectID, author); err != nil {
		return err
	}
	if j.Status == "interrupted" {
		if author != "operator" || !confirmed {
			return errors.New("operator confirmation that the previous process stopped is required")
		}
		j.Status = "stopped"
		j.Detail = "Operator released the interrupted build hold."
	} else {
		j.Consumers = slices.DeleteFunc(j.Consumers, func(s string) bool { return s == author })
		if len(j.Consumers) == 0 && j.Status == "queued" {
			j.Status = "stopped"
		}
	}
	if err = m.saveLocked(); err != nil {
		return err
	}
	if len(j.Consumers) == 0 {
		if live := m.jobs[job]; live != nil {
			live.cancel()
		}
	}
	m.dispatchJobLocked(j.ProjectID)
	return nil
}
func (m *Manager) dispatchJobLocked(project string) {
	if m.closed || m.writeErr != nil || m.projectHeld(project) {
		return
	}
	for _, live := range m.jobs {
		if live.project == project {
			return
		}
	}
	for _, j := range m.state.Jobs {
		if j.ProjectID == project && j.Status == "interrupted" {
			return
		}
	}
	for i := range m.state.Jobs {
		j := &m.state.Jobs[i]
		if j.ProjectID != project || j.Status != "queued" {
			continue
		}
		p, _ := m.projectLocked(project)
		j.Status = "running"
		if m.saveLocked() != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		live := &runningJob{project: project, cancel: cancel}
		m.jobs[j.ID] = live
		copy := *j
		m.wg.Add(1)
		go m.executeJob(ctx, cancel, copy, p, live)
		return
	}
}
func (m *Manager) executeJob(ctx context.Context, cancel context.CancelFunc, job Job, p Project, live *runningJob) {
	defer m.wg.Done()
	defer cancel()
	cmd := exec.CommandContext(ctx, job.Command[0], job.Command[1:]...)
	cmd.Dir = p.Directory
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Pdeathsig: syscall.SIGKILL}
	cmd.Cancel = func() error { killSession(cmd.Process.Pid); return nil }
	cmd.WaitDelay = 2 * time.Second
	f, err := os.OpenFile(m.outputPath(job.ID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	code := -1
	if err == nil {
		writer := &jobOutput{file: f, cancel: cancel}
		cmd.Stdout = writer
		cmd.Stderr = writer
		err = cmd.Start()
		if err == nil {
			m.mu.Lock()
			live.pid = cmd.Process.Pid
			m.mu.Unlock()
			err = cmd.Wait()
			killSession(cmd.Process.Pid)
			code = cmd.ProcessState.ExitCode()
		}
		if syncErr := f.Sync(); err == nil {
			err = syncErr
		}
		_ = f.Close()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.jobs, job.ID)
	j, _ := m.jobLocked(job.ID)
	now := time.Now().UTC()
	j.EndedAt = &now
	j.ExitCode = &code
	if ctx.Err() != nil {
		j.Status = "stopped"
	} else if err != nil {
		j.Status = "failed"
		j.Detail = err.Error()
	} else {
		j.Status = "exited"
	}
	if m.saveLocked() == nil {
		m.dispatchJobLocked(p.ID)
	}
}

type jobOutput struct {
	file   *os.File
	n      int64
	cancel context.CancelFunc
}

func (w *jobOutput) Write(p []byte) (int, error) {
	if w.n+int64(len(p)) > maxOutput {
		w.cancel()
		return 0, errors.New("job output limit reached")
	}
	n, err := w.file.Write(p)
	if err != nil {
		w.cancel()
	}
	w.n += int64(n)
	if err == nil && n != len(p) {
		err = io.ErrShortWrite
	}
	return n, err
}

// StopProjectJobs cancels owned build processes without claiming their exit.
func (m *Manager) StopProjectJobs(project string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := []string{}
	for _, j := range m.state.Jobs {
		if j.ProjectID == project && (j.Status == "running" || j.Status == "interrupted") {
			ids = append(ids, j.ID)
		}
	}
	if len(ids) == 0 {
		return ids, nil
	}
	if err := m.writable(); err != nil {
		return nil, err
	}
	for i := range m.state.Jobs {
		j := &m.state.Jobs[i]
		if !slices.Contains(ids, j.ID) {
			continue
		}
		if live := m.jobs[j.ID]; live != nil {
			live.cancel()
		}
		j.Detail = "Independent supervision requested termination."
	}
	return ids, m.saveLocked()
}
