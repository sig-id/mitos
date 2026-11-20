package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	workspacefs "mitos/internal/workspace"
)

type Local struct {
	Confinement Confinement
	// sandbox, when set, runs every command in an enforcing container
	// runtime that attests its posture (see NewNucleus).
	sandbox *nucleusRuntime
	// name is the provider name admission compiles against; "local" unless set.
	name string
	// helper is the host binary mounted into mediated sandboxes to run the
	// in-sandbox egress loop.
	helper  string
	mu      sync.Mutex
	handles map[string]*localHandle
}

// SetEgressHelper records the host executable used as the in-sandbox egress
// loop for mediated launches.
func (l *Local) SetEgressHelper(path string) { l.helper = path }

type localHandle struct {
	prepared Workspace
	owner    *Local
	spec     LaunchSpec
	started  bool
	sequence uint64
	mu       sync.Mutex
}

func (h *localHandle) ID() string        { return h.spec.Base }
func (h *localHandle) Workspace() string { return h.spec.Workspace.Directory }

type localSession struct {
	sequence uint64
	handle   *localHandle
	cmd      *exec.Cmd
	done     chan struct{}
	output   limitedBuffer
	err      error
	events   []Event
	sandbox  *sandboxRun
	mu       sync.Mutex
}

func (s *localSession) ID() string              { return s.handle.ID() }
func (s *localSession) PID() int                { return s.cmd.Process.Pid }
func (s *localSession) Done() <-chan struct{}   { return s.done }
func (s *localSession) Result() ([]byte, error) { <-s.done; return s.output.Bytes(), s.err }
func (s *localSession) OutputOffset() uint64    { return s.output.Total() }
func (l *Local) Capabilities() Capabilities {
	if l.sandbox != nil {
		return l.sandbox.capabilities()
	}
	if l.Confinement.enabled() {
		return l.Confinement.capabilities()
	}
	return Capabilities{Isolation: "process", Egress: "unrestricted", DataMount: "unsupported", NetModes: []string{"unrestricted"}}
}

// Name is the provider name launches are admitted against.
func (l *Local) Name() string {
	if l.name == "" {
		return "local"
	}
	return l.name
}

