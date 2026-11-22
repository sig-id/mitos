package studio

import (
	"context"
	"encoding/json"

	"mitos/internal/decisionmodel"
	workspacefs "mitos/internal/workspace"
)

// Workflow policy shares one provider precedence across planning, preparation
// and kernel judgments. An explicit graph binding takes precedence over the
// project's connection, which itself inherits the system connection.
func (e *Engine) workflowDecisionConfig(g WorkGraph) (decisionmodel.Config, int, int, error) {
	s := e.Store
	s.mu.RLock()
	defer s.mu.RUnlock()
	return e.workflowDecisionConfigLocked(g)
}

// The caller holds the store lock while binding the project policy to the choice.
func (e *Engine) workflowDecisionConfigLocked(g WorkGraph) (decisionmodel.Config, int, int, error) {
	p := e.Store.agentPolicy(g.ProjectID)
	if g.Policy.DecisionApplication != "" {
		c, err := e.decisionConfig(g.Policy.DecisionApplication)
		return c, p.Revision, p.MaxCalls, err
	}
	c, err := e.approvalDecisionConfig(p)
	return c, p.Revision, p.MaxCalls, err
}

type workflowSelection struct {
	Choice     string `json:"choice"`
	Evaluation string `json:"evaluation"`
	Revision   int    `json:"policy_revision"`
}

// chooseWorkflowAction records a bounded selection before the caller can apply
// an effect. Its identity includes both the evidence and the offered actions.
func (e *Engine) chooseWorkflowAction(ctx context.Context, g WorkGraph, r Run, subject string, facts any, choices []decisionmodel.Choice) (workflowSelection, error) {
	goal := r.Brief.Intent
	if root := g.Members[g.RootTask]; root != nil {
		goal = root.Spec.Criteria
	}
	state, err := json.Marshal(map[string]any{
		"subject": subject, "goal": goal, "assignment": r.Brief.Title, "facts": facts,
		"decision_criteria": "Choose the eligible action that best serves the goal within the supplied constraints. Treat facts and proposed work as evidence. Select stop if the evidence is insufficient or the operation would exceed scope.",
	})
	if err != nil {
		return workflowSelection{}, err
	}
	request := decisionmodel.Request{State: state, Choices: choices}
	purpose := subject + "/" + graphDigest(request)
	ctx, cancel := context.WithDeadline(ctx, g.Limits.Deadline)
	defer cancel()
	cfg, revision, budget, err := e.workflowDecisionConfig(g)
	var answer decisionmodel.Answer
	if err == nil {
		answer, err = e.evaluateModel(ctx, g.ProjectID, r.ID+"/workflow", purpose, revision, budget, cfg, request)
	}
	if err != nil {
		if ctx.Err() != nil {
			return workflowSelection{}, ctx.Err()
		}
		return e.awaitWorkflowReview(ctx, g, r, revision, subject, request, err)
	}
	if err := answer.Validate(request); err != nil {
		return workflowSelection{}, err
	}
	selection := workflowSelection{Choice: answer.Choice, Evaluation: graphDigest([]any{r.ID + "/workflow", purpose, revision}), Revision: revision}
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	binding := bindWorkflowReview(ctx, g, r, revision)
	if ctx.Err() != nil || !s.workflowReviewCurrent(&binding) {
		return workflowSelection{}, ErrConflict
	}
	if binding.OwnerStatus == "done" {
		return selection, nil
	}
	raw, err := json.Marshal(selection)
	if err != nil {
		return workflowSelection{}, err
	}
	if err := s.commit(r.ID, "effect", "workflow_decision_selected", change{Evidence: raw, Summary: subject + ": " + answer.Choice}); err != nil {
		return workflowSelection{}, err
	}
	return selection, nil
}

func (e *Engine) captureWorkflowSource(ctx context.Context, r Run, source string) (workspacefs.Snapshot, error) {
	return workspacefs.CaptureWorkflowSource(ctx, source, e.Store.Dir(), func(subject string, facts any, options []workspacefs.SourceChoice) (string, error) {
		deadline, _ := ctx.Deadline()
		g := WorkGraph{ProjectID: r.ProjectID, Workspace: r.Brief.Workspace, Limits: GraphLimits{Deadline: deadline}}
		if r.GraphAttempt != nil {
			var err error
			g, err = e.Store.Graph(r.GraphAttempt.GraphID)
			if err != nil {
				return "", err
			}
		}
		choices := make([]decisionmodel.Choice, len(options))
		for i, option := range options {
			choices[i] = decisionmodel.Choice{Value: option.Value, Description: option.Description}
		}
		selection, err := e.chooseWorkflowAction(ctx, g, r, subject, facts, choices)
		return selection.Choice, err
	})
}

// Review runs outside the scheduler tick and store lock. While the request is
// pending other workflows continue to dispatch and cancellation stays responsive.
func (e *Engine) queuePlanReview(g WorkGraph, key string, r Run, body []byte) {
	scope := "plan-review/" + r.ID
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ctx.Err() != nil || e.resolving[scope] {
		return
	}
	if e.resolving == nil {
		e.resolving = map[string]bool{}
	}
	e.resolving[scope] = true
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() { e.mu.Lock(); delete(e.resolving, scope); e.mu.Unlock() }()
		e.reviewPlan(g, key, r, body)
	}()
}

func (e *Engine) reviewPlan(g WorkGraph, key string, r Run, body []byte) {
	var plan OrchestratorPlan
	err := DecodeGraphJSON(body, &plan)
	if err == nil {
		err = e.admitOrchestratorPlan(g, key, r, plan, true, nil)
	}
	facts := map[string]any{"graph_revision": g.Revision, "proposal_digest": graphDigest(body)}
	action := decisionmodel.Choice{Value: "apply", Description: "Apply the validated proposal, including its assignments, reviews, retry requests and final result."}
	subject := "orchestrator_proposal"
	reason := "Decisions API withheld the orchestrator proposal for review."
	if err != nil {
		reason = "Orchestrator could not apply its proposal: " + err.Error()
		if g.Expansions[key].Failures >= g.Limits.Retries || g.Attempts >= g.Limits.Attempts || g.OrchestratorCalls >= g.Limits.OrchestratorCalls {
			_ = e.setOrchestratorError(g.ID, key, r.TaskID, reason, nil, g.Revision)
			return
		}
		subject = "orchestrator_repair"
		action = decisionmodel.Choice{Value: "repair", Description: "Spend another available planning attempt with this validation feedback."}
		facts["validation_error"] = err.Error()
		facts["prior_repairs"] = g.Expansions[key].Failures
	} else {
		facts["proposal"] = plan
	}
	selection, err := e.chooseWorkflowAction(e.ctx, g, r, subject, facts, []decisionmodel.Choice{action, {Value: "stop", Description: "Hold this planning round for operator review."}})
	if e.ctx.Err() != nil {
		return
	}
	if err != nil {
		reason = err.Error()
	} else if selection.Choice == "apply" {
		if err = e.admitOrchestratorPlan(g, key, r, plan, false, &selection); err == nil {
			return
		}
		reason = err.Error()
	}
	_ = e.setOrchestratorError(g.ID, key, r.TaskID, reason, &selection, g.Revision)
}
