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
	if err := e.reconcileGraphControllers(); err != nil {
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
		if old.Status != "active" || !time.Now().Before(old.Limits.Deadline) {
			continue
		}
		g := graphClone(*old)
		gc := graphChange{}
		g.UpdatedAt = time.Now().UTC()
		for _, tid := range graphIDs(&g) {
			m := g.Members[tid]
			if m.Spec.Kind != "application" || m.CurrentRunID != "" || m.CancelRequested || m.Replacement != "" || m.Accepted != nil {
				continue
			}
			if g.Attempts >= g.Limits.Attempts {
				break
			}
			t := s.tasks[tid]
			if t == nil || !s.graphReadiness(t).Ready {
				continue
			}
			if m.Executor == nil {
				return graphError("missing frozen executor")
			}
			rid := newID("local")
			binding := GraphAttempt{GraphID: g.ID, GraphRevision: g.Revision, Specification: m.Revision, PolicyRevision: g.PolicyRevision, Generation: g.ControllerGeneration, Executor: *m.Executor, Criteria: m.Spec.Criteria, Context: m.Spec.Context}
			r := Run{ID: rid, TaskID: tid, ProjectID: g.ProjectID, Tenant: decision.LocalTenant, Brief: m.Spec.Brief, AppName: m.Executor.Application.Name, GraphAttempt: &binding, Status: "queued", Stage: "Ready to start", Summary: "Graph task is waiting for execution capacity.", CreatedAt: g.UpdatedAt, UpdatedAt: g.UpdatedAt}
			if len(m.Attempts) > 0 {
				r.ParentID = m.Attempts[len(m.Attempts)-1]
			}
			m.CurrentRunID = rid
			m.Attempts = append(m.Attempts, rid)
			m.State = "queued"
			m.FactRevision++
			g.Attempts++
			gc.Runs = append(gc.Runs, r)
		}
		if len(gc.Runs) > 0 {
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
	m := g.Members[t.ID]
	return m != nil && !m.CancelRequested && m.Replacement == "" && m.Accepted == nil && m.CurrentRunID == t.CurrentRunID
}
func (s *Store) graphReadiness(t *Task) workpool.Readiness {
	g := s.graphs[t.GraphID]
	if g == nil {
		return workpool.Readiness{Waiting: []string{"graph_missing"}}
	}
	req := t.Scheduling
	req.Deps = append([]workpool.Dependency(nil), req.Deps...)
	states := map[string]workpool.State{}
	for i := range req.Deps {
		dep := &req.Deps[i]
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
		if upstream := s.tasks[w.TaskID]; upstream != nil {
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
		if g.Policy.Mode != "defined" && m.ControllerFor == "" {
			g.Cursor = seq
			g.Triggers = append(g.Triggers, GraphTrigger{Seq: seq, TaskID: r.TaskID, RunID: id, Kind: kind})
			// Reopening is serialized with the outcome that makes more work relevant.
			root := g.Members[g.RootTask]
			if root.Sealed {
				root.Sealed = false
				g.Revision++
			}
		}
		if c.Status == "done" && m.Spec.Acceptance == "application" && (c.Verified || r.Verified) && (r.Claim == nil || r.Claim.ReleasedAt != nil || c.Claim != nil && c.Claim.ReleasedAt != nil) {
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
