package studio

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mitos/internal/compute"
	"mitos/internal/workpool"
	workspacefs "mitos/internal/workspace"
)

func submittedRun(t *testing.T, s *Store, app Application) (Run, string) {
	t.Helper()
	root := t.TempDir()
	r, err := s.Create(Brief{Title: "Wrap-up cut short", Workspace: root, AppID: app.ID, TimeoutMinutes: 1}, app.Name, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Transition(r.ID, "capability_started", "running", "Working", "The application is executing."); err != nil {
		t.Fatal(err)
	}
	if _, err := s.submitResult(r.ID, ResultSubmission{Version: HandoffVersion, Key: "result", Summary: "Appended 'world' as line 2 of file.txt."}); err != nil {
		t.Fatal(err)
	}
	return r, root
}

func runEvidence(r Run, kind string) bool {
	for _, e := range r.Events {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// An attempt that already submitted its result and then hits its deadline
// settles with that result instead of failing finished work (issue #127).
func TestDeadlineAfterSubmitCollectsResult(t *testing.T) {
	app := executionApp(t, `printf done; sleep 30 & wait`)
	s := testStore(t)
	r, root := submittedRun(t, s, app)
	engineCtx, stop := context.WithCancel(context.Background())
	defer stop()
	e := &Engine{provider: &compute.Local{}, Store: s, Apps: []Application{app}, Root: root, ctx: engineCtx}
	ctx, cancel := context.WithTimeout(engineCtx, 2*time.Second)
	defer cancel()
	e.execute(ctx, r)
	finished, err := s.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "done" {
		t.Fatalf("deadline after submit failed the attempt: %+v", finished)
	}
	if finished.Result == nil || finished.Result.Submission.Summary != "Appended 'world' as line 2 of file.txt." {
		t.Fatalf("submitted result not collected: %+v", finished.Result)
	}
	if finished.Summary != "Appended 'world' as line 2 of file.txt." {
		t.Fatalf("settlement summary: %q", finished.Summary)
	}
	if !runEvidence(finished, "deadline_after_submit") {
		t.Fatalf("no deadline_after_submit evidence: %+v", finished.Events)
	}
}

// A deadline without a submission still fails the attempt (issue #127).
func TestDeadlineWithoutSubmissionStillFails(t *testing.T) {
	app := executionApp(t, `printf partial > result.txt; sleep 30 & wait`)
	s := testStore(t)
	root := t.TempDir()
	r, err := s.Create(Brief{Title: "Timed-out work", Workspace: root, AppID: app.ID, TimeoutMinutes: 1}, app.Name, "", false)
	if err != nil {
		t.Fatal(err)
	}
	engineCtx, stop := context.WithCancel(context.Background())
	defer stop()
	e := &Engine{provider: &compute.Local{}, Store: s, Apps: []Application{app}, Root: root, ctx: engineCtx}
	ctx, cancel := context.WithTimeout(engineCtx, 2*time.Second)
	defer cancel()
	e.execute(ctx, r)
	finished, _ := s.Get(r.ID)
	if finished.Status != "failed" {
		t.Fatalf("deadline without submission changed behavior: %+v", finished)
	}
	if runEvidence(finished, "deadline_after_submit") {
		t.Fatal("deadline evidence recorded without a submission")
	}
}

// Once a result is submitted the remaining budget is only a bounded wrap-up: a
// session that never exits is stopped after the grace period and the result is
// collected (issue #127).
func TestSubmissionGraceStopsWrapupAndCollectsResult(t *testing.T) {
	oldGrace := resultWrapupGrace
	resultWrapupGrace = 300 * time.Millisecond
	t.Cleanup(func() { resultWrapupGrace = oldGrace })
	app := executionApp(t, `sleep 30 & wait`)
	s := testStore(t)
	r, root := submittedRun(t, s, app)
	engineCtx, stop := context.WithCancel(context.Background())
	defer stop()
	e := &Engine{provider: &compute.Local{}, Store: s, Apps: []Application{app}, Root: root, ctx: engineCtx, cancel: map[string]context.CancelFunc{}, submitGrace: map[string]*resultGrace{}}
	ctx, cancel := context.WithTimeout(engineCtx, 30*time.Second)
	e.cancel[r.ID] = cancel
	settled := make(chan Run, 1)
	go func() {
		e.execute(ctx, r)
		finished, _ := s.Get(r.ID)
		settled <- finished
	}()
	// The submission arms the grace window once the attempt is underway, as it
	// would from the agent's result_submit during the session; the deadline
	// itself is far away.
	for i := 0; i < 500; i++ {
		current, err := s.Get(r.ID)
		if err == nil && runEvidence(current, "workspace_prepared") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.noteResultSubmitted(r.ID)
	select {
	case finished := <-settled:
		if finished.Status != "done" || finished.Result == nil {
			t.Fatalf("grace did not collect the submitted result: %+v", finished)
		}
		if !runEvidence(finished, "deadline_after_submit") {
			t.Fatalf("no deadline_after_submit evidence: %+v", finished.Events)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("attempt outlived its wrap-up grace")
	}
	cancel()
}

// A restart between submission and completion recovers to the same settled
// state — including the harvested workspace changes a downstream attempt
// continues from — instead of parking a retry that would redo finished work
// (issue #127).
func TestRestartAfterSubmitRecoversSettledResult(t *testing.T) {
	app := executionApp(t, `sleep 30 & wait`)
	s := testStore(t)
	r, root := submittedRun(t, s, app)
	ctx := context.Background()
	// The attempt prepared its workspace and made a change before the server
	// stopped during wrap-up.
	before, err := workspacefs.CaptureSource(ctx, root, s.Dir(), workspacefs.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(s.Dir(), "workspaces", r.ID)
	history, err := workspacefs.Prepare(ctx, base, before)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, "workspace", "file.txt"), []byte("hello\nworld\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	current := s.runs[r.ID]
	current.GraphAttempt = &GraphAttempt{GraphID: "graph_restart"}
	s.runs[r.ID] = current
	if s.graphs == nil {
		s.graphs = map[string]*WorkGraph{}
	}
	s.graphs["graph_restart"] = &WorkGraph{ID: "graph_restart", ProjectID: current.ProjectID, Status: "active", Policy: GraphPolicy{Mode: "adaptive"}, RootTask: "goal", Members: map[string]*GraphMember{}}
	s.mu.Unlock()
	if err := s.RecordWorkspace(r.ID, "workspace_prepared", "Agent workspace prepared.", WorkspaceRecord{Mode: "copy", BaselineCommit: history.BaselineCommit, Fingerprint: before.Fingerprint, Limits: before.Limits}); err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(s, []Application{app}, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	finished, err := s.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Status != "done" || finished.Result == nil || finished.Result.Submission.Summary != "Appended 'world' as line 2 of file.txt." {
		t.Fatalf("restart did not collect the submitted result: %+v", finished)
	}
	if !runEvidence(finished, "restart_after_submit") || !runEvidence(finished, "workspace_harvested") {
		t.Fatalf("recovery evidence: %+v", finished.Events)
	}
	if finished.Decision != nil {
		t.Fatalf("recovery parked the attempt instead of settling it: %+v", finished.Decision)
	}
	patch, err := s.Artifact(r.ID, "changes.patch")
	if err != nil || len(patch) == 0 {
		t.Fatalf("workspace changes not recovered: %v", err)
	}
	if finished.Workspace == nil || finished.Workspace.ResultCommit == "" {
		t.Fatalf("workspace record not settled: %+v", finished.Workspace)
	}
}

// A submitted result whose workspace cannot be recovered keeps the
// interruption state instead of completing without the harvested changes
// (issue #127).
func TestRestartAfterSubmitWithoutWorkspaceKeepsInterruption(t *testing.T) {
	app := executionApp(t, `sleep 30 & wait`)
	s := testStore(t)
	r, root := submittedRun(t, s, app)
	e, err := NewEngine(s, []Application{app}, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	parked, err := s.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if parked.Status != "parked" || parked.Decision == nil || parked.Result == nil {
		t.Fatalf("unrecoverable attempt should keep its interruption state: %+v", parked)
	}
}

// budgetEnd distinguishes a budget end from other cancellations and reports
// the moment execution actually stopped.
func TestBudgetEndDistinguishesGraceFromOtherCancels(t *testing.T) {
	engineCtx, stop := context.WithCancel(context.Background())
	defer stop()
	e := &Engine{ctx: engineCtx, submitGrace: map[string]*resultGrace{}}
	deadline := time.Now().Add(50 * time.Millisecond)
	deadlineCtx, cancelDeadline := context.WithDeadline(engineCtx, deadline)
	defer cancelDeadline()
	time.Sleep(80 * time.Millisecond)
	end, expired := e.budgetEnd(deadlineCtx, "run-x")
	if !expired || !end.Equal(deadline) {
		t.Fatalf("deadline not recognized: %v %v", end, expired)
	}
	graceDeadline := time.Now().Add(-time.Second)
	graceCtx, cancelGrace := context.WithCancel(engineCtx)
	e.submitGrace["run-x"] = &resultGrace{deadline: graceDeadline, expired: true}
	cancelGrace()
	// The grace ended execution before the original deadline; the reported
	// end is the grace expiry, not the unused budget.
	end, expired = e.budgetEnd(graceCtx, "run-x")
	if !expired || !end.Equal(graceDeadline) {
		t.Fatalf("expired grace not recognized: %v %v", end, expired)
	}
	if _, expired = e.budgetEnd(engineCtx, "run-x"); expired {
		t.Fatal("live context counted as expired")
	}
	plainCtx, cancelPlain := context.WithCancel(engineCtx)
	cancelPlain()
	if _, expired = e.budgetEnd(plainCtx, "run-other"); expired {
		t.Fatal("cancellation without a grace window counted as a budget end")
	}
	if !budgetEndedSession(context.Canceled) || !budgetEndedSession(context.DeadlineExceeded) {
		t.Fatal("session-end classification is wrong")
	}
	if budgetEndedSession(errCommandFailed) {
		t.Fatal("an ordinary failure classified as the budget ending the session")
	}
}

// A recovered attempt with bound dependency inputs completes only when those
// copies still match the fingerprint recorded at preparation; a changed copy
// keeps the interruption state instead of completing through recovery (#131
// review).
func TestRestartRecoveryVerifiesDependencyInputs(t *testing.T) {
	for _, test := range []struct {
		name    string
		tamper  bool
		settled bool
	}{{"unchanged inputs settle", false, true}, {"changed inputs keep interruption", true, false}} {
		t.Run(test.name, func(t *testing.T) {
			app := executionApp(t, `sleep 30 & wait`)
			s := testStore(t)
			r, root := submittedRun(t, s, app)
			ctx := context.Background()
			before, err := workspacefs.CaptureSource(ctx, root, s.Dir(), workspacefs.DefaultLimits)
			if err != nil {
				t.Fatal(err)
			}
			base := filepath.Join(s.Dir(), "workspaces", r.ID)
			history, err := workspacefs.Prepare(ctx, base, before)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(base, "workspace", "file.txt"), []byte("hello\nworld\n"), 0600); err != nil {
				t.Fatal(err)
			}
			// Bound upstream inputs: one copied artifact under inputs/, with
			// its preparation fingerprint journalled as the wrap-up would.
			inputs := filepath.Join(base, "inputs", "upstream")
			if err := os.MkdirAll(inputs, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(inputs, "changes.patch"), []byte("patch\n"), 0444); err != nil {
				t.Fatal(err)
			}
			snapshot, err := workspacefs.CaptureData(ctx, filepath.Join(base, "inputs"), workspacefs.DefaultLimits)
			if err != nil {
				t.Fatal(err)
			}
			s.mu.Lock()
			current := s.runs[r.ID]
			current.GraphAttempt = &GraphAttempt{GraphID: "graph_restart"}
			s.runs[r.ID] = current
			if s.graphs == nil {
				s.graphs = map[string]*WorkGraph{}
			}
			s.graphs["graph_restart"] = &WorkGraph{ID: "graph_restart", ProjectID: current.ProjectID, Status: "active", Policy: GraphPolicy{Mode: "adaptive"}, RootTask: "goal", Members: map[string]*GraphMember{}}
			task := s.tasks[r.TaskID]
			task.DependencyEvidence = []workpool.Witness{{TaskID: "upstream", RunID: "run-upstream", Artifacts: []workpool.Artifact{{Name: "changes.patch"}}}}
			s.tasks[r.TaskID] = task
			s.mu.Unlock()
			if err := s.RecordWorkspace(r.ID, "workspace_prepared", "Agent workspace prepared.", WorkspaceRecord{Mode: "copy", BaselineCommit: history.BaselineCommit, Fingerprint: before.Fingerprint, Limits: before.Limits}); err != nil {
				t.Fatal(err)
			}
			if err := s.RecordEvidence(r.ID, "dependency_inputs_prepared", "Bound upstream artifacts were copied and verified before execution.", map[string]any{"fingerprint": snapshot.Fingerprint}); err != nil {
				t.Fatal(err)
			}
			if test.tamper {
				// A misbehaving agent can relax the read-only mode before
				// changing its bound input copy.
				if err := os.Chmod(filepath.Join(inputs, "changes.patch"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(inputs, "changes.patch"), []byte("tampered\n"), 0444); err != nil {
					t.Fatal(err)
				}
			}
			e, err := NewEngine(s, []Application{app}, root)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(e.Close)
			finished, err := s.Get(r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.settled {
				if finished.Status != "done" || finished.Result == nil || !runEvidence(finished, "restart_after_submit") {
					t.Fatalf("recovery did not settle with verified inputs: %+v", finished)
				}
				return
			}
			if finished.Status != "parked" || finished.Decision == nil || finished.Result == nil {
				t.Fatalf("changed inputs should keep the interruption state: %+v", finished)
			}
		})
	}
}
