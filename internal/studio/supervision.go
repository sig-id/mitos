package studio

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"mitos/internal/autonomy"
	"mitos/internal/observation"
	workspacefs "mitos/internal/workspace"
)

func (e *Engine) waitBoundary(ctx context.Context, id, workspace string) error {
	activity, err := observation.NewLocalActivity(workspace)
	if err != nil {
		return err
	}
	defer activity.Close()
	clock, err := observation.NewClock(time.Now(), observation.Thresholds{Snap: 200 * time.Millisecond, Steer: 750 * time.Millisecond, Block: 45 * time.Second})
	if err != nil {
		return err
	}
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
		sample, err := activity.Sample(0)
		if err != nil {
			return err
		}
		edges, err := clock.Observe(time.Now(), sample)
		if err != nil {
			return err
		}
		for _, edge := range edges {
			if edge.Threshold == "snap" {
				return e.Store.RecordEvidence(id, "quiescent", "The stopped workspace reached a checkpoint boundary.", edge)
			}
		}
	}
}

func segmentPrefix(number int) string {
	if number == 1 {
		return ""
	}
	return fmt.Sprintf("segment-%03d-", number)
}
func segmentBase(root string, number int) string {
	if number == 1 {
		return root
	}
	return filepath.Join(root, "segments", fmt.Sprintf("segment-%03d", number))
}

func (e *Engine) resumeConfigured(ctx context.Context, r Run) error {
	plan := r.Execution
	base := filepath.Join(e.Store.Dir(), "workspaces", r.ID)
	history := &workspacefs.History{Base: base, Directory: filepath.Join(base, "history.git"), BaselineCommit: plan.InitialCommit}
	before, err := history.Load(ctx, plan.InitialCommit, plan.InitialFingerprint)
	if err != nil {
		return err
	}
	oracleDir := filepath.Join(base, "oracle")
	oracle, err := workspacefs.CaptureResult(ctx, oracleDir, workspacefs.DefaultLimits)
	if err != nil || oracle.Fingerprint != plan.OracleFingerprint {
		return errOracleTampered
	}
	app := plan.Application
	app.Directory = plan.Directory
	return e.runSegments(ctx, r, app, history, before, oracle, oracleDir)
}

func (e *Engine) restorePending(ctx context.Context, r Run, history *workspacefs.History) error {
	if r.PendingRewind == nil {
		return nil
	}
	pending := r.PendingRewind
	var target *autonomy.Checkpoint
	for i := range r.Checkpoints {
		if r.Checkpoints[i].ID == pending.CheckpointID {
			target = &r.Checkpoints[i]
			break
		}
	}
	if target == nil {
		return errors.New("recorded rewind checkpoint is missing")
	}
	root := filepath.Join(e.Store.Dir(), "workspaces", r.ID)
	if err := history.Restore(ctx, target.WorkspaceCommit, target.Fingerprint, segmentBase(root, pending.NextSegment)); err != nil {
		return err
	}
	return e.Store.CompleteRewind(r.ID, pending.ID)
}

func (e *Engine) runSegments(ctx context.Context, r Run, app Application, history *workspacefs.History, before, oracle workspacefs.Snapshot, oracleDir string) error {
	root := filepath.Join(e.Store.Dir(), "workspaces", r.ID)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := e.Store.Get(r.ID)
		if err != nil {
			return err
		}
		if current.Status == "stopping" {
			return context.Canceled
		}
		if current.PendingRewind != nil {
			if err = e.restorePending(ctx, current, history); err != nil {
				return err
			}
			current, err = e.Store.Get(r.ID)
			if err != nil {
				return err
			}
		}
		number := len(current.Segments) + 1
		memo := ""
		inputCommit := history.BaselineCommit
		if current.ReadyRewind != nil {
			if current.ReadyRewind.NextSegment != number {
				return errors.New("restored segment sequence changed")
			}
			memo = current.ReadyRewind.Memo
			for _, checkpoint := range current.Checkpoints {
				if checkpoint.ID == current.ReadyRewind.CheckpointID {
					inputCommit = checkpoint.WorkspaceCommit
					if err = history.Restore(ctx, checkpoint.WorkspaceCommit, checkpoint.Fingerprint, segmentBase(root, number)); err != nil {
						return err
					}
				}
			}
		}
		if proceed, err := e.modelBoundary(ctx, r.ID, "before_segment", number, inputCommit, "", "continue"); err != nil || !proceed {
			return err
		}
		segment, err := e.Store.StartSegment(r.ID, inputCommit)
		if err != nil {
			return err
		}
		attemptHistory := *history
		attemptHistory.Base = segmentBase(root, number)
		prefix := segmentPrefix(number)
		attemptErr := e.attempt(ctx, r, app, &attemptHistory, before, oracle, oracleDir, prefix, memo, segment)
		signal := failureSignal(ctx, attemptErr)
		if err = e.Store.FinishSegment(r.ID, signal); err != nil {
			return err
		}
		current, err = e.Store.Get(r.ID)
		if err != nil {
			return err
		}
		if current.Workspace != nil && current.Workspace.ResultCommit != "" {
			checkpointCtx, release := context.WithTimeout(context.Background(), time.Minute)
			pinErr := history.Pin(checkpointCtx, current.Workspace.ResultCommit)
			release()
			if pinErr != nil {
				return pinErr
			}
			if _, err = e.Store.TakeCheckpoint(r.ID, prefix+"output.txt"); err != nil {
				return err
			}
		}
		if attemptErr == nil {
			current, err = e.Store.Get(r.ID)
			if err != nil {
				return err
			}
			if len(current.Checkpoints) == 0 || !current.Checkpoints[len(current.Checkpoints)-1].Green {
				return errors.New("segment has no green checkpoint")
			}
			return e.Store.Complete(r.ID, "Acceptance passed on the checkpointed result. Download the patch or resulting workspace.", true)
		}
		if ctx.Err() != nil || app.Supervision == nil || signal == "service_health_unhealthy" || signal == "oracle_tampered" || signal == "ownership_violation" || signal == "dependency_inputs_changed" || signal == "dependency_input_unavailable" || signal == "blocked_on_human" || signal == "observation_contract_failed" {
			return attemptErr
		}
		current, err = e.Store.Get(r.ID)
		if err != nil {
			return err
		}
		if autonomy.PredicateStall(current.Checkpoints, "main") {
			signal = "predicate_stall"
			if err = e.Store.RecordEvidence(r.ID, "predicate_stall", "The acceptance predicate shows no progress across three consecutive segments.", map[string]int{"segments": 3}); err != nil {
				return err
			}
			for _, rewind := range current.Rewinds {
				if rewind.ReasonSignal == "predicate_stall" {
					return e.parkSupervision(r.ID, "predicate_stall_repeated")
				}
			}
		}
		_, _, exhausted := autonomy.Intensity(*app.Supervision, current.Rewinds, "main", time.Now())
		if exhausted {
			return e.parkSupervision(r.ID, "restart_intensity_exhausted")
		}
		if app.Supervision.Strategy == "escalate" || signal == "execution_failed" {
			return e.parkSupervision(r.ID, signal)
		}
		target := ""
		for i := len(current.Checkpoints) - 1; i >= 0; i-- {
			c := current.Checkpoints[i]
			if c.Green && !c.Invalidated {
				target = c.ID
				break
			}
		}
		if target == "" {
			return e.parkSupervision(r.ID, "no_green_checkpoint")
		}
		if proceed, err := e.modelBoundary(ctx, r.ID, "before_recovery", number, target, signal, "rewind"); err != nil || !proceed {
			return err
		}
		scope := "step"
		if app.Supervision.Strategy == "rewind_tail" {
			scope = "tail"
		}
		memo = fmt.Sprintf("Segment %d ended with %s. Its output is %soutput.txt and its change manifest is %schanges.json. The acceptance evidence and original brief remain on the run record. Repair the cause before repeating the work.", number, signal, prefix, prefix)
		if _, err = e.Store.BeginRewind(r.ID, autonomy.Rewind{FromSegment: len(current.Segments), CheckpointID: target, Scope: scope, Memo: memo, ReasonSignal: signal}); err != nil {
			return err
		}
	}
}

