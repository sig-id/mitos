package studio

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"mitos/internal/compute"
	"mitos/internal/decision"
	"mitos/internal/desk"
	"mitos/internal/messaging"
	workspacefs "mitos/internal/workspace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Engine struct {
	Messages *messaging.Service
	Desk     *desk.Manager
	provider compute.Provider
	Store    *Store
	Apps     []Application
	Root     string
	mu       sync.Mutex
	cancel   map[string]context.CancelFunc
	ctx      context.Context
	stop     context.CancelFunc
	wg       sync.WaitGroup
	actions  sync.Mutex
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
	e := &Engine{provider: &compute.Local{}, Store: s, Apps: apps, Root: abs, cancel: map[string]context.CancelFunc{}, ctx: ctx, stop: cancel}
	if err = s.ensureHome(); err != nil {
		cancel()
		return nil, err
	}
	// Load confirmed project workspaces before recovering or scheduling tasks.
	e.Desk, err = desk.OpenStored(filepath.Join(s.Dir(), "desk"), abs, s)
	if err != nil {
		cancel()
		return nil, err
	}
	initialized := false
	defer func() {
		if !initialized {
			e.Desk.Close()
		}
	}()
	if err = s.recoverDecisionRequests(); err != nil {
		cancel()
		return nil, err
	}
	for _, r := range s.List() {
		if r.ExecutionKind == "interactive" {
			continue
		}
		if r.Status == "stopping" && !r.Sample {
			if err = s.Transition(r.ID, "run_cancelled", "cancelled", "Stopped", "The server restarted after a stop request. Any already harvested evidence is preserved; no command was restarted."); err != nil {
				cancel()
				return nil, err
			}
		} else if !r.Sample && (r.Status == "running" || (r.Status == "queued" && r.Claim != nil && r.Claim.ReleasedAt == nil)) {
			if err = s.Park(r.ID, checkpointOption(r, recoveryDecision())); err != nil {
				cancel()
				return nil, err
			}
		}
	}
	for _, run := range s.List() {
		if err = s.interruptServiceCondition(run.ID); err != nil {
			cancel()
			return nil, err
		}
		if run.Health != nil && run.Health.Status == "watching" {
			if err = s.closeHealth(run.ID, run.Health.MonitorID, "Server restart ended the old health monitor."); err != nil {
				cancel()
				return nil, err
			}
		}
	}
	startupCycle := time.Now().UTC()
	if err = e.reconcileServiceLifecycle(); err != nil {
		cancel()
		return nil, err
	}
	if err = e.reconcileServiceReplacements(); err != nil {
		cancel()
		return nil, err
	}
	if err = e.reconcileContinuousServices(startupCycle); err != nil {
		cancel()
		return nil, err
	}
	if err = e.reconcileServiceEvents(); err != nil {
		cancel()
		return nil, err
	}
	if err = e.reconcileServiceRetries(time.Now().UTC()); err != nil {
		cancel()
		return nil, err
	}
	if err = e.scheduleServices(startupCycle, true); err != nil {
		cancel()
		return nil, err
	}
	initialized = true
	e.wg.Add(1)
	go e.dispatch()
	return e, nil
}
func (e *Engine) Close() {
	e.stop()
	if e.Messages != nil {
		e.Messages.Close()
	}
	if e.Desk != nil {
		e.Desk.Close()
	}
	e.wg.Wait()
}
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
		return "", errors.New("workspace does not exist; choose an existing project directory")
	}
	allowed := Within(e.Root, path)
	if !allowed && e.Desk != nil {
		for _, project := range e.Desk.Projects() {
			if Within(project.Directory, path) {
				allowed = true
				break
			}
		}
	}
	if !allowed {
		return "", errors.New("open this directory as a project to confirm it as a workspace")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return "", errors.New("workspace must be a directory")
	}
	return path, nil
}
func (e *Engine) Launch(b Brief, parent string) (Run, error) {
	b, name, err := e.prepareBrief(b)
	if err != nil {
		return Run{}, err
	}
	return e.Store.Create(b, name, parent, false)
}
func (e *Engine) prepareBrief(b Brief) (Brief, string, error) {
	if b.ProjectID != "" {
		project, err := e.Desk.Project(b.ProjectID)
		if err != nil {
			return Brief{}, "", err
		}
		if b.Workspace != "" && b.Workspace != project.Directory {
			return Brief{}, "", errors.New("workspace differs from the selected project")
		}
		b.Workspace = project.Directory
	}
	app, ok := e.App(b.AppID)
	if !ok {
		return Brief{}, "", errors.New("choose an installed application")
	}
	if !app.Builtin {
		if _, err := compileApplication(app); err != nil {
			return Brief{}, "", err
		}
		if err := app.Acceptance.Validate(); err != nil {
			return Brief{}, "", err
		}
	}
	if app.Supervision != nil {
		if err := app.Supervision.Validate(); err != nil {
			return Brief{}, "", err
		}
	}
	if app.Observation != nil {
		if err := app.Observation.Validate(); err != nil {
			return Brief{}, "", err
		}
	}
	if err := b.Validate(); err != nil {
		return Brief{}, "", err
	}
	if app.Builtin {
		for _, dep := range b.Scheduling.Deps {
			if dep.Payload == "data" {
				return Brief{}, "", errors.New("the inventory entrypoint does not consume dependency artifacts; choose a command application")
			}
		}
	}
	if deadline := b.Scheduling.Deadline; deadline != nil && !time.Now().Before(*deadline) {
		return Brief{}, "", errors.New("the absolute task deadline has expired")
	}
	p, err := e.Workspace(b.Workspace)
	if err != nil {
		return Brief{}, "", err
	}
	b.Workspace = p
	if !app.Builtin && len(b.Ownership.Paths) == 0 {
		b.Ownership.Paths = []string{"."}
	}
	return b, app.Name, nil
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
	if r.ExecutionKind == "interactive" {
		return Run{}, errors.New("replace the executor through its durable task")
	}
	if r.Decision != nil && r.Decision.Source != nil {
		return Run{}, ErrConflict
	}
	if !r.Terminal() && !(r.Status == "parked" && r.Decision != nil && r.Decision.Resolution != nil && r.Decision.Choice == "retry") {
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
	return e.ResolveAs(id, decisionID, choice, "", "console", decision.LocalOperator())
}
func (e *Engine) Cancel(id string) error {
	e.actions.Lock()
	defer e.actions.Unlock()
	return e.cancelWork(id, "The operator stopped this work.")
}

// Caller holds actions. Service lifecycle stops use the same durable stop path.
func (e *Engine) cancelWork(id, reason string) error {
	r, err := e.Store.Get(id)
	if err != nil {
		return err
	}
	if r.ExecutionKind == "interactive" {
		return e.Desk.Stop(r.SessionID)
	}
	if r.Decision != nil && r.Decision.Source != nil {
		return e.Store.cancelAgentRun(id)
	}
	if r.Status == "cancelled" || r.Status == "stopping" {
		return nil
	}
	if r.Terminal() {
		if handled, err := e.Store.cancelServiceRetry(id, reason); handled || err != nil {
			return err
		}
		return ErrConflict
	}
	if err = e.Store.RecordDecision(id, "abort", reason); err != nil {
		return err
	}
	if err = e.Store.Transition(id, "cancellation_requested", "stopping", "Stopping", "Stopping the command and preserving its output and workspace change."); err != nil {
		return err
	}
	e.mu.Lock()
	cancel := e.cancel[id]
	if cancel != nil {
		cancel()
	}
	e.mu.Unlock()
	if cancel == nil {
		return e.Store.Transition(id, "run_cancelled", "cancelled", "Stopped", reason+" The execution record is preserved.")
	}
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
			if err := e.Store.expireAgentSessions(); err != nil {
				continue
			}
			cycleTime := time.Now().UTC()
			if err := e.reconcileServiceLifecycle(); err != nil {
				continue
			}
			if err := e.reconcileServiceReplacements(); err != nil {
				continue
			}
			if err := e.reconcileContinuousServices(cycleTime); err != nil {
				continue
			}
			if err := e.reconcileServiceEvents(); err != nil {
				continue
			}
			if err := e.reconcileServiceRetries(time.Now().UTC()); err != nil {
				continue
			}
			if err := e.scheduleServices(cycleTime, false); err != nil {
				continue
			}
			e.reconcileDecisions()
			e.mu.Lock()
			active := make([]string, 0, len(e.cancel))
			for id := range e.cancel {
				active = append(active, id)
			}
			runs, readyErr := e.Store.ReadyRuns(active)
			e.mu.Unlock()
			if readyErr != nil {
				continue
			}
			for _, candidate := range runs {
				r := candidate.Run
				if r.Status != "queued" || r.Sample || r.ExecutionKind == "interactive" {
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
				current, err := e.Store.Get(r.ID)
				if err != nil || current.Status != "queued" {
					e.mu.Unlock()
					continue
				}
				active = active[:0]
				for id := range e.cancel {
					active = append(active, id)
				}
				acquired, claimErr := e.Store.ScheduleSelected(r.ID, active, candidate.Policy)
				if claimErr != nil || !acquired {
					e.mu.Unlock()
					continue
				}
				deadline := time.Now().Add(time.Duration(r.Brief.TimeoutMinutes) * time.Minute)
				if current.Execution != nil {
					deadline = current.Execution.Deadline
				}
				if taskDeadline := current.Brief.Scheduling.Deadline; taskDeadline != nil && taskDeadline.Before(deadline) {
					deadline = *taskDeadline
				}
				ctx, cancel := context.WithDeadline(e.ctx, deadline)
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
	defer func() {
		// A stop can race with any final transition or parking attempt. Close it
		// after the worker has finished harvesting, even if that transition lost.
		latest, err := e.Store.Get(r.ID)
		if err == nil && latest.Status == "stopping" {
			if e.Store.ReleaseClaims(r.ID, "execution_stopped_and_harvest_finished") != nil {
				return
			}
			_ = e.Store.Transition(r.ID, "run_cancelled", "cancelled", "Stopped", "Stopped. Available execution evidence was preserved.")
		}
	}()
	err := e.perform(ctx, r)
	if err != nil {
		if e.Store.ReleaseClaims(r.ID, "execution_stopped_and_harvest_finished") != nil {
			return
		}
	}
	if err == nil {
		return
	}
	current, getErr := e.Store.Get(r.ID)
	if getErr != nil || current.Terminal() {
		return
	}
	if current.Status == "stopping" {
		_ = e.Store.Transition(r.ID, "run_cancelled", "cancelled", "Stopped", "Stopped. Available output and workspace changes were preserved.")
		return
	}
	if errors.Is(err, errServiceCondition) {
		_ = e.Store.Transition(r.ID, "service_condition_skipped", "cancelled", "Condition not met", "The service condition withheld application execution. No acceptance or failure retry was requested.")
		return
	}
	if errors.Is(err, errHealthRestart) || errors.Is(err, errHealthKill) {
		kind := "service_health_killed"
		if errors.Is(err, errHealthRestart) {
			kind = "service_health_restart"
		}
		_ = e.Store.Transition(r.ID, kind, "failed", "Service unhealthy", "The health policy stopped this attempt after preserving available output and workspace changes.")
		return
	}
	if errors.Is(err, errOwnershipViolation) {
		_ = e.Store.Park(r.ID, checkpointOption(current, ownershipDecision()))
		return
	}
	if errors.Is(err, errDependencyChanged) {
		_ = e.Store.Park(r.ID, checkpointOption(current, dependencyInputDecision()))
		return
	}
	if errors.Is(err, errOracleTampered) {
		_ = e.Store.Park(r.ID, checkpointOption(current, oracleDecision()))
		return
	}
	if e.ctx.Err() != nil {
		_ = e.Store.Park(r.ID, checkpointOption(current, recoveryDecision()))
		return
	}
	if errors.Is(err, errBlockedOnHuman) || errors.Is(err, errObservationLost) {
		_ = e.Store.Park(r.ID, checkpointOption(current, observationDecision(err)))
		return
	}
	message := err.Error()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		message = "The time limit was reached. Review the output and try again with a longer limit."
	}
	_ = e.Store.Transition(r.ID, "run_failed", "failed", "Needs a new attempt", message)
}

// Local execution is a trusted-process provider, not a security sandbox. It
// accepts installed argv only, clears inherited credentials, caps output, and
// stops the process group on cancellation. Strong isolation is an external seam.
func executeCommand(ctx context.Context, argv []string, dir, base string, input []byte) ([]byte, error) {
	return executeObservedCommand(ctx, argv, dir, base, input, nil)
}

func executeObservedCommand(ctx context.Context, argv []string, dir, base string, input []byte, monitor *commandObservation) ([]byte, error) {
	provider := &compute.Local{}
	launch, _ := compute.Compile(compute.Request{}, map[string]compute.Capabilities{"local": provider.Capabilities()}, true)
	launch.RunID = "local-check"
	launch.CapabilityRef = "installed-check"
	launch.Base = base
	launch.Workspace.Directory = filepath.Join(base, "workspace")
	if _, err := os.Stat(launch.Workspace.Directory); err != nil {
		launch.Workspace.Directory = dir
	}
	launch.Posture.Deadline, _ = ctx.Deadline()
	_, output, err := executeProviderCommand(ctx, provider, launch, argv, dir, input, monitor)
	return output, err
}

func copyWorkspace(ctx context.Context, source, dest, dataDir string) error {
	snapshot, err := workspacefs.CaptureSource(ctx, source, dataDir, workspacefs.DefaultLimits)
	if err != nil {
		return err
	}
	return snapshot.WriteTo(dest)
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
	summary := fmt.Sprintf("Inventoried %d files (%d bytes). Every included file has a SHA-256 fingerprint. Source files were not changed.", len(files), total)
	text := fmt.Sprintf("# %s\n\n%s\n\nWorkspace fingerprint: %s\n\nSource ignore rules are honored; intake symlinks, Git metadata, and Studio data are excluded. This is a file inventory, not a code-quality or security assessment.\n", r.Brief.Title, summary, report.Fingerprint)
	if err = e.Store.SaveArtifacts(r.ID, ArtifactData{Name: "inventory.json", Data: b}, ArtifactData{Name: "report.md", Data: []byte(text)}); err != nil {
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
