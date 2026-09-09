package studio

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Engine struct {
	Store   *Store
	Apps    []Application
	Root    string
	mu      sync.Mutex
	cancel  map[string]context.CancelFunc
	ctx     context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
	actions sync.Mutex
}

func NewEngine(s *Store, apps []Application, root string) (*Engine, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{Store: s, Apps: apps, Root: abs, cancel: map[string]context.CancelFunc{}, ctx: ctx, stop: cancel}
	for _, r := range s.List() {
		if r.Status == "running" && !r.Sample {
			if err = s.Park(r.ID, recoveryDecision()); err != nil {
				cancel()
				return nil, err
			}
		}
	}
	e.wg.Add(1)
	go e.dispatch()
	return e, nil
}
func (e *Engine) Close() { e.stop(); e.wg.Wait() }
func (e *Engine) App(id string) (Application, bool) {
	for _, a := range e.Apps {
		if a.ID == id {
			return a, true
		}
	}
	return Application{}, false
}

func Within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
func (e *Engine) Workspace(path string) (string, error) {
	if !filepath.IsAbs(path) {
		path = filepath.Join(e.Root, path)
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errors.New("workspace does not exist; choose a directory under " + e.Root)
	}
	if !Within(e.Root, path) {
		return "", errors.New("choose a workspace under " + e.Root)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("workspace must be a directory")
	}
	return path, nil
}
func (e *Engine) Launch(b Brief, parent string) (Run, error) {
	app, ok := e.App(b.AppID)
	if !ok {
		return Run{}, errors.New("choose an installed application")
	}
	if err := b.Validate(); err != nil {
		return Run{}, err
	}
	p, err := e.Workspace(b.Workspace)
	if err != nil {
		return Run{}, err
	}
	b.Workspace = p
	return e.Store.Create(b, app.Name, parent, false)
}
func (e *Engine) Retry(id string) (Run, error) {
	e.actions.Lock()
	defer e.actions.Unlock()
	return e.retry(id)
}
func (e *Engine) retry(id string) (Run, error) {
	r, err := e.Store.Get(id)
	if err != nil {
		return Run{}, err
	}
	if !r.Terminal() {
		return Run{}, ErrConflict
	}
	// A repeated submission returns the same child, never duplicate execution.
	for _, child := range e.Store.List() {
		if child.ParentID == id {
			return child, nil
		}
	}
	return e.Launch(r.Brief, id)
}
func (e *Engine) Resolve(id, decisionID, choice string) (string, error) {
	e.actions.Lock()
	defer e.actions.Unlock()
	if err := e.Store.Resolve(id, decisionID, choice); err != nil {
		return "", err
	}
	if choice == "retry" {
		r, err := e.retry(id)
		return r.ID, err
	}
	return id, nil
}
func (e *Engine) Cancel(id string) error {
	e.actions.Lock()
	defer e.actions.Unlock()
	r, err := e.Store.Get(id)
	if err != nil {
		return err
	}
	if r.Status == "cancelled" {
		return nil
	}
	if r.Terminal() {
		return ErrConflict
	}
	if err = e.Store.RecordDecision(id, "abort", "The operator stopped this work."); err != nil {
		return err
	}
	if err = e.Store.Transition(id, "run_cancelled", "cancelled", "Stopped", "Stopped by you. The execution record is preserved."); err != nil {
		return err
	}
	e.mu.Lock()
	if cancel := e.cancel[id]; cancel != nil {
		cancel()
	}
	e.mu.Unlock()
	return nil
}

