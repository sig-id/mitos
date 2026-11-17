package studio

import (
	"fmt"
	"slices"
	"time"

	"mitos/internal/decision"
	"mitos/internal/workpool"
)

// Graph attempts use the same durable queue and admission/claim path as other
// application work. A graph has no independent process launcher.
func (e *Engine) reconcileGraphs() error {
	// Apply cancellation through the normal process stop and harvest path. Intent
	// remains durable if the engine crashes between this read and cancellation.
	for _, g := range e.Store.Graphs() {
		expired := !time.Now().Before(g.Limits.Deadline)
		for _, id := range graphIDs(&g) {
			m := g.Members[id]
			if !m.CancelRequested && !expired && g.Status == "active" {
				continue
			}
			for _, rid := range m.Attempts {
				r, err := e.Store.Get(rid)
				if err != nil {
					return err
				}
				if (r.Active() || r.Status == "parked") && r.Status != "stopping" {
					if err = e.Cancel(rid); err != nil && err != ErrConflict {
						return err
					}
				}
			}
		}
	}
	if err := e.reconcileGraphOrchestrators(); err != nil {
		return err
	}
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := []string{}
	for id := range s.graphs {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		old := s.graphs[id]
		if s.supervisionHeld(old.ProjectID) || old.Status != "active" || !time.Now().Before(old.Limits.Deadline) {
			continue
		}
		g := graphClone(*old)
		gc := graphChange{}
		g.UpdatedAt = time.Now().UTC()
		continued := false
		for _, tid := range graphIDs(&g) {
			if s.continueYielded(&g, g.Members[tid]) {
				continued = true
			}
		}
		for _, tid := range graphIDs(&g) {
			m := g.Members[tid]
			if m.Spec.Kind != "application" || m.CurrentRunID != "" || m.CancelRequested || m.Replacement != "" || m.Accepted != nil {
				continue
			}
			if g.Attempts >= g.Limits.Attempts {
				break
			}
			ready, err := s.refreshResumedOrchestrator(&g, m)
			if err != nil {
				return err
			}
			if !ready {
				continue
			}
			t := s.tasks[tid]
			if t == nil || !s.graphMemberReadiness(&g, m).Ready {
				continue
			}
			if m.Executor == nil {
				return graphError("missing frozen executor")
			}
			if m.OrchestratorFor != "" && m.ContinueFrom != "" {
				if g.OrchestratorCalls >= g.Limits.OrchestratorCalls {
					continue
				}
				g.OrchestratorCalls++
			}
			rid := newID("local")
			binding := GraphAttempt{ContinueFrom: m.ContinueFrom, Continuation: m.Continuation, GraphID: g.ID, GraphRevision: g.Revision, Specification: m.Revision, PolicyRevision: g.PolicyRevision, Generation: g.OrchestratorGeneration, Executor: *m.Executor, Criteria: m.Spec.Criteria, Context: m.Spec.Context}
			r := Run{ID: rid, TaskID: tid, ProjectID: g.ProjectID, Tenant: decision.LocalTenant, Brief: m.Spec.Brief, AppName: m.Executor.Application.Name, GraphAttempt: &binding, Status: "queued", Stage: "Ready to start", Summary: "Graph task is waiting for execution capacity.", CreatedAt: g.UpdatedAt, UpdatedAt: g.UpdatedAt}
			if agent := m.Executor.Application.Agent; agent != nil && !isOrchestratorKind(agent.Definition.Kind) {
				r.HandoffProtocol = HandoffVersion
			}
			if m.OrchestratorFor != "" {
				for _, member := range g.Members {
					if member.OrchestratorFor == "" && member.CurrentRunID != "" {
						r.ReviewRuns = append(r.ReviewRuns, member.CurrentRunID)
					}
				}
				slices.Sort(r.ReviewRuns)
			}
			if len(m.Attempts) > 0 {
				r.ParentID = m.Attempts[len(m.Attempts)-1]
			}
			m.RefreshContext = false
			m.ContinueFrom = ""
			m.Continuation = nil
			m.CurrentRunID = rid
			m.Attempts = append(m.Attempts, rid)
			m.State = "queued"
			m.FactRevision++
			g.Attempts++
			gc.Runs = append(gc.Runs, r)
		}
		if len(gc.Runs) > 0 || continued {
			gc.Graph = g
			s.projectGraph(&gc, old)
			if err := s.commit("", "effect", "graph_attempts_queued", change{Graph: &gc}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) graphCanDispatch(t *Task) bool {
	g := s.graphs[t.GraphID]
	if g == nil || g.Status != "active" || !time.Now().Before(g.Limits.Deadline) {
		return false
	}
	if g.ServiceRunID != "" {
		parent := s.runs[g.ServiceRunID]
		if parent == nil || parent.Status != "running" || s.lifecycleTaskHeld(s.tasks[parent.TaskID]) {
			return false
		}
	}
	m := g.Members[t.ID]
	return m != nil && !m.CancelRequested && m.Replacement == "" && m.Accepted == nil && m.CurrentRunID == t.CurrentRunID
}
func (s *Store) graphReadiness(t *Task) workpool.Readiness {
	g := s.graphs[t.GraphID]
	if g == nil {
		return workpool.Readiness{Waiting: []string{"graph_missing"}}
	}
	return s.graphMemberReadiness(g, g.Members[t.ID])
}
func (s *Store) graphMemberReadiness(g *WorkGraph, m *GraphMember) workpool.Readiness {
	req := m.Spec.Brief.Scheduling
	req.Deps = append([]workpool.Dependency(nil), req.Deps...)
	states := map[string]workpool.State{}
	for i := range req.Deps {
		dep := &req.Deps[i]
		if dep.Inbox != nil {
			states[dep.From] = s.graphInboxState(*dep)
			continue
		}
		m := g.Members[dep.From]
		if m == nil {
			continue
		}
		upstream := s.tasks[dep.From]
		if upstream == nil {
			continue
		}
		status := "pending"
		if m.Accepted != nil {
			status = "done"
		} else if r := s.runs[m.CurrentRunID]; r != nil && s.graphSettled(m) {
			if dep.Guard == "candidate_available" && r.Status == "done" {
				status = "done"
			} else if r.Status == "failed" || r.Status == "cancelled" {
				status = r.Status
			}
		}
		if dep.Guard == "candidate_available" {
			dep.Guard = "done"
		}
		states[dep.From] = workpool.State{ID: dep.From, Status: status, RunID: m.CurrentRunID, Seq: upstream.Head.Seq, Hash: upstream.Head.Hash}
	}
	ready := workpool.Evaluate(req, states)
	// Witnesses refer to the actual task event status; eligibility above separately
	// establishes graph acceptance rather than treating process exit as acceptance.
	for i := range ready.Matched {
		w := &ready.Matched[i]
		if upstream := s.tasks[w.TaskID]; upstream != nil && w.Inbox == nil {
			w.Status = upstream.Status
		}
	}
	return ready
}

// Relevant outcomes and their pending trigger are written in the SAME journal
// record. Logs/artifact writes do not advance structural or fact revisions.
func (s *Store) graphRunChange(id, kind string, c change, at time.Time, seq int64) (*graphChange, error) {
	r := s.runs[id]
	if r == nil || r.GraphAttempt == nil {
		return nil, nil
	}
	old := s.graphs[r.GraphAttempt.GraphID]
	if old == nil {
		return nil, graphError("attempt lost graph ownership")
	}
	m := old.Members[r.TaskID]
	if m == nil || m.CurrentRunID != id {
		return nil, nil
	}
	relevant := c.Status != "" && c.Status != r.Status || c.Claim != nil && c.Claim.ReleasedAt != nil
	if !relevant {
		return nil, nil
	}
	g := graphClone(*old)
	m = g.Members[r.TaskID]
	m.FactRevision++
	g.UpdatedAt = at
	if c.Status != "" {
		m.State = c.Status
	}
	terminal := c.Status == "done" || c.Status == "failed" || c.Status == "cancelled" || c.Status == "parked"
	if terminal && g.Status == "active" {
		if g.Policy.Mode != "defined" && m.OrchestratorFor == "" {
			g.Cursor = seq
			g.Triggers = append(g.Triggers, GraphTrigger{Seq: seq, TaskID: r.TaskID, RunID: id, Kind: kind})
			// Reopening is serialized with the outcome that makes more work relevant.
			root := g.Members[g.RootTask]
			if root.Sealed {
				root.Sealed = false
				g.Revision++
			}
			if c.Status == "done" && r.Result != nil {
				recordGraphProposals(&g, m, r, seq)
			}
		}
		if c.Status == "done" && m.Yield == nil && m.Spec.Acceptance == "application" && (c.Verified || r.Verified) && (r.Claim == nil || r.Claim.ReleasedAt != nil || c.Claim != nil && c.Claim.ReleasedAt != nil) {
			m.Accepted = &GraphAcceptance{SpecRevision: m.Revision, FactRevision: m.FactRevision, RunID: id, Evidence: fmt.Sprintf("run:%s:verified", id), Rationale: "Registered application verifier passed for this frozen attempt.", At: at, Manifest: graphDigest(struct {
				Run        string
				Spec       int64
				Acceptance any
			}{id, m.Revision, r.Acceptance})}
			m.State = "accepted"
			if r.TaskID == g.RootTask {
				g.Status = "accepted"
			}
		}
	}
	return &graphChange{Graph: g}, nil
}

// recordGraphProposals journals a finished attempt's proposals in the same
// record as its outcome trigger, so the reviewer sees both together. A blocking
// proposal turns the outcome into a yield: the attempt's slot and claim are
// released as for any finished run, but the member waits instead of offering
// a candidate for review.
func recordGraphProposals(g *WorkGraph, m *GraphMember, r *Run, seq int64) {
	sub := r.Result.Submission
	if len(sub.Proposals) == 0 || len(r.Result.ProposalIDs) != len(sub.Proposals) {
		return
	}
	if g.Proposals == nil {
		g.Proposals = map[string]*GraphProposalRecord{}
	}
	blocking := []string{}
	for i, p := range sub.Proposals {
		id := r.Result.ProposalIDs[i]
		g.Proposals[id] = &GraphProposalRecord{ID: id, Local: p.ID, TaskID: r.TaskID, RunID: r.ID, ResultID: r.Result.ID, Trigger: seq, Blocking: p.Blocking}
		if p.Blocking {
			blocking = append(blocking, id)
		}
	}
	m.Proposed += len(sub.Proposals)
	if len(blocking) > 0 {
		m.Yield = &GraphYield{RunID: r.ID, Proposals: blocking}
		m.State = "yielded"
	}
}

// continueYielded readies a yielded member for its continuation once every
// blocking proposal is reviewed and all work accepted for it has settled
// (accepted, or cancelled/replaced without an accepted result). Dependencies on
// abandoned work are dropped so readiness and dependency inputs reflect only
// accepted results. Caller holds the store lock and commits g.
func (s *Store) continueYielded(g *WorkGraph, m *GraphMember) bool {
	if m.Yield == nil || m.CancelRequested || m.Replacement != "" || !s.graphSettled(m) {
		return false
	}
	outcomes := []ProposalOutcome{}
	abandoned := map[string]bool{}
	rewrite := map[string]string{}
	for _, id := range m.Yield.Proposals {
		rec := g.Proposals[id]
		if rec == nil || rec.Outcome == "" {
			return false
		}
		o := ProposalOutcome{ID: rec.ID, Local: rec.Local, Outcome: rec.Outcome, Rationale: rec.Rationale}
		for _, tid := range rec.Tasks {
			accepted := acceptedObligation(g, tid)
			switch {
			case accepted != "":
				o.Completed = append(o.Completed, accepted)
				rewrite[tid] = accepted
			case graphObligationSettled(s, g, tid):
				o.Abandoned = append(o.Abandoned, tid)
				abandoned[tid] = true
			default:
				return false
			}
		}
		outcomes = append(outcomes, o)
	}
	deps := []workpool.Dependency{}
	for _, d := range m.Spec.Brief.Scheduling.Deps {
		if abandoned[d.From] {
			continue
		}
		if to := rewrite[d.From]; to != "" {
			d.From = to
		}
		deps = append(deps, d)
	}
	m.History = append(m.History, m.Spec)
	m.Spec.Brief.Scheduling.Deps = deps
	m.Revision++
	m.FactRevision++
	m.ContinueFrom = m.Yield.RunID
	m.Continuation = &GraphContinuation{Reason: "yield", Proposals: outcomes}
	m.Yield = nil
	m.Yields++
	m.CurrentRunID = ""
	m.State = "queued"
	g.Revision++
	return true
}

// acceptedObligation follows the replacement chain from id and returns the
// member that carries the accepted result, if any.
func acceptedObligation(g *WorkGraph, id string) string {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		m := g.Members[id]
		if m == nil {
			return ""
		}
		if m.Accepted != nil {
			return id
		}
		id = m.Replacement
	}
	return ""
}

// graphObligationSettled reports whether the work at id (or its replacement
// chain) ended without an accepted result and nothing is still running.
func graphObligationSettled(s *Store, g *WorkGraph, id string) bool {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		m := g.Members[id]
		if m == nil || !s.graphSettled(m) {
			return m == nil
		}
		if m.Replacement == "" {
			return m.CancelRequested
		}
		id = m.Replacement
	}
	return true
}
