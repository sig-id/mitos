package studio

import (
	"fmt"
	"slices"
	"time"

	"mitos/internal/decision"
)

var errGraphRetry = fmt.Errorf("%w: retry this attempt through its graph", ErrConflict)

func (s *Store) interruptExecution(r Run) error {
	// An interrupted provider call has unknown consumption. Its graph
	// decision already has a separate review and dismissal lifecycle.
	if r.ExecutionKind == "graph_decision" {
		return s.Transition(r.ID, "graph_decision_interrupted", "failed", "Interrupted", "Review the interrupted graph decision before requesting another inference.")
	}
	d := recoveryDecision()
	if r.GraphAttempt == nil {
		return s.Park(r.ID, checkpointOption(r, d))
	}
	d.ReasonSignal = "graph_attempt_interrupted"
	d.Question = "Retry this interrupted attempt through its graph?"
	d.Options[0].Consequence = "Queue a graph-owned replacement within the graph's remaining limits. Inspect and release any outstanding claims first."
	d.Options[1].Consequence = "Close this graph member and preserve the interrupted attempt."
	return s.Park(r.ID, d)
}

func isGraphInterruption(r *Run) bool {
	return r.GraphAttempt != nil && r.Decision != nil && r.Decision.Source == nil &&
		(r.Decision.ReasonSignal == "graph_attempt_interrupted" || r.Decision.ReasonSignal == "execution_interrupted")
}

// The resolution and the graph's scheduling intent share one journal record.
// Only the normal graph scheduler can assign the successor its frozen binding.
func (s *Store) settleGraphInterruption(r *Run, resolution decision.Resolution, command *DeviceCommandRecord) error {
	if r.Decision.AppliedAt != nil {
		return nil
	}
	old := s.graphs[r.GraphAttempt.GraphID]
	if old == nil {
		return ErrNotFound
	}
	now := time.Now().UTC()
	g := graphClone(*old)
	// A worker cancellation is also an ordinary terminal fact, which can
	// unblock dependents and wake the graph's planner.
	if resolution.Choice == "cancel" && r.Status != "cancelled" {
		outcome, err := s.graphRunChange(r.ID, "approval_resolved", change{Status: "cancelled"}, now, s.seq+1)
		if err != nil {
			return err
		}
		if outcome != nil {
			g = outcome.Graph
		}
	}
	m := g.Members[r.TaskID]
	if m == nil || m.CurrentRunID != r.ID || m.Accepted != nil {
		return ErrConflict
	}
	x := g.Expansions[m.OrchestratorFor]
	if resolution.Choice == "retry" {
		if r.Status != "parked" || g.Status != "active" || m.CancelRequested || m.Replacement != "" ||
			g.Attempts >= g.Limits.Attempts || !now.Before(g.Limits.Deadline) {
			return fmt.Errorf("%w: graph member is closed or its execution allowance has expired", ErrConflict)
		}
		if r.Claim != nil && r.Claim.ReleasedAt == nil {
			return fmt.Errorf("%w: inspect and release outstanding execution claims before retrying", ErrConflict)
		}
		if m.OrchestratorFor != "" {
			if r.ExecutionKind == "graph_decision" || x == nil || x.OrchestratorTask != r.TaskID || x.Outcome != "" || !now.Before(x.Deadline) ||
				g.OrchestratorCalls >= g.Limits.OrchestratorCalls || len(g.Members) >= g.Limits.Tasks {
				return fmt.Errorf("%w: planning expansion cannot be retried", ErrConflict)
			}
			s.replaceGraphOrchestrator(&g)
			m.State = "cancelled"
		} else {
			if len(m.Attempts)-m.Yields > g.Limits.Retries || m.Yield != nil {
				return fmt.Errorf("%w: graph member cannot retry within its allowance", ErrConflict)
			}
			m.State, m.CurrentRunID = "queued", ""
		}
	} else {
		m.State, m.CancelRequested = "cancelled", true
		if x != nil && x.OrchestratorTask == r.TaskID && x.Outcome == "" {
			x.Error, x.Outcome = "", "Operator closed the interrupted planning attempt."
			for _, d := range g.Decisions {
				if d.TaskID == r.TaskID && graphDecisionPending(d.Outcome) {
					d.Outcome, d.Reason = "dismissed", x.Outcome
				}
			}
			for i := range g.Triggers {
				tr := &g.Triggers[i]
				if tr.Outcome == "" && fmt.Sprintf("event:%d", tr.Seq) == x.ID {
					tr.Outcome = x.Outcome
				}
			}
		}
	}
	if resolution.Choice == "retry" {
		m.FactRevision++
	}
	g.Revision++
	g.UpdatedAt = now
	if err := validateGraph(&g); err != nil {
		return err
	}
	gc := graphChange{Graph: g}
	s.projectGraph(&gc, old)
	c := change{Graph: &gc, DeviceCommand: command, DecisionApplied: true, ResultRunID: r.ID, Status: "cancelled", Stage: "Closed", Summary: "The graph applied the operator's " + resolution.Choice + " choice."}
	if r.Decision.Resolution == nil {
		c.Resolution, c.Choice = &resolution, resolution.Choice
	}
	// The run event projects its own task. Keep only sibling changes in the
	// graph projection so replay attaches this record to the old run once.
	for i, task := range gc.Tasks {
		if task.ID == r.TaskID {
			task.Status = m.State
			c.Task, c.TaskEffect = &task, "task_"+m.State
			gc.Tasks = slices.Delete(gc.Tasks, i, i+1)
			break
		}
	}
	kind := "decision_applied"
	if c.Resolution != nil {
		kind = "approval_resolved"
	}
	return s.commit(r.ID, "effect", kind, c)
}
