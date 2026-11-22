package studio

import (
	"context"
	"errors"
	"time"
)

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

// budgetExpired reports whether an attempt's execution context was ended by
// its time budget: the deadline itself, or the wrap-up grace that follows a
// result submission. Operator stops and server shutdowns are settled before
// this is consulted.
func (e *Engine) budgetExpired(ctx context.Context, run string) bool {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return true
	}
	if ctx.Err() == nil {
		return false
	}
	e.mu.Lock()
	g := e.submitGrace[run]
	grace := g != nil && g.expired
	e.mu.Unlock()
	return grace
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
