package desk

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"mitos/internal/agentcli"

	"github.com/creack/pty"
)

const maxOutput = 16 << 20

type terminal struct {
	file    *os.File
	pid     int
	inputMu sync.Mutex
	seen    map[string]bool
	keys    []string
}

func (t *terminal) stop() { killSession(t.pid); _ = t.file.Close() }

func (m *Manager) StartAgent(project string, request agentcli.Request, serverURL string) (Session, error) {
	launch, err := m.Agents.Prepare(request)
	if err != nil {
		return Session{}, err
	}
	role := request.Role
	if role == "" {
		role = "agent"
		if launch.Kind == "shell" {
			role = "shell"
		}
	}
	name := promptTitle(request.Prompt)
	pending := name == ""
	if pending {
		name = launch.AgentName + " · " + strings.TrimPrefix(id("new"), "new_")[:6]
	}
	return m.start(project, name, role, launch.AgentName, serverURL, launch, pending, request.Assignment)
}

func promptTitle(prompt string) string {
	name := []rune(strings.Join(strings.Fields(prompt), " "))
	truncated := len(name) > 79 || len(string(name)) > 96
	for len(name) > 79 || len(string(name)) > 96 {
		name = name[:len(name)-1]
	}
	if truncated {
		return string(name) + "…"
	}
	return string(name)
}

