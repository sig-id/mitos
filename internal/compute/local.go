package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"syscall"
	"time"

	workspacefs "mitos/internal/workspace"
)

type Local struct {
	mu      sync.Mutex
	handles map[string]*localHandle
}
type localHandle struct {
	prepared Workspace
	owner    *Local
	spec     LaunchSpec
	started  bool
	mu       sync.Mutex
}

func (h *localHandle) ID() string        { return h.spec.Base }
func (h *localHandle) Workspace() string { return h.spec.Workspace.Directory }

type localSession struct {
	handle *localHandle
	cmd    *exec.Cmd
	done   chan struct{}
	output limitedBuffer
	err    error
	events []Event
	mu     sync.Mutex
}

func (s *localSession) ID() string              { return s.handle.ID() }
func (s *localSession) PID() int                { return s.cmd.Process.Pid }
func (s *localSession) Done() <-chan struct{}   { return s.done }
func (s *localSession) Result() ([]byte, error) { <-s.done; return s.output.Bytes(), s.err }
func (s *localSession) OutputOffset() uint64    { return s.output.Total() }
func (l *Local) Capabilities() Capabilities {
	return Capabilities{Isolation: "process", Egress: "unrestricted", DataMount: "unsupported", NetModes: []string{"unrestricted"}}
}
func (l *Local) Prepare(ctx context.Context, spec LaunchSpec) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	admitted, err := Compile(Request{TrustTier: spec.TrustTier, Provider: "local", NetMode: spec.Posture.NetMode, WorkspaceMode: spec.Workspace.Mode, BrokerEndpoint: spec.Posture.BrokerEndpoint}, map[string]Capabilities{"local": l.Capabilities()}, spec.Development)
	if err != nil || spec.Provider != "local" || admitted.TrustTier != spec.TrustTier || !reflect.DeepEqual(admitted.Capabilities, spec.Capabilities) || admitted.Posture.NetMode != spec.Posture.NetMode || admitted.Posture.DataMount != spec.Posture.DataMount {
		return nil, errors.New("local provider cannot enforce the requested launch posture")
	}
	if spec.Workspace.Mode != "copy" {
		return nil, errors.New("local in_place execution is not implemented")
	}
	if spec.RunID == "" || spec.CapabilityRef == "" || !filepath.IsAbs(spec.Base) || !filepath.IsAbs(spec.Workspace.Directory) {
		return nil, errors.New("launch needs run/capability identity and absolute paths")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.handles == nil {
		l.handles = map[string]*localHandle{}
	}
	if existing := l.handles[spec.Base]; existing != nil {
		if !reflect.DeepEqual(existing.spec, spec) {
			return nil, errors.New("prepared launch changed")
		}
		return existing, nil
	}
	prepared := spec.Workspace
	if spec.Workspace.Source != "" && spec.Workspace.HistoryDirectory == "" {
		if filepath.Join(spec.Base, "workspace") != spec.Workspace.Directory {
			return nil, errors.New("workspace does not match provider destination")
		}
		metadataPath := filepath.Join(spec.Base, "prepared.json")
		if encoded, readErr := os.ReadFile(metadataPath); readErr == nil {
			if err = json.Unmarshal(encoded, &prepared); err != nil {
				return nil, err
			}
			if prepared.Directory != spec.Workspace.Directory || prepared.Source != spec.Workspace.Source || prepared.HistoryDirectory != filepath.Join(spec.Base, "history.git") {
				return nil, errors.New("prepared workspace identity changed")
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return nil, readErr
		} else {
			snapshot, captureErr := workspacefs.CaptureSource(ctx, spec.Workspace.Source, spec.Base, workspacefs.DefaultLimits)
			if captureErr != nil {
				return nil, captureErr
			}
			history, prepareErr := workspacefs.Prepare(ctx, spec.Base, snapshot)
			if prepareErr != nil {
				return nil, prepareErr
			}
			prepared.HistoryDirectory = history.Directory
			prepared.BaselineCommit = history.BaselineCommit
			prepared.BaselineFingerprint = snapshot.Fingerprint
			if err = savePreparation(metadataPath, prepared); err != nil {
				return nil, err
			}
		}
		history := workspacefs.History{Base: spec.Base, Directory: prepared.HistoryDirectory, BaselineCommit: prepared.BaselineCommit}
		if _, err = history.Load(ctx, prepared.BaselineCommit, prepared.BaselineFingerprint); err != nil {
			return nil, err
		}
		current, captureErr := workspacefs.CaptureResult(ctx, prepared.Directory, workspacefs.DefaultLimits)
		if captureErr != nil {
			return nil, captureErr
		}
		if current.Fingerprint != prepared.BaselineFingerprint {
			return nil, errors.New("prepared source workspace changed before start")
		}
	}
	info, err := os.Stat(spec.Workspace.Directory)
	if err != nil || !info.IsDir() {
		return nil, errors.New("prepared workspace is missing")
	}
	for _, name := range []string{"home", "tmp"} {
		if err = os.MkdirAll(filepath.Join(spec.Base, name), 0700); err != nil {
			return nil, err
		}
	}
	// Pin the requested posture independently of memory. Re-prepare never upgrades
	// an existing workspace to stronger isolation or silently changes its inputs.
	if err = savePreparation(filepath.Join(spec.Base, "launch.json"), spec); err != nil {
		return nil, err
	}
	h := &localHandle{owner: l, spec: spec, prepared: prepared}
	l.handles[spec.Base] = h
	return h, nil
}
func (l *Local) Start(ctx context.Context, handle Handle, command Command) (Session, error) {
	h, ok := handle.(*localHandle)
	if !ok || h.owner != l {
		return nil, errors.New("foreign provider handle")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return nil, ErrAlreadyStarted
	}
	if len(command.Argv) == 0 || !filepath.IsAbs(command.Argv[0]) {
		return nil, errors.New("command requires an absolute executable")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s := &localSession{handle: h, done: make(chan struct{})}
	commandCtx := ctx
	cancel := func() {}
	if !h.spec.Posture.Deadline.IsZero() {
		commandCtx, cancel = context.WithDeadline(ctx, h.spec.Posture.Deadline)
	}
	cmd := exec.CommandContext(commandCtx, command.Argv[0], command.Argv[1:]...)
	s.cmd = cmd
	cmd.Dir = h.spec.Workspace.Directory
	if command.Directory != "" {
		if !filepath.IsAbs(command.Directory) {
			cancel()
			return nil, errors.New("working directory must be absolute")
		}
		cmd.Dir = command.Directory
	}
	if command.Interact == nil {
		cmd.Stdin = bytes.NewReader(command.Input)
	}
	base := h.spec.Base
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(base, "home"), "TMPDIR=" + filepath.Join(base, "tmp"), "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CEILING_DIRECTORIES=" + base + string(os.PathListSeparator) + filepath.Dir(cmd.Dir), "GIT_TERMINAL_PROMPT=0", "MITOS_WORKSPACE=" + h.spec.Workspace.Directory}
	if command.NativeEnv != nil {
		cmd.Env = append([]string(nil), command.NativeEnv...)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error { return killGroup(s) }
	var protocol *protocolStream
	if command.Interact != nil {
		input, err := cmd.StdinPipe()
		if err != nil {
			cancel()
			return nil, err
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			input.Close()
			cancel()
			return nil, err
		}
		protocol = &protocolStream{Reader: output, WriteCloser: input, readCloser: output}
	} else {
		cmd.Stdout = &s.output
	}
	cmd.Stderr = &s.output
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	h.started = true
	s.events = []Event{{Offset: 1, Type: "status", Status: "started", At: time.Now().UTC()}}
	go func() {
		defer cancel()
		if protocol != nil {
			protocolErr := command.Interact(commandCtx, protocol, &s.output)
			_ = protocol.Close()
			_ = killGroup(s)
			waitErr := cmd.Wait()
			// The protocol decides successful turn completion; shutdown kills the
			// persistent app-server only after its result has been captured.
			s.err = protocolErr
			if commandCtx.Err() != nil {
				s.err = errors.Join(protocolErr, commandCtx.Err(), waitErr)
			}
		} else {
			s.err = cmd.Wait()
		}
		_ = killGroup(s)
		code := cmd.ProcessState.ExitCode()
		s.mu.Lock()
		s.events = append(s.events, Event{Offset: 2, Type: "status", Status: "exited", At: time.Now().UTC(), ExitCode: &code})
		s.mu.Unlock()
		close(s.done)
	}()
	return s, nil
}
func killGroup(s *localSession) error {
	err := syscall.Kill(-s.cmd.Process.Pid, syscall.SIGKILL)
	if errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return err
}
func (l *Local) session(session Session) (*localSession, error) {
	s, ok := session.(*localSession)
	if !ok || s.handle.owner != l {
		return nil, errors.New("foreign provider session")
	}
	return s, nil
}
func (l *Local) Stream(ctx context.Context, session Session, after uint64) (<-chan Event, error) {
	s, err := l.session(session)
	if err != nil {
		return nil, err
	}
	if after > 2 {
		return nil, errors.New("stream offset is beyond this session")
	}
	out := make(chan Event, 2)
	go func() {
		defer close(out)
		send := func() {
			s.mu.Lock()
			events := append([]Event(nil), s.events...)
			s.mu.Unlock()
			for _, event := range events {
				if event.Offset > after {
					select {
					case out <- event:
						after = event.Offset
					case <-ctx.Done():
						return
					}
				}
			}
		}
		send()
		select {
		case <-s.done:
			send()
		case <-ctx.Done():
		}
	}()
	return out, nil
}
func (l *Local) Control(ctx context.Context, session Session, command string) error {
	if command != "interrupt" {
		return ErrUnsupported
	}
	return l.Stop(ctx, session)
}
func (l *Local) Snapshot(ctx context.Context, session Session) (string, error) {
	if _, err := l.session(session); err != nil {
		return "", err
	}
	return "", ErrUnsupported
}
func (l *Local) Stop(ctx context.Context, session Session) error {
	s, err := l.session(session)
	if err != nil {
		return err
	}
	select {
	case <-s.done:
		return nil
	default:
	}
	if err = killGroup(s); err != nil {
		return err
	}
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (l *Local) Harvest(ctx context.Context, session Session) (Harvest, error) {
	s, err := l.session(session)
	if err != nil {
		return Harvest{}, err
	}
	select {
	case <-s.done:
	default:
		return Harvest{}, errors.New("harvest requires a stopped session")
	}
	result := Harvest{Output: s.output.Bytes()}
	spec := s.handle.spec
	spec.Workspace = s.handle.prepared
	if spec.Workspace.HistoryDirectory == "" {
		return result, nil
	}
	history := workspacefs.History{Base: spec.Base, Directory: spec.Workspace.HistoryDirectory, BaselineCommit: spec.Workspace.BaselineCommit}
	before, err := history.Load(ctx, spec.Workspace.BaselineCommit, spec.Workspace.BaselineFingerprint)
	if err != nil {
		return result, err
	}
	change, err := history.Harvest(ctx, before)
	if err != nil {
		return result, err
	}
	result.Change = &change
	return result, nil
}

type limitedBuffer struct {
	mu        sync.Mutex
	b         bytes.Buffer
	truncated bool
	total     uint64
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += uint64(n)
	room := (2 << 20) - b.b.Len()
	if room > 0 {
		b.b.Write(p[:min(room, n)])
	}
	if n > room {
		b.truncated = true
	}
	return n, nil
}
func (b *limitedBuffer) Total() uint64 { b.mu.Lock(); defer b.mu.Unlock(); return b.total }
func (b *limitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := append([]byte(nil), b.b.Bytes()...)
	if b.truncated {
		out = append(out, []byte("\n[Output capped at 2 MiB]\n")...)
	}
	return out
}

func savePreparation(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if previous, readErr := os.ReadFile(path); readErr == nil {
		if !bytes.Equal(previous, encoded) {
			return errors.New("saved launch preparation changed")
		}
		return nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".preparing-")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	_, err = file.Write(encoded)
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Link(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

// protocolStream is closed on protocol completion; CommandContext kills the
// process group on cancellation, unblocking pipe reads as well.
type protocolStream struct {
	io.Reader
	io.WriteCloser
	readCloser io.Closer
}

func (s *protocolStream) Close() error {
	return errors.Join(s.WriteCloser.Close(), s.readCloser.Close())
}
