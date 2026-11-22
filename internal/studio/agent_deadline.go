package studio

import (
	"context"
	"errors"
	"path/filepath"
	"time"

	workspacefs "mitos/internal/workspace"
)

// recoverHarvestBudget bounds the workspace recovery of an interrupted
// attempt at restart.
const recoverHarvestBudget = 30 * time.Second

// resultWrapupGrace bounds an attempt's wrap-up once its result is submitted
// (issue #127): the remaining budget is only for closing the session, so a
// submitted attempt never holds a worker slot until its deadline. A variable
// so tests can shorten it.
var resultWrapupGrace = 60 * time.Second

// resultGrace tracks one attempt's wrap-up window. Entries live while the
// engine executes the attempt; expired is guarded by Engine.mu.
type resultGrace struct {
	submittedAt time.Time
	deadline    time.Time
	expired     bool
}

// noteResultSubmitted arms the wrap-up window that follows a successful result
// submission. The attempt keeps its worker slot only for the grace period;
// afterwards its execution context is ended and the submitted result is
// collected instead of failing the attempt.
func (e *Engine) noteResultSubmitted(run string) {
	e.mu.Lock()
	if e.submitGrace == nil {
		e.submitGrace = map[string]*resultGrace{}
	}
	if _, ok := e.submitGrace[run]; ok {
		e.mu.Unlock()
		return
	}
	cancel, running := e.cancel[run]
	now := time.Now()
	e.submitGrace[run] = &resultGrace{submittedAt: now, deadline: now.Add(resultWrapupGrace)}
	e.mu.Unlock()
	if !running {
		return
	}
	time.AfterFunc(resultWrapupGrace, func() {
		e.mu.Lock()
		if g := e.submitGrace[run]; g != nil {
			g.expired = true
		}
		e.mu.Unlock()
		cancel()
	})
}

// releaseResultGrace forgets the wrap-up window once the attempt's execution
// has finished. A timer that already fired only cancels a finished context.
func (e *Engine) releaseResultGrace(run string) {
	delete(e.submitGrace, run)
}

// budgetEnd reports whether an attempt's execution context was ended by its
// time budget — the deadline itself, or the wrap-up grace that follows a
// result submission — and the moment that ending occurred, for calibration.
// Operator stops and server shutdowns are settled before this is consulted.
func (e *Engine) budgetEnd(ctx context.Context, run string) (time.Time, bool) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		end, _ := ctx.Deadline()
		return end, true
	}
	if ctx.Err() == nil {
		return time.Time{}, false
	}
	e.mu.Lock()
	g := e.submitGrace[run]
	end, expired := time.Time{}, g != nil && g.expired
	if expired {
		// The grace ended execution before the original deadline; measure
		// from the moment it actually stopped, not the unused budget.
		end = g.deadline
	}
	e.mu.Unlock()
	return end, expired
}

// budgetEndedSession reports whether perform failed with the budget ending
// itself rather than a wrap-up failure: stopping, harvesting or saving
// artifacts must stay visible instead of being settled as finished work.
func budgetEndedSession(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// collectSubmittedResult settles an attempt whose time ran out after it had
// already submitted a result for the task's current revision (issue #127): the
// deadline only cut the wrap-up short, so the submitted result is collected
// and the attempt finishes the same way as a normal exit after submission.
// evidence names the recorded marker; end is the moment the budget ended (zero
// when unknown, as after a restart). It reports whether the run was settled.
func (e *Engine) collectSubmittedResult(run, evidence string, end time.Time) bool {
	receipt, ok := e.Store.SubmittedResult(run)
	if !ok {
		return false
	}
	if end.IsZero() || end.Before(receipt.At) {
		end = time.Now()
	}
	if err := e.Store.RecordEvidence(run, evidence, "The time budget ended after this result was submitted; the submitted result was collected.", map[string]int64{"submission_seq": receipt.Seq, "after_submit_seconds": max(0, int64(end.Sub(receipt.At).Round(time.Second).Seconds()))}); err != nil {
		return false
	}
	summary := receipt.Submission.Summary
	if len(summary) > 2000 {
		summary = summary[:2000]
	}
	return e.Store.Complete(run, summary, false) == nil
}

// recoverSubmittedAttempt settles an attempt the server interrupted after it
// submitted its result (issue #127): its leftover workspace changes are
// harvested and persisted first — downstream attempts continue from
// changes.patch — and the submitted result is then collected like a normal
// exit after submission. When the workspace cannot be recovered the attempt
// keeps its interruption state for the operator instead of completing without
// the patch. It reports whether the run was settled.
func (e *Engine) recoverSubmittedAttempt(r Run) bool {
	if _, ok := e.Store.SubmittedResult(r.ID); !ok {
		return false
	}
	if !e.harvestRecoveredAttempt(r) {
		return false
	}
	return e.collectSubmittedResult(r.ID, "restart_after_submit", time.Time{})
}

// harvestRecoveredAttempt persists the workspace changes of an attempt whose
// process died after submitting but before its wrap-up could harvest. The
// harvest is available when it was already saved, or when the attempt's
// prepared workspace can still be read from disk; segmented executions keep
// their trees under per-segment bases and are not reconstructed here.
func (e *Engine) harvestRecoveredAttempt(r Run) bool {
	for _, a := range r.Artifacts {
		if a.Name == "changes.patch" {
			return true // the wrap-up persisted its harvest before the stop
		}
	}
	if r.GraphAttempt == nil || r.Workspace == nil || r.Workspace.BaselineCommit == "" {
		return false // no prepared agent workspace to recover from
	}
	ctx, cancel := context.WithTimeout(context.Background(), recoverHarvestBudget)
	defer cancel()
	base := filepath.Join(e.Store.Dir(), "workspaces", r.ID)
	history := &workspacefs.History{Limits: r.Workspace.Limits, Base: base, Directory: filepath.Join(base, "history.git"), BaselineCommit: r.Workspace.BaselineCommit}
	before, err := history.Load(ctx, history.BaselineCommit, r.Workspace.Fingerprint)
	if err != nil {
		return false
	}
	before.OmittedSubmodules = r.Workspace.OmittedSubmodules
	harvested, err := history.Harvest(ctx, before)
	if err != nil {
		return false
	}
	manifest, err := harvested.Manifest()
	if err != nil {
		return false
	}
	archive, err := harvested.Snapshot.Archive()
	if err != nil {
		return false
	}
	if err := e.Store.SaveArtifacts(r.ID, ArtifactData{Name: "changes.patch", Data: harvested.Patch}, ArtifactData{Name: "changes.json", Data: manifest}, ArtifactData{Name: "workspace.tar.gz", Data: archive}); err != nil {
		return false
	}
	return e.Store.RecordWorkspace(r.ID, "workspace_harvested", "Recovered the agent's workspace changes after a restart.", WorkspaceRecord{OmittedSubmodules: r.Workspace.OmittedSubmodules, Limits: r.Workspace.Limits, Mode: "copy", BaselineCommit: r.Workspace.BaselineCommit, ResultCommit: harvested.ResultCommit, Fingerprint: harvested.Fingerprint, ChangedFiles: len(harvested.Changes)}) == nil
}