func (m *Manager) start(project, name, role, command, serverURL string, launch agentcli.Launch, pending bool, assignment *agentcli.Assignment) (Session, error) {
	if !validText(name, 100) || len(command) > 4096 || strings.ContainsRune(command, 0) {
		return Session{}, errors.New("provide a session name and a command of at most 4096 bytes")
	}
	if role != "agent" && role != "coordinator" && role != "shell" {
		return Session{}, errors.New("unknown session role")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return Session{}, err
	}
	p, err := m.projectLocked(project)
	if err != nil {
		return Session{}, err
	}
	if assignment != nil {
		copy := *assignment
		assignment = &copy
		for _, value := range []string{assignment.GoalID, assignment.TaskID, assignment.RequestedBy, assignment.HandoffTopicID} {
			if len(value) > 128 || strings.ContainsAny(value, "\x00\r\n") {
				return Session{}, errors.New("invalid assignment reference")
			}
		}
		if assignment.RequestedBy != "" && assignment.RequestedBy != "operator" {
			parent, err := m.sessionLocked(assignment.RequestedBy)
			if err != nil || parent.ProjectID != project {
				return Session{}, errors.New("requesting session must belong to this project")
			}
		}
		if assignment.HandoffTopicID != "" {
			found := false
			for _, topic := range m.state.Topics {
				if topic.ID == assignment.HandoffTopicID && topic.ProjectID == project {
					found = true
				}
			}
			if !found {
				return Session{}, errors.New("handoff topic must belong to this project")
			}
		}
	}
	if len(m.live) >= 16 || len(m.state.Sessions) >= 1024 {
		return Session{}, errors.New("session limit reached")
	}
	if role == "coordinator" {
		for _, s := range m.state.Sessions {
			if s.ProjectID == project && s.Role == role && (s.Status == "running" || s.Status == "starting") {
				return Session{}, errors.New("this project already has an active coordinator")
			}
		}
	}
	for _, previous := range m.state.Sessions {
		if previous.ProjectID == project && previous.Status == "interrupted" {
			return Session{}, errors.New("inspect and settle interrupted sessions before starting another session in this shared directory")
		}
	}
	s := Session{ID: id("session"), ProjectID: project, AgentID: launch.AgentID, ProfileID: launch.ProfileID, NamePending: pending, Name: strings.TrimSpace(name), Role: role, Command: command, Status: "starting", Rows: 28, Cols: 100, CreatedAt: time.Now().UTC()}
	s.Assignment = assignment
	m.state.Sessions = append(m.state.Sessions, s)
	if err = m.saveLocked(); err != nil {
		return Session{}, err
	}
	cmd := exec.Command(launch.Executable, launch.Args...)
	cmd.Dir = p.Directory
	scratch := filepath.Join(m.dir, s.ID+".tmp")
	if err = os.MkdirAll(scratch, 0700); err != nil {
		return m.startFailedLocked(s.ID, err)
	}
	storedSession, _ := m.sessionLocked(s.ID)
	storedSession.Scratch = scratch
	// These are trusted operator terminals: use the operator's normal CLI config.
	// Unlike batch providers, this path makes no confinement or oracle claims.
	cmd.Env = append(launch.Env, "TERM=xterm-256color", "COLORTERM=truecolor", "TMPDIR="+scratch, "GIT_TRACE2_EVENT="+filepath.Join(scratch, "git.jsonl"), "GIT_TRACE2_EVENT_BRIEF=1", "MITOS_PROJECT_ID="+project, "MITOS_SESSION_ID="+s.ID, "MITOS_URL="+serverURL)
	if assignment != nil {
		cmd.Env = append(cmd.Env, "MITOS_GOAL_ID="+assignment.GoalID, "MITOS_TASK_ID="+assignment.TaskID, "MITOS_REQUESTED_BY="+assignment.RequestedBy, "MITOS_HANDOFF_TOPIC_ID="+assignment.HandoffTopicID)
	}
	instructions := filepath.Join(m.dir, s.ID+".md")
	text := fmt.Sprintf("# Mitos project %s\n\nRole: %s. Shared directory: %s.\nUse mitos desk topics to discover relevant topics; read details with mitos desk topic ID.\nUse mitos desk post --topic ID --revision N --body TEXT, follow --topic ID, and jobs.\nUse mitos desk build -- ARGS for a coordinated Cargo build. Builds serialize; joining is explicit with mitos desk join JOB_ID. A joined result does not automatically validate your later edits.\nUse mitos desk resources to inspect the shared directory and current process activity.\nCoordinate shared-file and Git changes through topics. CLI commands outside managed jobs are cooperative and are not intercepted.\nThe coordinator maintains the plan, publishes topic summaries, and asks the operator for choices outside the brief. It has no additional runtime permissions.\n", p.Name, role, p.Directory)
	text += "\nFleet tools: mitos desk agents, inspect-agent --agent ID, sessions, start-session --agent ID --prompt TEXT, send-prompt --prompt TEXT SESSION_ID, and stop-session SESSION_ID. Use --goal ID --task ID --handoff-topic TOPIC_ID on start-session to retain assignment lineage. Native CLI sub-agents are separate from Mitos sessions.\nUse the topic board to report completion with evidence, blockers, or requests for more agents. Before handing off, write your own continuation prompt in a topic: objective, current state, evidence, remaining work, and relevant files. Pass that topic to the next session; a topic reference does not inject its content automatically.\nMitos currently supplies fleet tools, not an autonomous scheduler. Terminal prompts are input bytes and are not proof that an agent accepted a direction.\n"
	if err = os.WriteFile(instructions, []byte(text), 0600); err != nil {
		return m.startFailedLocked(s.ID, err)
	}
	binary, _ := os.Executable()
	cmd.Env = append(cmd.Env, "MITOS_COORDINATION="+instructions, "MITOS_BIN="+binary)
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
	f, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: s.Rows, Cols: s.Cols})
	if err != nil {
		return m.startFailedLocked(s.ID, err)
	}
	if err = syscall.SetNonblock(int(f.Fd()), true); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		_ = f.Close()
		_ = cmd.Wait()
		return m.startFailedLocked(s.ID, err)
	}
	t := &terminal{file: f, pid: cmd.Process.Pid, seen: map[string]bool{}}
	m.live[s.ID] = t
	stored, _ := m.sessionLocked(s.ID)
	stored.Status, stored.PID = "running", cmd.Process.Pid
	if err = m.saveLocked(); err != nil {
		t.stop()
		_ = cmd.Wait()
		delete(m.live, s.ID)
		return Session{}, err
	}
	s = *stored
	m.wg.Add(1)
	go m.waitTerminal(s.ID, cmd, t)
	return s, nil
}
func (m *Manager) startFailedLocked(id string, cause error) (Session, error) {
	s, _ := m.sessionLocked(id)
	s.Status, s.Detail = "failed", cause.Error()
	now := time.Now().UTC()
	s.EndedAt = &now
	if err := m.saveLocked(); err != nil {
		return Session{}, err
	}
	return Session{}, cause
}
func (m *Manager) waitTerminal(id string, cmd *exec.Cmd, t *terminal) {
	defer m.wg.Done()
	readDone := make(chan error, 1)
	go func() {
		f, err := os.OpenFile(m.outputPath(id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			t.stop()
			readDone <- err
			return
		}
		n, copyErr := io.Copy(outputWriter{f, m.notifyOutput}, io.LimitReader(t.file, maxOutput+1))
		if err = f.Sync(); copyErr == nil {
			copyErr = err
		}
		_ = f.Close()
		if n > maxOutput {
			copyErr = errors.New("terminal output reached the 16 MiB retention limit")
			t.stop()
		}
		if copyErr != nil && !errors.Is(copyErr, syscall.EIO) && !errors.Is(copyErr, os.ErrClosed) {
			t.stop()
		}
		readDone <- copyErr
	}()
	err := cmd.Wait()
	killSession(t.pid)
	var readErr error
	select {
	case readErr = <-readDone:
	case <-time.After(300 * time.Millisecond):
		_ = t.file.Close()
		readErr = <-readDone
	}
	_ = t.file.Close()
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.live, id)
	s, _ := m.sessionLocked(id)
	if s == nil {
		return
	}
	now := time.Now().UTC()
	s.EndedAt = &now
	code := cmd.ProcessState.ExitCode()
	s.ExitCode = &code
	s.PID = 0
	if s.Status == "stopping" || m.closed {
		s.Status = "stopped"
	} else if err != nil {
		s.Status = "failed"
		s.Detail = err.Error()
	} else {
		s.Status = "exited"
	}
	if readErr != nil && !errors.Is(readErr, syscall.EIO) && !errors.Is(readErr, os.ErrClosed) {
		s.Detail = readErr.Error()
	}
	_ = m.saveLocked()
}
func (m *Manager) Input(id, key, data string) error {
	if key == "" || len(key) > 100 || len(data) > 8192 {
		return errors.New("input requires a request key and at most 8192 bytes")
	}
	m.mu.Lock()
	t := m.live[id]
	err := m.writable()
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if t == nil {
		return ErrConflict
	}
	t.inputMu.Lock()
	defer t.inputMu.Unlock()
	if t.seen[key] {
		return nil
	}
	// Mark before write: an uncertain partial write must never be replayed.
	t.seen[key] = true
	t.keys = append(t.keys, key)
	if len(t.keys) > 512 {
		delete(t.seen, t.keys[0])
		t.keys = t.keys[1:]
	}
	_ = t.file.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_, err = t.file.Write([]byte(data))
	return err
}