func (e *Engine) dispatch() {
	defer e.wg.Done()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-e.ctx.Done():
			return
		case <-tick.C:
			runs := e.Store.List()
			for i := len(runs) - 1; i >= 0; i-- {
				r := runs[i]
				if r.Status != "queued" || r.Sample {
					continue
				}
				e.mu.Lock()
				if len(e.cancel) >= 2 {
					e.mu.Unlock()
					break
				}
				if _, ok := e.cancel[r.ID]; ok {
					e.mu.Unlock()
					continue
				}
				ctx, cancel := context.WithTimeout(e.ctx, time.Duration(r.Brief.TimeoutMinutes)*time.Minute)
				e.cancel[r.ID] = cancel
				e.mu.Unlock()
				e.wg.Add(1)
				go func() {
					defer e.wg.Done()
					defer cancel()
					defer func() { e.mu.Lock(); delete(e.cancel, r.ID); e.mu.Unlock() }()
					e.execute(ctx, r)
				}()
			}
		}
	}
}

func (e *Engine) execute(ctx context.Context, r Run) {
	err := e.perform(ctx, r)
	if err == nil {
		return
	}
	current, getErr := e.Store.Get(r.ID)
	if getErr != nil || current.Terminal() {
		return
	}
	if e.ctx.Err() != nil {
		_ = e.Store.Park(r.ID, recoveryDecision())
		return
	}
	message := err.Error()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		message = "The time limit was reached. Review the output and try again with a longer limit."
	}
	_ = e.Store.Transition(r.ID, "run_failed", "failed", "Needs a new attempt", message)
}

func (e *Engine) perform(ctx context.Context, r Run) error {
	app, ok := e.App(r.Brief.AppID)
	if !ok {
		return errors.New("application is no longer installed")
	}
	if err := e.Store.RecordDecision(r.ID, "advance", "Prepare a fresh workspace for the brief."); err != nil {
		return err
	}
	if err := e.Store.Transition(r.ID, "run_started", "running", "Preparing workspace", "Making a fresh copy. The original workspace stays untouched."); err != nil {
		return err
	}
	source, err := e.Workspace(r.Brief.Workspace)
	if err != nil {
		return err
	}
	base := filepath.Join(e.Store.Dir(), "workspaces", r.ID)
	workspace := filepath.Join(base, "workspace")
	if err = os.MkdirAll(workspace, 0700); err != nil {
		return err
	}
	if err = copyWorkspace(ctx, source, workspace, e.Store.Dir()); err != nil {
		return err
	}
	if app.Builtin {
		return e.inventory(ctx, r, workspace)
	}
	if err = e.Store.Transition(r.ID, "capability_started", "running", "Working on the brief", "The application is executing in its workspace copy."); err != nil {
		return err
	}
	request := map[string]any{"run_id": r.ID, "brief": r.Brief, "workspace": workspace}
	input, _ := json.Marshal(request)
	output, runErr := executeCommand(ctx, app.Command, workspace, base, input)
	if err = e.Store.SaveArtifact(r.ID, "output.txt", output); err != nil {
		return err
	}
	if runErr != nil {
		return fmt.Errorf("application failed: %w. Open output.txt for details", runErr)
	}
	if err = e.Store.RecordDecision(r.ID, "verify", "Run the application's acceptance command outside the workspace."); err != nil {
		return err
	}
	if err = e.Store.Transition(r.ID, "verification_started", "running", "Checking the result", "Running the separately configured acceptance command."); err != nil {
		return err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	verdict, verifyErr := executeCommand(verifyCtx, app.Acceptance, app.Directory, base, input)
	if err = e.Store.SaveArtifact(r.ID, "acceptance.txt", verdict); err != nil {
		return err
	}
	if verifyErr != nil {
		return fmt.Errorf("acceptance did not pass: %w. Review acceptance.txt before retrying", verifyErr)
	}
	if err = e.Store.Transition(r.ID, "verification_recorded", "running", "Acceptance passed", "The configured acceptance command exited successfully."); err != nil {
		return err
	}
	return e.Store.Complete(r.ID, "The application finished and its acceptance command passed. Review the output and the saved workspace before using the result.", true)
}

// Local execution is a trusted-process provider, not a security sandbox. It
// accepts installed argv only, clears inherited credentials, caps output, and
// stops the process group on cancellation. Strong isolation is an external seam.
func executeCommand(ctx context.Context, argv []string, dir, base string, input []byte) ([]byte, error) {
	if len(argv) == 0 {
		return nil, errors.New("missing command")
	}
	home := filepath.Join(base, "home")
	tmp := filepath.Join(base, "tmp")
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(tmp, 0700); err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "TMPDIR=" + tmp, "LANG=C.UTF-8", "MITOS_WORKSPACE=" + filepath.Join(base, "workspace")}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 2 * time.Second
	var output limitedBuffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	err := cmd.Run()
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return output.Bytes(), err
}

