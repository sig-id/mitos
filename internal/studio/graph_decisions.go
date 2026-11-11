package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"mitos/internal/decision"
	"mitos/internal/decisionmodel"
)

type GraphDecisionRequest struct {
	Proposal   GraphProposal `json:"proposal"` // envelope; operations must be empty
	Candidates []GraphPlan   `json:"candidates"`
	Context    string        `json:"context"`
}
type GraphDecision struct {
	Key          string                `json:"key"`
	Digest       string                `json:"digest"`
	Proposal     GraphProposal         `json:"proposal"`
	Candidates   []GraphPlan           `json:"candidates"`
	Request      decisionmodel.Request `json:"request"`
	Config       decisionmodel.Config  `json:"config"`
	TaskID       string                `json:"task_id"`
	RunID        string                `json:"run_id"`
	Outcome      string                `json:"outcome"` // queued, pending, selected, rejected, applied, dismissed
	Reason       string                `json:"reason,omitempty"`
	CallDeadline *time.Time            `json:"call_deadline,omitempty"`
	Answer       *decisionmodel.Answer `json:"answer,omitempty"`
	Applied      *GraphReceipt         `json:"applied,omitempty"`
}

func (e *Engine) RequestGraphDecision(req GraphDecisionRequest) (GraphDecision, error) {
	if len(req.Candidates) < 2 || len(req.Candidates) > 16 || len(req.Context) > 16000 || len(req.Proposal.Operations) > 0 {
		return GraphDecision{}, graphError("supply 2–16 candidates, bounded context, and an envelope without operations")
	}
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.graphs[req.Proposal.GraphID]
	if old == nil {
		return GraphDecision{}, ErrNotFound
	}
	digest := graphDigest(req)
	if d := old.Decisions[req.Proposal.Key]; d != nil {
		if d.Digest != digest {
			return GraphDecision{}, ErrConflict
		}
		return graphClone(*d), nil
	}
	app, ok := e.App(old.Policy.DecisionApplication)
	if !ok || app.DecisionModel == nil {
		return GraphDecision{}, graphError("no registered graph decision application")
	}
	if err := app.DecisionModel.Validate(); err != nil {
		return GraphDecision{}, err
	}
	if old.ControllerCalls >= old.Limits.ControllerCalls || old.Attempts >= old.Limits.Attempts || len(old.Expansions) >= old.Limits.Expansions || len(old.Members) >= old.Limits.Tasks {
		return GraphDecision{}, graphError("decision allowance exhausted")
	}
	if old.Members[old.RootTask].Sealed {
		return GraphDecision{}, graphError("decision requires an open root scope")
	}
	for _, candidate := range req.Candidates {
		p := req.Proposal
		p.Operations = candidate.Operations
		p.Explanation = candidate.Explanation
		if _, _, err := e.proposeGraph(old, p, graphDigest(p), false); err != nil {
			return GraphDecision{}, err
		}
	}
	g := graphClone(*old)
	now := time.Now().UTC()
	g.Revision++
	g.ControllerCalls++
	g.Attempts++
	g.UpdatedAt = now
	tid, rid := newID("task"), newID("local")
	p := req.Proposal
	p.Revision = g.Revision
	p.Key = "decision:" + req.Proposal.Key
	d := GraphDecision{Key: req.Proposal.Key, Digest: digest, Proposal: p, Candidates: graphClone(req.Candidates), Config: *app.DecisionModel, TaskID: tid, RunID: rid, Outcome: "queued"}
	state, _ := json.Marshal(map[string]any{"context": req.Context, "graph_id": g.ID, "revision": g.Revision, "reads": p.Reads, "candidates": d.Candidates})
	if len(state) > 40000 {
		return GraphDecision{}, graphError("decision projection exceeds 40 KB")
	}
	d.Request = decisionmodel.Request{ID: newID("graph_decision"), State: state}
	for i, candidate := range req.Candidates {
		desc := candidate.Explanation
		if len(desc) == 0 || len(desc) > 1000 {
			return GraphDecision{}, graphError("each candidate requires a 1–1000 byte explanation")
		}
		d.Request.Choices = append(d.Request.Choices, decisionmodel.Choice{Value: strconv.Itoa(i), Description: desc})
	}
	spec := GraphSpec{ID: tid, Parent: g.RootTask, Kind: "application", Criteria: "Record one typed decision without directly executing its choice", Acceptance: "application", Brief: Brief{ProjectID: g.ProjectID, Title: "Evaluate graph candidates", Workspace: g.Workspace, AppID: app.ID, TimeoutMinutes: 2}}
	spec.Brief.Scheduling.Normalize()
	spec.Brief.Scheduling.Deadline = &p.Deadline
	executor := GraphExecutor{Application: app, Directory: app.Directory, Builtin: app.Builtin}
	executor.Digest = graphDigest(executor)
	binding := GraphAttempt{GraphID: g.ID, GraphRevision: g.Revision, Specification: 1, PolicyRevision: g.PolicyRevision, Generation: g.ControllerGeneration, Executor: executor, Criteria: spec.Criteria}
	g.Members[tid] = &GraphMember{ControllerFor: "decision:" + d.Key, Spec: spec, Revision: 1, FactRevision: 1, Executor: &executor, CurrentRunID: rid, Attempts: []string{rid}, State: "queued"}
	if g.Decisions == nil {
		g.Decisions = map[string]*GraphDecision{}
	}
	g.Decisions[d.Key] = &d
	g.Expansions["decision:"+d.Key] = &GraphExpansion{ID: "decision:" + d.Key, Scope: g.RootTask, Generation: g.ControllerGeneration, Deadline: p.Deadline, ControllerTask: tid}
	if err := validateGraph(&g); err != nil {
		return GraphDecision{}, err
	}
	r := Run{ID: rid, TaskID: tid, Tenant: decision.LocalTenant, ProjectID: g.ProjectID, Brief: spec.Brief, AppName: app.Name, GraphAttempt: &binding, ExecutionKind: "graph_decision", Status: "queued", Stage: "Waiting for decision capacity", CreatedAt: now, UpdatedAt: now}
	gc := graphChange{Graph: g, Runs: []Run{r}}
	s.projectGraph(&gc, old)
	if err := s.commit("", "effect", "graph_decision_queued", change{Graph: &gc}); err != nil {
		return GraphDecision{}, err
	}
	return graphClone(d), nil
}
func (e *Engine) executeGraphDecision(ctx context.Context, r Run) error {
	s := e.Store
	s.mu.Lock()
	old := s.graphs[r.GraphAttempt.GraphID]
	if old == nil {
		s.mu.Unlock()
		return ErrNotFound
	}
	var key string
	for k, d := range old.Decisions {
		if d.RunID == r.ID {
			key = k
			break
		}
	}
	if key == "" {
		s.mu.Unlock()
		return ErrNotFound
	}
	d := old.Decisions[key]
	if d.Outcome != "queued" {
		s.mu.Unlock()
		return graphError("decision already invoked; pending remote consumption requires review")
	}
	g := graphClone(*old)
	copy := g.Decisions[key]
	fresh := old.Status == "active" && old.Revision == d.Proposal.Revision && old.ControllerGeneration == d.Proposal.Generation && time.Now().Before(d.Proposal.Deadline)
	for id, read := range d.Proposal.Reads {
		m := old.Members[id]
		if m == nil || m.Revision != read.Specification || m.FactRevision != read.Fact {
			fresh = false
		}
	}
	if !fresh {
		copy.Outcome = "rejected"
		copy.Reason = "state_changed_before_inference"
		err := s.commit(r.ID, "effect", "graph_decision_rejected", change{Graph: &graphChange{Graph: g}, Status: "failed", Stage: "Decision is stale", Summary: "The candidate snapshot changed before inference. No provider call was made."})
		s.mu.Unlock()
		return err
	}
	copy.Outcome = "pending"
	callDeadline := minTime(copy.Proposal.Deadline, time.Now().UTC().Add(time.Duration(copy.Config.TimeoutMS)*time.Millisecond))
	copy.CallDeadline = &callDeadline
	if err := s.commit(r.ID, "effect", "graph_decision_requested", change{Graph: &graphChange{Graph: g}}); err != nil {
		s.mu.Unlock()
		return err
	}
	request, cfg, deadline := copy.Request, copy.Config, *copy.CallDeadline
	s.mu.Unlock()
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	answer, callErr := decisionmodel.Decide(ctx, cfg, request)
	s.mu.Lock()
	defer s.mu.Unlock()
	old = s.graphs[r.GraphAttempt.GraphID]
	g = graphClone(*old)
	copy = g.Decisions[key]
	current := s.runs[r.ID]
	copy.Outcome = "rejected"
	if callErr == nil && answer.Validate(copy.Request) == nil {
		copy.Answer = &answer
	}
	switch {
	case old.Decisions[key].Outcome != "pending":
		copy.Outcome = old.Decisions[key].Outcome
		copy.Reason = "decision_no_longer_pending"
	case current.Status != "running" || g.Status != "active" || g.ControllerGeneration != copy.Proposal.Generation || g.Revision != copy.Proposal.Revision:
		copy.Reason = "state_changed"
	case !time.Now().Before(deadline):
		copy.Reason = "deadline_expired"
	case callErr != nil:
		copy.Reason = "inference_failed"
	case answer.Validate(copy.Request) != nil:
		copy.Reason = "invalid_answer"
	case !answer.Meets(cfg):
		copy.Reason = "confidence_threshold"
	default:
		copy.Outcome = "selected"
	}
	m := g.Members[r.TaskID]
	m.FactRevision++
	m.State = "done"
	m.Accepted = &GraphAcceptance{SpecRevision: m.Revision, FactRevision: m.FactRevision, RunID: r.ID, Evidence: "decision:" + key, Rationale: "Typed inference receipt recorded; this does not accept the selected work.", At: time.Now().UTC(), Manifest: graphDigest(copy)}
	c := change{Graph: &graphChange{Graph: g}, Status: "done", Stage: "Decision recorded", Summary: "The model response is recorded. Applying a choice requires current graph validation."}
	if current.Status == "stopping" {
		c.Status = "cancelled"
		c.Stage = "Decision cancelled"
	}
	return s.commit(r.ID, "effect", "graph_decision_completed", c)
}
func (e *Engine) ApplyGraphDecision(id, key string) (GraphReceipt, error) {
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.graphs[id]
	if old == nil {
		return GraphReceipt{}, ErrNotFound
	}
	d := old.Decisions[key]
	if d == nil {
		return GraphReceipt{}, ErrNotFound
	}
	if d.Applied != nil {
		return graphClone(*d.Applied), nil
	}
	if d.Outcome != "selected" || d.Answer == nil {
		return GraphReceipt{}, ErrConflict
	}
	index, err := strconv.Atoi(d.Answer.Choice)
	if err != nil || index < 0 || index >= len(d.Candidates) {
		return GraphReceipt{}, ErrConflict
	}
	candidate := d.Candidates[index]
	p := d.Proposal
	p.Operations = []GraphOperation{{Kind: "close_expansion", Expansion: "decision:" + key, Rationale: "Apply recorded choice " + d.Answer.Choice}}
	p.Operations = append(p.Operations, candidate.Operations...)
	p.Explanation = candidate.Explanation
	// Mark consumed only in the candidate transaction so its own decision cannot
	// deadlock sealing. A rejection publishes neither consumption nor graph edits.
	base := graphClone(*old)
	base.Decisions[key].Outcome = "applied"
	next, receipt, err := e.proposeGraph(&base, p, graphDigest(p), false)
	if err != nil {
		return GraphReceipt{}, err
	}
	next.Decisions[key].Applied = &receipt
	gc := graphChange{Graph: next}
	s.projectGraph(&gc, old)
	if err = s.commit("", "effect", "graph_decision_applied", change{Graph: &gc}); err != nil {
		return GraphReceipt{}, err
	}
	return receipt, nil
}
func graphDecisionPending(outcome string) bool {
	return outcome == "queued" || outcome == "pending" || outcome == "selected"
}
func (s *Store) recoverGraphDecisions() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, old := range s.graphs {
		g := graphClone(*old)
		changed := false
		for _, d := range g.Decisions {
			if d.Outcome == "pending" {
				d.Outcome = "rejected"
				d.Reason = "interrupted_unknown_consumption"
				changed = true
			}
		}
		if changed {
			if err := s.commit("", "effect", "graph_decisions_interrupted", change{Graph: &graphChange{Graph: g}}); err != nil {
				return fmt.Errorf("recover graph decision: %w", err)
			}
		}
	}
	return nil
}