// Prompt is the structured operator/orchestrator input boundary. Terminal control
// sequences, menu choices, and replay responses are never mistaken for titles.
func (m *Manager) Prompt(id, key, text string, bracketed bool) error {
	if key == "" || len(key) > 100 || strings.ContainsRune(text, 0) {
		return errors.New("invalid prompt request")
	}
	data := strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\n", "\r")
	if bracketed && text != "" {
		data = "\x1b[200~" + data + "\x1b[201~"
	}
	data += "\r"
	if len(data) > 8192 {
		return errors.New("send at most 8192 bytes at a time")
	}
	m.mu.Lock()
	s, err := m.sessionLocked(id)
	if err == nil {
		err = m.writable()
	}
	if err == nil && m.live[id] == nil {
		err = ErrConflict
	}
	if err == nil && s.NamePending {
		if name := promptTitle(text); name != "" {
			s.Name = name
			s.NamePending = false
			err = m.saveLocked()
		}
	}
	m.mu.Unlock()
	if err != nil {
		return err
	}
	return m.Input(id, key, data)
}
func (m *Manager) Resize(id string, rows, cols uint16) error {
	if rows < 2 || cols < 10 || rows > 300 || cols > 500 {
		return errors.New("invalid terminal size")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	t := m.live[id]
	if t == nil {
		return ErrConflict
	}
	if err := pty.Setsize(t.file, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		return err
	}
	s, _ := m.sessionLocked(id)
	s.Rows, s.Cols = rows, cols
	return nil
}

func (m *Manager) Settle(id string, confirmed bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	s, err := m.sessionLocked(id)
	if err != nil {
		return err
	}
	if !confirmed || s.Status != "interrupted" {
		return ErrConflict
	}
	s.Status = "stopped"
	s.Detail = "Operator confirmed that the interrupted session's previous processes stopped."
	now := time.Now().UTC()
	s.EndedAt = &now
	return m.saveLocked()
}
func (m *Manager) Stop(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.writable(); err != nil {
		return err
	}
	s, err := m.sessionLocked(id)
	if err != nil {
		return err
	}
	t := m.live[id]
	if t == nil {
		switch s.Status {
		case "stopped":
			return nil
		case "failed", "exited":
			s.Status = "stopped"
			return m.saveLocked()
		default:
			return fmt.Errorf("%w: inspect the interrupted session and confirm its previous processes stopped", ErrConflict)
		}
	}
	s.Status = "stopping"
	if err = m.saveLocked(); err != nil {
		return err
	}
	t.stop()
	return nil
}
