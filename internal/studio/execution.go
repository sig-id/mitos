package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mitos/internal/autonomy"
	"path/filepath"
	"time"

	"mitos/internal/acceptance"
	workspacefs "mitos/internal/workspace"
)

type WorkspaceRecord struct {
	Mode           string `json:"mode"`
	BaselineCommit string `json:"baseline_commit"`
	ResultCommit   string `json:"result_commit,omitempty"`
	Fingerprint    string `json:"fingerprint"`
	ChangedFiles   int    `json:"changed_files"`
}

var errOracleTampered = errors.New("oracle_tampered")
var errCommandFailed = errors.New("command_failed")
var errPredicateFailed = errors.New("predicate_failed")

func (e *Engine) perform(ctx context.Context, r Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := e.checkServiceCondition(ctx, r); err != nil {
		return err
	}
	if r.Execution != nil {
		return e.resumeConfigured(ctx, r)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	app, ok := e.App(r.Brief.AppID)
	if !ok {
		return errors.New("application is no longer installed")
	}
	if err := e.Store.RecordDecision(r.ID, "advance", "Prepare a fresh workspace for the brief."); err != nil {
		return err
	}
	if err := e.Store.Transition(r.ID, "run_started", "running", "Preparing workspace", "Selecting tracked and untracked non-ignored files. The source stays untouched."); err != nil {
		return err
	}
	launch, err := compileApplication(app)
	if err != nil && !app.Builtin {
		return err
	}
	source, err := e.Workspace(r.Brief.Workspace)
	if err != nil {
		return err
	}
	base := filepath.Join(e.Store.Dir(), "workspaces", r.ID)
	before, err := workspacefs.CaptureSource(ctx, source, e.Store.Dir(), workspacefs.DefaultLimits)
	if err != nil {
		return err
	}
	workspace := filepath.Join(base, "workspace")
	if app.Builtin {
		if err = before.WriteTo(workspace); err != nil {
			return err
		}
		return e.inventory(ctx, r, workspace)
	}
	history, err := workspacefs.Prepare(ctx, base, before)
	if err != nil {
		return err
	}
	baseline, err := json.MarshalIndent(before, "", "  ")
	if err != nil {
		return err
	}
	if err = e.Store.SaveArtifact(r.ID, "baseline.json", baseline); err != nil {
		return err
	}
	record := WorkspaceRecord{Mode: "copy", BaselineCommit: history.BaselineCommit, Fingerprint: before.Fingerprint}
	if err = e.Store.RecordWorkspace(r.ID, "workspace_prepared", "A separate workspace and Git baseline are ready.", record); err != nil {
		return err
	}
	// Freeze the installed verifier's files before starting the worker. Same-user
	// process isolation is still not an inaccessible oracle boundary.
	oracle, err := workspacefs.CaptureSource(ctx, app.Directory, e.Store.Dir(), workspacefs.DefaultLimits)
	if err != nil {
		return err
	}
	oracleDir := filepath.Join(base, "oracle")
	if err = oracle.WriteTo(oracleDir); err != nil {
		return err
	}
	if err = e.Store.RecordEvidence(r.ID, "oracle_prepared", "Captured the installed acceptance files before execution.", map[string]string{"fingerprint": oracle.Fingerprint, "isolation": "same_user_process"}); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	deadline, _ := ctx.Deadline()
	launch.RunID = r.ID
	launch.CapabilityRef = app.ID
	launch.Base = base
	launch.Workspace.Source = source
	launch.Workspace.Directory = workspace
	launch.Workspace.HistoryDirectory = history.Directory
	launch.Workspace.BaselineCommit = history.BaselineCommit
	launch.Workspace.BaselineFingerprint = before.Fingerprint
	launch.Posture.Deadline = deadline
	plan := ExecutionPlan{Launch: &launch, Application: app, Directory: app.Directory, InitialCommit: history.BaselineCommit, InitialFingerprint: before.Fingerprint, OracleFingerprint: oracle.Fingerprint, Deadline: deadline}
	if err = e.Store.ConfigureExecution(r.ID, plan); err != nil {
		return err
	}
	record.ResultCommit = history.BaselineCommit
	if err = e.Store.RecordWorkspace(r.ID, "initial_workspace_recorded", "The initial workspace commit is available for recovery.", record); err != nil {
		return err
	}
	if err = e.waitBoundary(ctx, r.ID, workspace); err != nil {
		return err
	}
	if app.Supervision != nil {
		initial := workspacefs.Harvest{BaselineCommit: history.BaselineCommit, ResultCommit: history.BaselineCommit, Fingerprint: before.Fingerprint, Snapshot: before}
		err = e.evaluate(ctx, r, app, history, before, initial, oracle, oracleDir, "initial-", before, nil)
		if errors.Is(err, errOracleTampered) || ctx.Err() != nil {
			return err
		}
		if err != nil {
			if recordErr := e.Store.RecordEvidence(r.ID, "initial_predicate_failed", "The initial workspace is not a verified green recovery point.", map[string]string{"reason": err.Error()}); recordErr != nil {
				return recordErr
			}
		}
	}
	if err = history.Pin(ctx, history.BaselineCommit); err != nil {
		return err
	}
	if _, err = e.Store.TakeCheckpoint(r.ID, "baseline.json"); err != nil {
		return err
	}
	return e.runSegments(ctx, r, app, history, before, oracle, oracleDir)
}

func (e *Engine) attempt(ctx context.Context, r Run, app Application, history *workspacefs.History, before workspacefs.Snapshot, oracle workspacefs.Snapshot, oracleDir, prefix, memo string, segment autonomy.Segment) error {
	var err error
	base := history.Base
	workspace := filepath.Join(base, "workspace")
	record := WorkspaceRecord{Mode: "copy", BaselineCommit: history.BaselineCommit, Fingerprint: before.Fingerprint}
	if err = e.Store.Transition(r.ID, "capability_started", "running", "Working on the brief", "The application is executing in its workspace copy."); err != nil {
		return err
	}
	task, taskErr := e.Store.Task(r.TaskID)
	if taskErr != nil {
		return taskErr
	}
	inputs, inputSnapshot, inputErr := e.prepareDependencyInputs(ctx, r, base, prefix)
	if inputErr != nil {
		return fmt.Errorf("%w: %v", errDependencyUnavailable, inputErr)
	}
	var monitor *commandObservation
	if app.Observation != nil {
		monitor, err = newCommandObservation(e.Store, r.ID, *app.Observation)
		if err != nil {
			return err
		}
	}
	launch, err := compileApplication(app)
	if err != nil {
		return err
	}
	currentPlan, getErr := e.Store.Get(r.ID)
	if getErr != nil {
		return getErr
	}
	if currentPlan.Execution != nil && currentPlan.Execution.Launch != nil {
		launch = *currentPlan.Execution.Launch
	}
	launch.RunID = r.ID
	launch.CapabilityRef = app.ID
	launch.Base = base
	launch.Workspace.Directory = workspace
	launch.Workspace.Source = ""
	launch.Workspace.HistoryDirectory = history.Directory
	launch.Workspace.BaselineCommit = history.BaselineCommit
	launch.Workspace.BaselineFingerprint = before.Fingerprint
	launch.Posture.Deadline, _ = ctx.Deadline()
	if err = e.Store.RecordEvidence(r.ID, "provider_launch_compiled", "The provider and its enforced posture are fixed for this segment.", launch); err != nil {
		return err
	}
	healthState, err := e.Store.startHealth(r.ID, segment.ID)
	if err != nil {
		return err
	}
	var healthMonitor *serviceHealthMonitor
	var healthInput map[string]any
	if healthState != nil {
		healthMonitor = &serviceHealthMonitor{store: e.Store, runID: r.ID, scope: healthState, provider: e.provider, launch: launch}
		healthInput = map[string]any{"monitor_id": healthState.MonitorID, "segment_id": segment.ID, "heartbeat_route": "/api/runs/" + r.ID + "/heartbeat"}
	}
	input, err := json.Marshal(map[string]any{"run_id": r.ID, "task_id": r.TaskID, "dependencies": task.DependencyEvidence, "service_lifecycle": task.ServiceLifecycle, "service_ingress": task.ServiceIngress, "service_event": task.ServiceEvent, "service_cycle": task.ServiceCycle, "service_dependencies": task.ServiceDependencyEvidence, "service_health": healthInput, "inputs": inputs, "brief": r.Brief, "workspace": workspace, "segment": segment, "failure_memo": memo})
	if err != nil {
		return err
	}
	session, output, runErr := executeProviderCommand(ctx, e.provider, launch, app.Command, workspace, input, monitor, healthMonitor)
	if err = e.Store.SaveArtifact(r.ID, prefix+"output.txt", output); err != nil {
		return err
	}
	// Harvest gets a separate bounded cleanup window after stop/timeout/shutdown.
	// Cancellation must not discard the very evidence needed to inspect failure.
	harvestCtx, release := context.WithTimeout(context.Background(), time.Minute)
	defer release()
	if err = e.waitBoundary(harvestCtx, r.ID, workspace); err != nil {
		return err
	}
	var harvested workspacefs.Harvest
	if session != nil {
		stream, streamErr := e.provider.Stream(harvestCtx, session, 0)
		if streamErr != nil {
			return streamErr
		}
		for event := range stream {
			if err = e.Store.RecordEvidence(r.ID, "provider_status", "Recorded provider lifecycle status.", event); err != nil {
				return err
			}
		}
		result, harvestErr := e.provider.Harvest(harvestCtx, session)
		err = harvestErr
		if err == nil {
			if result.Change == nil {
				err = errors.New("provider returned no workspace harvest")
			} else {
				harvested = *result.Change
			}
		}
	} else {
		harvested, err = history.Harvest(harvestCtx, before)
	}
	if err != nil {
		if recordErr := e.Store.RecordEvidence(r.ID, "harvest_failed", "The workspace change could not be harvested; the saved workspace and output remain available.", map[string]string{"reason": err.Error()}); recordErr != nil {
			return recordErr
		}
		return errors.Join(runErr, fmt.Errorf("workspace harvest failed: %w", err))
	}
	manifest, err := harvested.Manifest()
	if err != nil {
		return err
	}
	archive, err := harvested.Snapshot.Archive()
	if err != nil {
		return err
	}
	for _, artifact := range []struct {
		name string
		data []byte
	}{{"changes.patch", harvested.Patch}, {"changes.json", manifest}, {"workspace.tar.gz", archive}} {
		if err = e.Store.SaveArtifact(r.ID, prefix+artifact.name, artifact.data); err != nil {
			return err
		}
	}
	record.ResultCommit = harvested.ResultCommit
	record.Fingerprint = harvested.Fingerprint
	record.ChangedFiles = len(harvested.Changes)
	if err = e.Store.RecordWorkspace(r.ID, "workspace_harvested", fmt.Sprintf("Saved %d changed paths, a Git patch, and the resulting workspace.", len(harvested.Changes)), record); err != nil {
		return err
	}
	if len(r.Brief.Ownership.Paths) > 0 {
		outside := []string{}
		for _, change := range harvested.Changes {
			if !r.Brief.Ownership.Contains(change.Path) {
				outside = append(outside, change.Path)
			}
		}
		if len(outside) > 0 {
			if err = e.Store.RecordEvidence(r.ID, "ownership_violation", "Changes exceeded the declared owned paths.", map[string]any{"paths": outside}); err != nil {
				return err
			}
			return errOwnershipViolation
		}
	}
	touched := app.Acceptance.Touched(harvested.Changes)
	if err = verifyDependencyInputs(harvestCtx, base, inputSnapshot); err != nil {
		if recordErr := e.Store.RecordEvidence(r.ID, "dependency_inputs_changed", "The bound upstream input copies changed during execution.", map[string]string{"reason": "fingerprint_mismatch"}); recordErr != nil {
			return recordErr
		}
		return err
	}
	currentOracle, oracleErr := workspacefs.CaptureResult(harvestCtx, oracleDir, workspacefs.DefaultLimits)
	if oracleErr != nil || currentOracle.Fingerprint != oracle.Fingerprint {
		touched = append(touched, "installed acceptance files")
	}
	if len(touched) > 0 {
		if err = e.Store.RecordEvidence(r.ID, "oracle_tampered", "Execution changed declared oracle paths or the saved acceptance files. Acceptance will not run.", map[string]any{"paths": touched}); err != nil {
			return err
		}
		if err = e.Store.RecordDecision(r.ID, "escalate", "Ask the operator to review the changed oracle before any further attempt."); err != nil {
			return err
		}
		return errOracleTampered
	}
	if runErr != nil {
		return fmt.Errorf("%w: %w. Output and partial workspace changes are saved", errCommandFailed, runErr)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	measureBefore := before
	if len(app.Acceptance.Measure) > 0 && segment.InputCommit != history.BaselineCommit {
		current, getErr := e.Store.Get(r.ID)
		if getErr != nil {
			return getErr
		}
		fingerprint := ""
		for _, checkpoint := range current.Checkpoints {
			if checkpoint.WorkspaceCommit == segment.InputCommit {
				fingerprint = checkpoint.Fingerprint
				break
			}
		}
		if fingerprint == "" {
			return errors.New("segment input checkpoint is missing")
		}
		measureBefore, err = history.Load(ctx, segment.InputCommit, fingerprint)
		if err != nil {
			return err
		}
	}
	return e.evaluate(ctx, r, app, history, before, harvested, oracle, oracleDir, prefix, measureBefore, inputSnapshot)
}

func (e *Engine) evaluate(ctx context.Context, r Run, app Application, history *workspacefs.History, before workspacefs.Snapshot, harvested workspacefs.Harvest, oracle workspacefs.Snapshot, oracleDir, prefix string, measureBefore workspacefs.Snapshot, inputSnapshot *workspacefs.Snapshot) error {
	if err := e.Store.RecordDecision(r.ID, "verify", "Reconstruct the harvested change on a pristine baseline and execute the saved acceptance predicate."); err != nil {
		return err
	}
	if err := e.Store.Transition(r.ID, "verification_started", "running", "Checking the result", "Checking a reconstructed result outside the worker workspace."); err != nil {
		return err
	}
	verifyCtx, cancel := context.WithTimeout(ctx, app.Acceptance.Timeout())
	defer cancel()
	verifyBase := filepath.Join(history.Base, "verification", prefix+"result")
	if err := history.VerificationTree(verifyCtx, before, harvested, filepath.Join(verifyBase, "workspace")); err != nil {
		return fmt.Errorf("reconstruct acceptance tree: %w", err)
	}
	verdict := acceptance.Verdict{Verdict: "unknown", WorkspaceCommit: harvested.ResultCommit, EvidenceRef: "", OracleIsolation: "same_user_process"}
	runCheck := func(argv []string, base, name string) ([]byte, error) {
		// Rebase an executable supplied by the installed app onto its pristine copy.
		argv = append([]string(nil), argv...)
		if Within(app.Directory, argv[0]) {
			rel, _ := filepath.Rel(app.Directory, argv[0])
			argv[0] = filepath.Join(oracleDir, rel)
		}
		input, _ := json.Marshal(map[string]any{"run_id": r.ID, "brief": r.Brief, "workspace": filepath.Join(base, "workspace")})
		output, err := executeCommand(verifyCtx, argv, oracleDir, base, input)
		if saveErr := e.Store.SaveArtifact(r.ID, prefix+name, output); saveErr != nil {
			return nil, saveErr
		}
		return output, err
	}
	var checkErr error
	baselineMeasured := len(app.Acceptance.Measure) == 0
	if len(app.Acceptance.Measure) > 0 {
		baselineBase := filepath.Join(history.Base, "verification", prefix+"baseline")
		if err := measureBefore.WriteTo(filepath.Join(baselineBase, "workspace")); err != nil {
			return err
		}
		output, err := runCheck(app.Acceptance.Measure, baselineBase, "measure-baseline.txt")
		if err == nil {
			verdict.BaselineMeasure, err = acceptance.Measurement(output)
		}
		if err != nil {
			checkErr = fmt.Errorf("baseline measurement failed: %w", err)
			verdict.EvidenceRef = prefix + "measure-baseline.txt"
		} else {
			baselineMeasured = true
		}
	}
	if checkErr == nil {
		verdict.EvidenceRef = prefix + "acceptance.txt"
		_, checkErr = runCheck(app.Acceptance.Cmd, verifyBase, "acceptance.txt")
		if checkErr == nil {
			verdict.Verdict = "pass"
		} else {
			verdict.Verdict = "fail"
		}
	}
	if baselineMeasured && len(app.Acceptance.Measure) > 0 && verifyCtx.Err() == nil {
		output, err := runCheck(app.Acceptance.Measure, verifyBase, "measure-result.txt")
		if err == nil {
			verdict.Measure, err = acceptance.Measurement(output)
		}
		if err == nil {
			verdict.Delta, err = acceptance.Delta(verdict.BaselineMeasure, verdict.Measure)
		}
		if err != nil {
			checkErr = errors.Join(checkErr, fmt.Errorf("result measurement failed: %w", err))
			verdict.Verdict = "unknown"
		}
	}
	// Verifiers can create build output in their own result tree, but cannot
	// rewrite the saved oracle and still produce a trusted local verdict.
	currentOracle, err := workspacefs.CaptureResult(verifyCtx, oracleDir, workspacefs.DefaultLimits)
	if err == nil && currentOracle.Fingerprint != oracle.Fingerprint {
		if err = e.Store.RecordEvidence(r.ID, "oracle_tampered", "Acceptance modified its saved oracle files.", map[string]string{"before": oracle.Fingerprint, "after": currentOracle.Fingerprint}); err != nil {
			return err
		}
		if err = e.Store.RecordDecision(r.ID, "escalate", "Review acceptance files changed during verification."); err != nil {
			return err
		}
		return errOracleTampered
	}
	if err != nil {
		checkErr = errors.Join(checkErr, err)
		verdict.Verdict = "unknown"
	}
	if inputErr := verifyDependencyInputs(verifyCtx, history.Base, inputSnapshot); inputErr != nil {
		if errors.Is(inputErr, errDependencyChanged) {
			if recordErr := e.Store.RecordEvidence(r.ID, "dependency_inputs_changed", "Verification changed the bound upstream input copies.", map[string]string{"phase": "verification"}); recordErr != nil {
				return recordErr
			}
		}
		return inputErr
	}
	if err = e.Store.RecordAcceptance(r.ID, verdict); err != nil {
		return err
	}
	if checkErr != nil {
		return fmt.Errorf("%w: %w. Review the saved verification evidence", errPredicateFailed, checkErr)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = e.Store.Transition(r.ID, "verification_recorded", "running", "Acceptance passed", "The saved acceptance command passed on the reconstructed change."); err != nil {
		return err
	}
	return nil
}

func oracleDecision() *HumanDecision {
	d := recoveryDecision()
	d.Question = "Review this changed acceptance oracle?"
	d.ReasonSignal = "oracle_tampered"
	d.InterruptClass = "judgment"
	d.Context.BlockedBy = "Execution changed files declared as the acceptance oracle, or changed the saved acceptance files. The patch and workspace are preserved, but the result has not been accepted. Review these changes before restarting."
	return d
}
