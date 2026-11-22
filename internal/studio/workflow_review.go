package studio

import (
	"context"
	"fmt"
	"strings"
	"time"

	"mitos/internal/decision"
	"mitos/internal/decisionmodel"
)

// WorkflowReview binds an operator fallback to the same evidence, choices,
// owning attempt and deadline as the Decisions API request.
type WorkflowReview struct {
	Owner               string    `json:"owner"`
	OwnerStatus         string    `json:"owner_status"`
	Generation          int64     `json:"generation"`
	GraphPolicyRevision int64     `json:"graph_policy_revision"`
	GraphRevision       int64     `json:"graph_revision"`
	PolicyRevision      int       `json:"policy_revision"`
	Deadline            time.Time `json:"deadline"`
}

func (s *Store) workflowReviewCurrent(w *WorkflowReview) bool {
	r := s.runs[w.Owner]
	if r == nil || r.Status != w.OwnerStatus || w.OwnerStatus == "done" && !r.Verified || !time.Now().Before(w.Deadline) || s.supervisionHeld(r.ProjectID) || s.agentPolicy(r.ProjectID).Revision != w.PolicyRevision {
		return false
	}
	if r.GraphAttempt == nil {
		return w.Generation == 0
	}
	g := s.graphs[r.GraphAttempt.GraphID]
	if g == nil || g.Status != "active" || g.OrchestratorGeneration != w.Generation || (w.GraphPolicyRevision != 0 && g.PolicyRevision != w.GraphPolicyRevision) || !time.Now().Before(g.Limits.Deadline) || w.OwnerStatus == "done" && g.Revision != w.GraphRevision {
		return false
	}
	m := g.Members[r.TaskID]
	return m != nil && m.CurrentRunID == r.ID && !m.CancelRequested && m.Replacement == ""
}

func bindWorkflowReview(ctx context.Context, g WorkGraph, r Run, revision int) WorkflowReview {
	deadline, _ := ctx.Deadline()
	status := "running"
	if r.Status == "done" {
		status = "done"
	}
	return WorkflowReview{Owner: r.ID, OwnerStatus: status, Generation: g.OrchestratorGeneration, GraphRevision: g.Revision, GraphPolicyRevision: g.PolicyRevision, PolicyRevision: revision, Deadline: deadline}
}

func (e *Engine) awaitWorkflowReview(ctx context.Context, g WorkGraph, r Run, revision int, subject string, req decisionmodel.Request, cause error) (workflowSelection, error) {
	s := e.Store
	id := "review_" + graphDigest([]any{g.ID, r.ID, revision, g.PolicyRevision, req})
	binding := bindWorkflowReview(ctx, g, r, revision)
	s.mu.Lock()
	if !s.workflowReviewCurrent(&binding) {
		s.mu.Unlock()
		return workflowSelection{}, ErrConflict
	}
	if s.runs[id] == nil {
		now := time.Now().UTC()
		question := "Choose how to proceed with " + strings.ReplaceAll(subject, "_", " ") + "."
		card := HumanDecision{ID: newID("decision"), RunID: id, CausedBy: s.seq + 1, Tenant: runTenant(&r), Kind: "choose", ReasonSignal: "workflow_policy_review", InterruptClass: "judgment", Reversibility: "external", Question: question, RequestedAt: now, DedupeKey: id, Context: decision.Context{Attempted: r.Brief.Title, BlockedBy: fmt.Sprintf("The Decisions API needs operator review (%s). Decision evidence: %s", cause, req.State)}}
		for _, opt := range req.Choices {
			card.Options = append(card.Options, Option{ID: opt.Value, Label: opt.Value, Consequence: opt.Description})
		}
		if err := card.Validate(false); err != nil {
			s.mu.Unlock()
			return workflowSelection{}, err
		}
		review := Run{ID: id, ProjectID: g.ProjectID, Tenant: card.Tenant, Brief: Brief{ProjectID: g.ProjectID, Title: question, Workspace: g.Workspace}, ExecutionKind: "workflow_review", WorkflowReview: &binding, Status: "parked", Stage: "Waiting for your decision", Summary: cause.Error(), Decision: &card, CreatedAt: now, UpdatedAt: now}
		if err := s.commit(id, "effect", "workflow_review_requested", change{Run: &review}); err != nil {
			s.mu.Unlock()
			return workflowSelection{}, err
		}
	}
	s.mu.Unlock()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		s.mu.RLock()
		review := s.runs[id]
		valid := s.workflowReviewCurrent(review.WorkflowReview)
		choice := review.Decision.Choice
		cancelled := review.Status == "cancelled"
		s.mu.RUnlock()
		if !valid || cancelled {
			return workflowSelection{}, ErrConflict
		}
		if choice != "" {
			return workflowSelection{Choice: choice, Evaluation: id, Revision: revision}, nil
		}
		select {
		case <-ctx.Done():
			return workflowSelection{}, ctx.Err()
		case <-tick.C:
		}
	}
}

func (s *Store) expireWorkflowReviews() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range s.pendingWorkflowReview {
		r := s.runs[id]
		if !s.workflowReviewCurrent(r.WorkflowReview) {
			_ = s.commit(r.ID, "effect", "workflow_review_expired", change{Status: "cancelled", Stage: "Review closed", Summary: "The originating decision is no longer current."})
		}
	}
}
