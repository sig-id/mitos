package studio

import (
	"context"
	"testing"
	"time"

	"mitos/internal/compute"
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
	// The submission arms the grace window; the deadline itself is far away.
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

// A restart between submission and deadline recovers to the same settled state
// instead of parking a retry that would redo finished work (issue #127).
func TestRestartAfterSubmitRecoversSettledResult(t *testing.T) {
	app := executionApp(t, `sleep 30 & wait`)
	s := testStore(t)
	r, root := submittedRun(t, s, app)
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
	if !runEvidence(finished, "restart_after_submit") {
		t.Fatalf("no restart_after_submit evidence: %+v", finished.Events)
	}
	if finished.Decision != nil {
		t.Fatalf("recovery parked the attempt instead of settling it: %+v", finished.Decision)
	}
}

// budgetExpired distinguishes a budget end from other cancellations.
func TestBudgetExpiredDistinguishesGraceFromOtherCancels(t *testing.T) {
	engineCtx, stop := context.WithCancel(context.Background())
	defer stop()
	e := &Engine{ctx: engineCtx, submitGrace: map[string]*resultGrace{}}
	deadlineCtx, cancelDeadline := context.WithTimeout(engineCtx, 10*time.Millisecond)
	defer cancelDeadline()
	time.Sleep(20 * time.Millisecond)
	if !e.budgetExpired(deadlineCtx, "run-x") {
		t.Fatal("deadline not recognized")
	}
	graceCtx, cancelGrace := context.WithCancel(engineCtx)
	e.submitGrace["run-x"] = &resultGrace{expired: true}
	cancelGrace()
	if !e.budgetExpired(graceCtx, "run-x") {
		t.Fatal("expired grace not recognized")
	}
	if e.budgetExpired(engineCtx, "run-x") {
		t.Fatal("live context counted as expired")
	}
	plainCtx, cancelPlain := context.WithCancel(engineCtx)
	cancelPlain()
	if e.budgetExpired(plainCtx, "run-other") {
		t.Fatal("cancellation without a grace window counted as a budget end")
	}
}