func failureSignal(ctx context.Context, err error) string {
	if err == nil {
		return ""
	}
	if ctx.Err() != nil {
		return "execution_interrupted"
	}
	switch {
	case errors.Is(err, errOwnershipViolation):
		return "ownership_violation"
	case errors.Is(err, errDependencyChanged):
		return "dependency_inputs_changed"
	case errors.Is(err, errDependencyUnavailable):
		return "dependency_input_unavailable"
	case errors.Is(err, errOracleTampered):
		return "oracle_tampered"
	case errors.Is(err, errBlockedOnHuman):
		return "blocked_on_human"
	case errors.Is(err, errObservationLost):
		return "observation_contract_failed"
	case errors.Is(err, errHealthRestart), errors.Is(err, errHealthKill):
		return "service_health_unhealthy"
	case errors.Is(err, errCommandFailed):
		return "command_failed"
	case errors.Is(err, errPredicateFailed):
		return "predicate_failed"
	default:
		return "execution_failed"
	}
}
func (e *Engine) parkSupervision(id, signal string) error {
	if err := e.Store.ReleaseClaims(id, "execution_stopped_and_harvest_finished"); err != nil {
		return err
	}
	if err := e.Store.RecordDecision(id, "escalate", "Bounded recovery cannot continue without an operator decision: "+signal); err != nil {
		return err
	}
	d := recoveryDecision()
	d.Question = "Start a new attempt or keep this work stopped?"
	d.ReasonSignal = signal
	d.InterruptClass = "judgment"
	d.Context.BlockedBy = "Automatic recovery stopped: " + signal + ". Segment outputs, changes, predicate evidence, and checkpoints are preserved. A new attempt gets a fresh execution budget."
	r, err := e.Store.Get(id)
	if err != nil {
		return err
	}
	return e.Store.Park(id, checkpointOption(r, d))
}

func checkpointOption(r Run, d *HumanDecision) *HumanDecision {
	if r.Execution == nil || !time.Now().Before(r.Execution.Deadline) {
		return d
	}
	if r.PendingRewind != nil || r.ReadyRewind != nil {
		d.Options = append(d.Options, Option{ID: "rewind", Label: "Resume the saved recovery", Consequence: "Continue the previously recorded restoration with its original memo, checkpoint, and deadline after inspecting any held claims."})
		return d
	}
	policy := autonomy.Supervision{Strategy: "rewind_to_green"}
	if r.Execution.Application.Supervision != nil {
		policy = *r.Execution.Application.Supervision
	}
	if _, _, exhausted := autonomy.Intensity(policy, r.Rewinds, "main", time.Now()); exhausted {
		return d
	}
	if len(r.Segments) > 0 && r.Segments[len(r.Segments)-1].FinishedAt == nil {
		return d
	}
	for _, checkpoint := range r.Checkpoints {
		if checkpoint.Green && !checkpoint.Invalidated {
			d.Options = append(d.Options, Option{ID: "rewind", Label: "Restore a passing checkpoint", Consequence: "Repeat this segment from the last passing checkpoint, retaining its failure evidence and original time limit."})
			break
		}
	}
	return d
}
