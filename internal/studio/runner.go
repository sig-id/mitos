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
	"mitos/internal/harness"
	"mitos/internal/messaging"
	"mitos/internal/structuredworker"
	"mitos/internal/workerwake"
	workspacefs "mitos/internal/workspace"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Engine struct {
	actionHTTP          *http.Client
	workerWake          *workerwake.Client
	localWorkerToken    string
	localWorkerIdentity structuredworker.Identity
	Messages            *messaging.Service
	Desk                *desk.Manager
	provider            compute.Provider
	// agentProvider is the provider for native agent execution only; OS
	// confinement is scoped here so other launch paths keep the plain local
	// posture they were admitted against.
	agentProvider compute.Provider
	// nucleus, when configured, enforces the confined postures applications
	// declare in their compute request (ADR 0008); nil otherwise.
	nucleus compute.Provider
	// agentNucleus runs native agents under Nucleus with brokered model
	// access (MITOS_AGENT_CONFINEMENT=nucleus); agentProvider is nucleus.
	agentNucleus bool
	confinement  compute.Confinement
	// egressRelay mediates model transport for network-isolated tenant
	// workers; egressEndpoint is its UNIX socket, empty when confinement is
	// off or the network is fully withheld.
	egressRelay    *compute.EgressRelay
	egressEndpoint string
	Store          *Store
	Apps           []Application
	Root           string
	mu             sync.Mutex
	cancel         map[string]context.CancelFunc
	// submitGrace bounds the wrap-up of attempts that already submitted a
	// result (issue #127); guarded by mu.
	submitGrace map[string]*resultGrace
	ctx         context.Context
	stop        context.CancelFunc
	wg          sync.WaitGroup
	actions     sync.Mutex
	// Serializes idempotent structured-work admission by request key.
	structuredMu sync.Mutex
	setupMu      sync.Mutex
	resolving    map[string]bool
	setupChats   map[string]context.CancelFunc
	liveOutput   map[string]*agentOutputPreview
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
	provider := compute.Provider(&compute.Local{})
	agentProvider := provider
	var confinement compute.Confinement
	var egressRelay *compute.EgressRelay
	egressEndpoint := ""
	agentTool := os.Getenv("MITOS_AGENT_CONFINEMENT")
	if tool := agentTool; tool != "" && tool != compute.ConfinementNucleus {
		var confineErr error
		confinement, confineErr = compute.NewConfinement(tool, os.Getenv("MITOS_AGENT_NETWORK"))
		if confineErr != nil {
			cancel()
			return nil, confineErr
		}
		// Withheld networks stay withheld. An unrestricted posture is mediated:
		// the worker sandbox gets its own network namespace whose only egress
		// is this allowlisted relay, so host listeners – including the
		// loopback operator API – stay unreachable. Failing to start the relay
		// fails closed instead of sharing the host network.
		if confinement.NetMode == compute.NetUnrestricted {
			egressEndpoint = filepath.Join(s.Dir(), "egress", "model.sock")
			egressRelay, confineErr = compute.StartEgressRelay(egressEndpoint, compute.DefaultModelHosts)
			if confineErr != nil {
				cancel()
				return nil, confineErr
			}
			confinement.EgressSocket = egressEndpoint
			if helper, helperErr := os.Executable(); helperErr == nil {
				confinement.HelperExecutable = helper
			}
			agentLocal := &compute.Local{Confinement: confinement}
			agentLocal.SetEgressHelper(confinement.HelperExecutable)
			agentProvider = agentLocal
		} else {
			agentProvider = &compute.Local{Confinement: confinement}
		}
	}
	// Nucleus enforces confined application postures (ADR 0008). Configured
	// but unusable fails startup: a declared posture never silently loses its
	// enforcing provider.
	var nucleus compute.Provider
	if binary := os.Getenv("MITOS_NUCLEUS"); binary != "" {
		resolved, lookErr := exec.LookPath(binary)
		if lookErr != nil {
			cancel()
			return nil, fmt.Errorf("MITOS_NUCLEUS: %w", lookErr)
		}
		if resolved, lookErr = filepath.Abs(resolved); lookErr != nil {
			cancel()
			return nil, lookErr
		}
		confined, nucleusErr := compute.NewNucleus(ctx, compute.NucleusConfig{Binary: resolved, CgroupRoot: os.Getenv("NUCLEUS_CGROUP_ROOT"), Rootfs: os.Getenv("MITOS_NUCLEUS_ROOTFS"), ProbeDir: filepath.Join(s.Dir(), "nucleus", "probe")})
		if nucleusErr != nil {
			cancel()
			return nil, nucleusErr
		}
		nucleus = confined
	}
	// Agents under Nucleus have one way out, their per-attempt model broker,
	// so there is no network mode to choose.
	agentNucleus := agentTool == compute.ConfinementNucleus
	if agentNucleus {
		if nucleus == nil {
			cancel()
			return nil, errors.New("MITOS_AGENT_CONFINEMENT=nucleus requires MITOS_NUCLEUS")
		}
		if os.Getenv("MITOS_AGENT_NETWORK") != "" {
			cancel()
			return nil, errors.New("MITOS_AGENT_NETWORK does not apply under nucleus: agents reach only their model API, through the per-attempt broker")
		}
		agentProvider = nucleus
	}
	e := &Engine{provider: provider, agentProvider: agentProvider, nucleus: nucleus, agentNucleus: agentNucleus, confinement: confinement, egressRelay: egressRelay, egressEndpoint: egressEndpoint, Store: s, Apps: apps, Root: abs, cancel: map[string]context.CancelFunc{}, ctx: ctx, stop: cancel}
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
	e.Desk.LaunchDefaults = s.AgentLaunchDefaults
	initialized := false
	defer func() {
		if !initialized {
			e.Desk.Close()
		}
	}()
	if err = s.recoverModelEvaluations(); err != nil {
		cancel()
		return nil, err
	}
	if err = s.recoverGraphDecisions(); err != nil {
		cancel()
		return nil, err
	}
	if err = s.recoverKernelJudgments(); err != nil {
		cancel()
		return nil, err
	}
	if err = s.recoverDecisionRequests(); err != nil {
		cancel()
		return nil, err
	}
	for _, r := range s.List() {
		// Logical occurrences recover their durable child graph below.
		if r.Brief.AppID == WorkflowAppID {
			continue
		}
		if r.ExecutionKind == "interactive" {
			continue
		}
		if r.Brief.Structured != nil && !r.Sample && (r.Status == "running" || r.RestartRetry) {
			if err = e.recoverStructured(r); err != nil {
				cancel()
				return nil, err
			}
			continue
		}
		if r.Status == "stopping" && !r.Sample {
			if err = s.Transition(r.ID, "run_cancelled", "cancelled", "Stopped", "The server restarted after a stop request. Any already harvested evidence is preserved; no command was restarted."); err != nil {
				cancel()
				return nil, err
			}
		} else if !r.Sample && (r.Status == "running" || (r.Status == "queued" && r.Claim != nil && r.Claim.ReleasedAt == nil)) {
			// A restart between submission and completion settles the already
			// submitted result — after persisting its leftover workspace
			// changes — instead of parking a retry that would redo finished
			// work (issue #127).
			if e.recoverSubmittedAttempt(r) {
				continue
			}
			if err = s.interruptExecution(r); err != nil {
				cancel()
				return nil, err
			}
		}
	}
	// Pre-upgrade structured runs recorded no frozen definition body; resolve
	// and persist the exact admitted revision before anything can claim them.
	if err = e.hydrateStructuredDefinitions(); err != nil {
		cancel()
		return nil, err
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
	if err = e.reconcileServiceGraphs(); err != nil {
		cancel()
		return nil, err
	}
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
	e.wg.Add(2)
	go e.dispatch()
	go e.watchLoop()
	return e, nil
}
func (e *Engine) Close() {
	e.setupMu.Lock()
	e.stop()
	e.setupMu.Unlock()
	if e.egressRelay != nil {
		e.egressRelay.Close()
	}
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
	return e.agentApplication(id)
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
	// Compare against resolved roots: on platforms with symlinked temp or
	// home prefixes (for example /var on macOS), the workspace is resolved
	// while the root itself may still carry the unresolved spelling.
	root := e.Root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	allowed := Within(root, path)
	if !allowed && e.Desk != nil {
		for _, project := range e.Desk.Projects() {
			dir := project.Directory
			if resolved, err := filepath.EvalSymlinks(dir); err == nil {
				dir = resolved
			}
			if Within(dir, path) {
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
	if parent != "" {
		prior, err := e.Store.Get(parent)
		if err != nil {
			return Run{}, err
		}
		if prior.GraphAttempt != nil {
			return Run{}, errGraphRetry
		}
	}
	if b.Structured != nil {
		if parent != "" {
			return e.retryStructured(b, parent)
		}
		return e.LaunchStructured(b)
	}
	b, name, err := e.prepareBrief(b)
	if err != nil {
		return Run{}, err
	}
	return e.Store.Create(b, name, parent, false)
}
func (e *Engine) prepareBrief(b Brief) (Brief, string, error) {
	if b.ProjectLimits {
		if b.AppID != WorkflowAppID || b.ProjectID == "" {
			return Brief{}, "", errors.New("project execution limits require an agent workflow and project")
		}
		setup, err := e.freezeSetup(b.ProjectID)
		if err != nil {
			return Brief{}, "", err
		}
		b.TimeoutMinutes = setup.Minutes
	}
	if b.AppID == WorkflowAppID && (b.ProjectID == "" || len(strings.TrimSpace(b.Intent)) < 3 || len(b.Ownership.Paths) > 0 || len(b.Ownership.Leases) > 0 || len(b.Scheduling.Deps) > 0) {
		return Brief{}, "", errors.New("agent workflows require a project and instructions; worker agents own execution claims and dependencies")
	}
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
	if err := validateDecisionModel(app); err != nil {
		return Brief{}, "", err
	}
	if !app.Builtin {
		if _, err := e.compileApplication(app); err != nil {
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
	if r.GraphAttempt != nil {
		return Run{}, errGraphRetry
	}
	if r.ExecutionKind == "interactive" {
		return Run{}, errors.New("replace the executor through its durable task")
	}
	if r.WorkflowReview != nil || r.Decision != nil && r.Decision.Source != nil {
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
	if r.Brief.AppID == WorkflowAppID {
		// Reconciliation awaits child settlement before closing the occurrence.
		return nil
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
			if err := e.reconcileWorkerLeases(); err != nil {
				continue
			}
			if err := e.reconcileDormantInputs(); err != nil {
				continue
			}
			if err := e.Store.expireAgentSessions(); err != nil {
				continue
			}
			if err := e.reconcileSupervision(); err != nil {
				continue
			}
			cycleTime := time.Now().UTC()
			if err := e.reconcileServiceGraphs(); err != nil {
				continue
			}
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
			if err := e.reconcileGraphs(); err != nil {
				continue
			}
			e.reconcileDecisions()
			e.Store.reconcileHeldJudgments()
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
				if r.Brief.Structured != nil {
					if os.Getenv("MITOS_LOCAL_STRUCTURED_WORKERS") == "off" {
						continue
					}
					if _, ok := e.agentApplication(r.Brief.AppID); !ok {
						continue
					}
				}

				e.mu.Lock()
				if len(e.cancel) >= e.Store.RuntimeCapacity() {
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
				var acquired bool
				var claimErr error
				if r.Brief.Structured != nil {
					acquired, claimErr = e.localWorkerClaim(r)
				} else {
					acquired, claimErr = e.Store.ScheduleSelected(r.ID, active, candidate.Policy)
				}
				if claimErr != nil || !acquired {
					e.mu.Unlock()
					continue
				}
				if r.Brief.AppID == WorkflowAppID {
					e.mu.Unlock()
					// The occurrence tracks the graph without consuming a worker slot.
					// Graph agents use ordinary pool admission and process limits.
					_ = e.startServiceGraph(r.ID)
					continue
				}
				deadline := e.executionDeadline(current)
				if current.GraphAttempt != nil && current.GraphAttempt.Executor.Application.Agent != nil {
					// A native agent attempt has no per-attempt hard timebox
					// (#128): its soft budget raises a condition and a kernel
					// judgment decides. The graph's elapsed limit is the hard
					// ceiling, and it never refills.
					if g := e.Store.graphForAttempt(current.GraphAttempt.GraphID); g != nil && g.Limits.Deadline.Before(deadline) {
						deadline = g.Limits.Deadline
					}
				}
				ctx, cancel := context.WithDeadline(e.ctx, deadline)
				e.cancel[r.ID] = cancel
				e.mu.Unlock()
				e.wg.Add(1)
				go func() {
					defer e.wg.Done()
					defer cancel()
					defer func() {
						e.mu.Lock()
						delete(e.cancel, r.ID)
						e.releaseResultGrace(r.ID)
						e.mu.Unlock()
					}()
					e.execute(ctx, r)
				}()
			}
		}
	}
}

// executionDeadline derives the hard deadline for a dispatched run. A
// native agent attempt has no per-attempt hard timebox (#128): its soft
// budget raises a condition and a kernel judgment decides, so the deadline
// starts from the graph elapsed limit — the only hard ceiling, never
// refilled — instead of the soft budget, which would still cancel a
// progressing attempt at its original size budget. Structured and scheduling
// deadlines still cap any run.
func (e *Engine) executionDeadline(current Run) time.Time {
	var deadline time.Time
	if current.GraphAttempt != nil && current.GraphAttempt.Executor.Application.Agent != nil {
		deadline = time.Now().Add(24 * time.Hour)
		if g := e.Store.graphForAttempt(current.GraphAttempt.GraphID); g != nil && g.Limits.Deadline.After(time.Now()) {
			deadline = g.Limits.Deadline
		}
	} else {
		deadline = time.Now().Add(time.Duration(current.Brief.TimeoutMinutes) * time.Minute)
		if current.Execution != nil {
			deadline = current.Execution.Deadline
		}
	}
	e.Store.mu.RLock()
	taskDeadline := e.Store.structuredTaskDeadline(&current)
	e.Store.mu.RUnlock()
	if !taskDeadline.IsZero() && taskDeadline.Before(deadline) {
		deadline = taskDeadline
	}
	if taskDeadline := current.Brief.Scheduling.Deadline; taskDeadline != nil && taskDeadline.Before(deadline) {
		deadline = *taskDeadline
	}
	return deadline
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
		var prerequisite *agentPrerequisiteError
		if errors.As(err, &prerequisite) && e.Store.blockAgentPrerequisite(r.ID, prerequisite.Error()) == nil {
			return
		}
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
		if r.Brief.Structured != nil {
			_ = e.Store.interruptStructured(r.ID, "Server shutdown interrupted structured work; a bounded replacement may resume the same Task after restart.")
			return
		}
		_ = e.Store.interruptExecution(current)
		return
	}
	if errors.Is(err, errBlockedOnHuman) || errors.Is(err, errObservationLost) {
		_ = e.Store.Park(r.ID, checkpointOption(current, observationDecision(err)))
		return
	}
	message := err.Error()
	var prerequisite *agentPrerequisiteError
	if errors.As(err, &prerequisite) {
		_ = e.Store.blockAgentPrerequisite(r.ID, prerequisite.Error())
		return
	}
	if end, expired := e.budgetEnd(ctx, r.ID); expired {
		// The attempt's time ran out. When it had already submitted its result
		// and the failure is that ending itself — not a wrap-up failure such as
		// harvesting or saving artifacts, which must stay visible — the result
		// is collected instead of failing finished work (issue #127).
		if budgetEndedSession(err) && e.collectSubmittedResult(r.ID, "deadline_after_submit", end) {
			return
		}
		message = "The time limit was reached. Review the output and try again with a longer limit."
		if current.AttemptBudget != nil && current.AttemptBudget.WrapUpAt != nil {
			message = "The budget judgment asked the attempt to wrap up and its grace period ended without a submission. Evidence is preserved."
		}
	}
	// Budget exhaustion is recorded structurally so calibration never scrapes
	// adapter-specific summary text.
	var steps *harness.TurnLimitError
	if errors.As(err, &steps) {
		message = steps.Error()
		_ = e.Store.RecordEvidence(r.ID, "execution_budget_exhausted", "The agent exhausted its model-step allowance.", map[string]int{"limit": steps.Limit})
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