func (l *Local) Prepare(ctx context.Context, spec LaunchSpec) (Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name := l.Name()
	admitted, err := Compile(Request{Resources: spec.Posture.Resources, TrustTier: spec.TrustTier, Provider: name, NetMode: spec.Posture.NetMode, WorkspaceMode: spec.Workspace.Mode, BrokerEndpoint: spec.Posture.BrokerEndpoint}, map[string]Capabilities{name: l.Capabilities()}, spec.Development)
	if err != nil || spec.Provider != name || admitted.TrustTier != spec.TrustTier || !reflect.DeepEqual(admitted.Capabilities, spec.Capabilities) || admitted.Posture.NetMode != spec.Posture.NetMode || admitted.Posture.DataMount != spec.Posture.DataMount {
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
	return l.startCommand(ctx, h, command)
}

// Next runs another invocation in the same prepared workspace and posture.
// Only the latest successfully exited process may advance the attempt once.
func (l *Local) Next(ctx context.Context, previous Session, command Command) (Session, error) {
	s, err := l.session(previous)
	if err != nil {
		return nil, err
	}
	h := s.handle
	h.mu.Lock()
	defer h.mu.Unlock()
	select {
	case <-s.done:
	default:
		return nil, errors.New("previous command is still running")
	}
	if h.sequence != s.sequence || s.err != nil {
		return nil, errors.New("cannot continue failed or superseded command")
	}
	return l.startCommand(ctx, h, command)
}

func (l *Local) startCommand(ctx context.Context, h *localHandle, command Command) (Session, error) {
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
	argv := command.Argv
	base := h.spec.Base
	if h.owner.Confinement.enabled() {
		var confineErr error
		posture := h.owner.Confinement
		posture.NetMode = h.spec.Posture.NetMode
		// A per-launch mediated egress endpoint isolates this workload's
		// network namespace even under an unrestricted posture.
		if h.spec.Posture.BrokerEndpoint != "" {
			if !strings.HasPrefix(h.spec.Posture.BrokerEndpoint, "unix://") {
				cancel()
				return nil, errors.New("mediated local egress requires a unix broker endpoint")
			}
			posture.EgressSocket = strings.TrimPrefix(h.spec.Posture.BrokerEndpoint, "unix://")
			posture.HelperExecutable = h.owner.helper
		}
		if argv, confineErr = confineArgv(posture, argv, base, h.spec.Workspace.Directory); confineErr != nil {
			cancel()
			return nil, confineErr
		}
	}
	dir := h.spec.Workspace.Directory
	if command.Directory != "" {
		if !filepath.IsAbs(command.Directory) {
			cancel()
			return nil, errors.New("working directory must be absolute")
		}
		dir = command.Directory
	}
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + filepath.Join(base, "home"), "TMPDIR=" + filepath.Join(base, "tmp"), "LANG=C.UTF-8", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CEILING_DIRECTORIES=" + base + string(os.PathListSeparator) + filepath.Dir(dir), "GIT_TERMINAL_PROMPT=0", "MITOS_WORKSPACE=" + h.spec.Workspace.Directory}
	if command.NativeEnv != nil {
		env = append([]string(nil), command.NativeEnv...)
	}
	if h.owner.sandbox != nil {
		run, err := h.owner.sandbox.launch(h.spec, argv, dir, env)
		if err != nil {
			cancel()
			return nil, err
		}
		s.sandbox = run
		argv, dir, env = run.argv, run.dir, run.env
	}
	cmd := exec.CommandContext(commandCtx, argv[0], argv[1:]...)
	s.cmd = cmd
	cmd.Dir = dir
	cmd.Env = env
	if command.Interact == nil {
		cmd.Stdin = bytes.NewReader(command.Input)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if s.sandbox != nil {
		cmd.ExtraFiles = s.sandbox.files
		// The supervisor dies with the server; Nucleus's reaper then kills
		// the container cgroup, so a crashed server leaves nothing running.
		// The signal follows the forking thread, an unlocked runtime thread
		// that Go does not retire.
		cmd.SysProcAttr.Pdeathsig = syscall.SIGKILL
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Cancel = func() error { return killGroup(s) }
	var protocol *protocolStream
	if command.Interact != nil {
		input, err := cmd.StdinPipe()
		if err != nil {
			s.closeSandbox()
			cancel()
			return nil, err
		}
		output, err := cmd.StdoutPipe()
		if err != nil {
			input.Close()
			s.closeSandbox()
			cancel()
			return nil, err
		}
		protocol = &protocolStream{Reader: output, WriteCloser: input, readCloser: output}
	} else {
		cmd.Stdout = &s.output
		if command.Stdout != nil {
			cmd.Stdout = io.MultiWriter(command.Stdout, &s.output)
		}
	}
	cmd.Stderr = &s.output
	if command.Stderr != nil {
		cmd.Stderr = io.MultiWriter(command.Stderr, &s.output)
	}
	if err := cmd.Start(); err != nil {
		s.closeSandbox()
		cancel()
		return nil, err
	}
	// A sandboxed workload is released from the start gate, and its session
	// handed out and protocol driven, only after the runtime attested the
	// admitted posture.
	var gate chan error
	if s.sandbox != nil {
		gate = make(chan error, 1)
		s.sandbox.begin()
	}
	go func() {
		defer cancel()
		var gateErr error
		if gate != nil {
			gateErr = <-gate
		}
		if gateErr != nil {
			if protocol != nil {
				_ = protocol.Close()
			}
			_ = killGroup(s)
			s.err = errors.Join(gateErr, cmd.Wait())
		} else if protocol != nil {
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
		if s.sandbox != nil {
			if err := s.sandbox.finish(); err != nil {
				s.err = errors.Join(s.err, err)
			}
			if s.err != nil && s.sandbox.memoryExhausted() {
				s.err = errors.Join(ErrMemoryExhausted, s.err)
			}
		}
		code := cmd.ProcessState.ExitCode()
		s.mu.Lock()
		s.events = append(s.events, Event{Offset: 2, Type: "status", Status: "exited", At: time.Now().UTC(), ExitCode: &code})
		s.mu.Unlock()
		close(s.done)
	}()
	if gate != nil {
		err := s.sandbox.await(commandCtx, nil)
		gate <- err
		if err != nil {
			<-s.done
			output := strings.TrimSpace(string(s.output.Bytes()))
			if output != "" {
				err = fmt.Errorf("%w: %s", err, output)
			}
			return nil, err
		}
	}
	h.started = true
	h.sequence++
	s.sequence = h.sequence
	s.mu.Lock()
	s.events = append([]Event{{Offset: 1, Type: "status", Status: "started", At: time.Now().UTC()}}, s.events...)
	s.mu.Unlock()
	return s, nil
}

func (s *localSession) closeSandbox() {
	if s.sandbox != nil {
		s.sandbox.close()
	}
}

func killGroup(s *localSession) error {
	pid := s.cmd.Process.Pid
	if s.sandbox != nil {
		// Revoking the container ends the workload; its supervisor then
		// reports the summary and exits. Killing the supervisor now would
		// lose the summary and make every outcome untrusted, so it is killed
		// only if it outlives the grace period.
		if revoked, err := s.sandbox.kill(); revoked && err == nil {
			time.AfterFunc(nucleusExitGrace, func() {
				select {
				case <-s.done:
				default:
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
			})
			return nil
		}
	}
	err := syscall.Kill(-pid, syscall.SIGKILL)
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