type limitedBuffer struct {
	mu        sync.Mutex
	b         bytes.Buffer
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	room := (2 << 20) - b.b.Len()
	if room > 0 {
		b.b.Write(p[:min(room, len(p))])
	}
	if n > room {
		b.truncated = true
	}
	return n, nil
}
func (b *limitedBuffer) Bytes() []byte {
	if b.truncated {
		b.b.WriteString("\n[Output capped at 2 MiB]\n")
	}
	return b.b.Bytes()
}

func copyWorkspace(ctx context.Context, source, dest, dataDir string) error {
	var total int64
	count := 0
	return filepath.WalkDir(source, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if path == source {
			return nil
		}
		if d.IsDir() && (Within(dataDir, path) || excluded(d.Name())) {
			return filepath.SkipDir
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		total += info.Size()
		count++
		if total > 64<<20 || count > 10000 {
			return errors.New("workspace exceeds 64 MiB or 10,000 files; choose a smaller directory")
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm()&0700)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(in, 64<<20))
		closeErr := out.Close()
		if err != nil {
			return err
		}
		return closeErr
	})
}
func excluded(name string) bool {
	switch name {
	case ".git", ".mitos", ".mitos-demo", ".runs", ".direnv", ".agent-worktrees", "node_modules", "vendor", "target", "bin", "_build", "deps", ".cache":
		return true
	}
	return false
}

func (e *Engine) inventory(ctx context.Context, r Run, workspace string) error {
	if err := e.Store.Transition(r.ID, "capability_started", "running", "Reading the workspace", "Collecting paths, sizes, and content hashes."); err != nil {
		return err
	}
	type file struct {
		Path   string `json:"path"`
		Bytes  int64  `json:"bytes"`
		SHA256 string `json:"sha256"`
	}
	files := []file{}
	var total int64
	tree := sha256.New()
	err := filepath.WalkDir(workspace, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(workspace, path)
		sum := sha256.Sum256(b)
		digest := hex.EncodeToString(sum[:])
		files = append(files, file{rel, int64(len(b)), digest})
		total += int64(len(b))
		fmt.Fprintf(tree, "%s\x00%s\n", rel, digest)
		return nil
	})
	if err != nil {
		return err
	}
	report := struct {
		Files       []file `json:"files"`
		TotalBytes  int64  `json:"total_bytes"`
		Fingerprint string `json:"fingerprint"`
	}{files, total, hex.EncodeToString(tree.Sum(nil))}
	b, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	if err = e.Store.SaveArtifact(r.ID, "inventory.json", b); err != nil {
		return err
	}
	summary := fmt.Sprintf("Inventoried %d files (%d bytes). Every included file has a SHA-256 fingerprint. Source files were not changed.", len(files), total)
	text := fmt.Sprintf("# %s\n\n%s\n\nWorkspace fingerprint: %s\n\nSymlinks and generated directories are excluded. This is a file inventory, not a code-quality or security assessment.\n", r.Brief.Title, summary, report.Fingerprint)
	if err = e.Store.SaveArtifact(r.ID, "report.md", []byte(text)); err != nil {
		return err
	}
	// Validate the harvested artifact through the independent integrity reader.
	if _, err = e.Store.Artifact(r.ID, "inventory.json"); err != nil {
		return err
	}
	if err = e.Store.Transition(r.ID, "verification_recorded", "running", "Inventory checked", "The saved inventory matches its recorded checksum."); err != nil {
		return err
	}
	return e.Store.Complete(r.ID, summary, true)
}
