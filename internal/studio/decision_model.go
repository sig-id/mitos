package studio

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"mitos/internal/decisionmodel"
)

// ModelDecision is an inference receipt, separate from human approval cards.
// Pending receipts consume the call budget even when remote consumption is unknown.
type ModelDecision struct {
	Version      int                   `json:"version"`
	Hook         string                `json:"hook"`
	Segment      int                   `json:"segment"`
	Target       string                `json:"target"`
	Request      decisionmodel.Request `json:"request"`
	RequestedSeq int64                 `json:"requested_seq"`
	RequestedAt  time.Time             `json:"requested_at"`
	CompletedAt  *time.Time            `json:"completed_at,omitempty"`
	Deadline     time.Time             `json:"deadline"`
	Outcome      string                `json:"outcome"` // pending, accepted, escalated, rejected
	Reason       string                `json:"reason,omitempty"`
	Answer       *decisionmodel.Answer `json:"answer,omitempty"`
}

var errModelBudget = errors.New("model_call_limit")

func findModelDecision(r *Run, hook string, segment int, target string) *ModelDecision {
	for i := len(r.ModelDecisions) - 1; i >= 0; i-- {
		d := &r.ModelDecisions[i]
		if d.Hook == hook && d.Segment == segment && d.Target == target {
			return d
		}
	}
	return nil
}

func (s *Store) beginModelDecision(id, hook string, segment int, target, signal, action string) (ModelDecision, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ModelDecision{}, false, ErrNotFound
	}
	if !r.Active() || r.Status == "stopping" || r.Execution == nil || !time.Now().Before(r.Execution.Deadline) {
		return ModelDecision{}, false, ErrConflict
	}
	cfg := r.Execution.Application.DecisionModel
	if cfg == nil || !cfg.HasHook(hook) {
		return ModelDecision{}, false, ErrConflict
	}
	if existing := findModelDecision(r, hook, segment, target); existing != nil {
		return *existing, false, nil
	}
	if len(r.ModelDecisions) >= cfg.MaxCalls {
		return ModelDecision{}, false, errModelBudget
	}
	// Transmit a deliberate, bounded projection, never the workspace, environment,
	// transcript, arbitrary event payloads, or raw worker output.
	state, err := json.Marshal(map[string]any{
		"task_id": r.TaskID, "run_id": r.ID, "hook": hook, "segment": segment,
		"target": target, "title": r.Brief.Title, "intent": r.Brief.Intent,
		"signal": signal, "acceptance": r.Acceptance,
		"application":        map[string]string{"id": r.Execution.Application.ID, "entrypoint": r.Execution.Application.Entrypoint, "description": r.Execution.Application.Description},
		"artifacts":          r.Artifacts[max(0, len(r.Artifacts)-8):],
		"remaining_calls":    cfg.MaxCalls - len(r.ModelDecisions) - 1,
		"execution_deadline": r.Execution.Deadline,
	})
	if err != nil {
		return ModelDecision{}, false, err
	}
	description := "Start the declared application segment within its existing execution limits."
	if action == "rewind" {
		description = "Apply the already eligible recovery to the specified passing checkpoint within the existing restart and time limits."
	}
	now := time.Now().UTC()
	d := ModelDecision{Version: 1, Hook: hook, Segment: segment, Target: target, RequestedSeq: s.seq + 1, RequestedAt: now,
		Deadline: minTime(r.Execution.Deadline, now.Add(time.Duration(cfg.TimeoutMS)*time.Millisecond)), Outcome: "pending",
		Request: decisionmodel.Request{ID: newID("model"), State: state, Choices: []decisionmodel.Choice{
			{Value: action, Description: description},
			{Value: "escalate", Description: "Keep execution stopped and ask the operator to review missing facts, ambiguity, or risk."},
		}},
	}
	if err = s.commit(id, "decision", "model_decision_requested", change{ModelDecision: &d}); err != nil {
		return ModelDecision{}, false, err
	}
	return d, true, nil
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func (s *Store) finishModelDecision(id, requestID string, answer decisionmodel.Answer, callErr error) (ModelDecision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ModelDecision{}, ErrNotFound
	}
	if len(r.ModelDecisions) == 0 {
		return ModelDecision{}, ErrConflict
	}
	d := r.ModelDecisions[len(r.ModelDecisions)-1]
	if d.Request.ID != requestID || d.Outcome != "pending" {
		return ModelDecision{}, ErrConflict
	}
	now := time.Now().UTC()
	d.CompletedAt = &now
	d.Outcome = "rejected"
	if callErr == nil && answer.Validate(d.Request) == nil {
		d.Answer = &answer
	}
	switch {
	case !r.Active() || r.Status == "stopping" || len(r.Events) == 0 || r.Events[len(r.Events)-1].Seq != d.RequestedSeq:
		d.Reason = "state_changed"
	case !now.Before(d.Deadline):
		d.Reason = "deadline_expired"
	case callErr != nil:
		d.Reason = callErr.Error() // Adapter errors are fixed codes; never remote bodies or credentials.
	default:
		if err := answer.Validate(d.Request); err != nil {
			d.Reason = err.Error()
		} else {
			d.Answer = &answer
			switch {
			case !answer.Meets(*r.Execution.Application.DecisionModel):
				d.Outcome, d.Reason = "escalated", "uncertain"
			case answer.Choice == "escalate":
				d.Outcome, d.Reason = "escalated", "model_escalated"
			default:
				d.Outcome = "accepted"
			}
		}
	}
	if err := s.commit(id, "effect", "model_decision_completed", change{ModelDecision: &d}); err != nil {
		return ModelDecision{}, err
	}
	return d, nil
}

func (e *Engine) modelBoundary(ctx context.Context, id, hook string, segment int, target, signal, action string) (bool, error) {
	r, err := e.Store.Get(id)
	if err != nil {
		return false, err
	}
	cfg := r.Execution.Application.DecisionModel
	if cfg == nil || !cfg.HasHook(hook) {
		return true, nil
	}
	d, fresh, err := e.Store.beginModelDecision(id, hook, segment, target, signal, action)
	if errors.Is(err, errModelBudget) {
		return false, e.parkModelDecision(id, "model_call_limit")
	}
	if err != nil {
		return false, err
	}
	if fresh {
		callCtx, cancel := context.WithDeadline(ctx, d.Deadline)
		answer, callErr := decisionmodel.Decide(callCtx, *cfg, d.Request)
		cancel()
		d, err = e.Store.finishModelDecision(id, d.Request.ID, answer, callErr)
		if err != nil {
			return false, err
		}
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if d.Outcome == "accepted" {
		return true, nil
	}
	reason := d.Reason
	if d.Outcome == "pending" {
		reason = "outcome_unknown"
	}
	return false, e.parkModelDecision(id, "decision_model_"+reason)
}

func (e *Engine) parkModelDecision(id, signal string) error {
	if err := e.Store.ReleaseClaims(id, "execution_stopped_and_harvest_finished"); err != nil {
		return err
	}
	d := recoveryDecision()
	d.ReasonSignal = signal
	d.Question = "Review this workflow decision?"
	d.Context.BlockedBy = "The decision model did not select an eligible next workflow transition: " + signal + ". Its durable request and any validated answer remain on the run. Review them before starting a fresh attempt."
	// A rewind cannot clear a model boundary or reset its inference allowance.
	return e.Store.Park(id, d)
}
