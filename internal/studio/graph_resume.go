package studio

import (
	"encoding/json"
	"strings"
	"time"

	"mitos/internal/workpool"
)

// Limits are new cumulative ceilings, not refunded usage. This operator action
// is deliberately separate from amendments, which stop at the old deadline.
type ResumeGraph struct {
	StartAutomatic bool        `json:"start_automatic,omitempty"`
	Key            string      `json:"key"`
	Revision       int64       `json:"revision"`
	Limits         GraphLimits `json:"limits"`
	AttemptMinutes int         `json:"attempt_minutes"`
}

func (e *Engine) ResumeGraph(id string, req ResumeGraph) (WorkGraph, error) {
	s := e.Store
	var policy GraphPolicy
	var setup *FrozenSetup
	if req.StartAutomatic {
		prior, err := s.Graph(id)
		if err != nil {
			return WorkGraph{}, err
		}
		digest := resumeGraphDigest(req)
		if receipt, ok := prior.Receipts[req.Key]; ok {
			if receipt.Digest != digest {
				return WorkGraph{}, ErrConflict
			}
			return prior, nil
		}
		if prior.Revision != req.Revision || prior.Status != "active" || len(prior.Members) != 1 || prior.Attempts != 0 || prior.Policy.OrchestratorApplication != "" {
			return WorkGraph{}, ErrConflict
		}
		policy, err = e.automaticPolicy()
		if err != nil {
			return WorkGraph{}, err
		}
		setup, err = e.freezeSetup(prior.ProjectID)
		if err != nil {
			return WorkGraph{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.graphs[id]
	if old == nil {
		return WorkGraph{}, ErrNotFound
	}
	digest := resumeGraphDigest(req)
	if receipt, ok := old.Receipts[req.Key]; ok {
		if receipt.Digest != digest {
			return WorkGraph{}, ErrConflict
		}
		return graphClone(*old), nil
	}
	now := time.Now().UTC()
	if !validGraphKey(req.Key) || req.AttemptMinutes < 1 || req.AttemptMinutes > 120 {
		return WorkGraph{}, graphError("provide a request key and an attempt time limit of 1–120 minutes")
	}
	if err := validateGraphLimits(req.Limits, now); err != nil {
		return WorkGraph{}, err
	}
	if req.Revision != old.Revision || old.Status != "active" {
		return WorkGraph{}, ErrConflict
	}
	if s.supervisionHeld(old.ProjectID) {
		return WorkGraph{}, graphError("resolve the project's supervision incident before resuming")
	}
	if old.ServiceRunID != "" {
		return WorkGraph{}, graphError("resume recurring work through its owning service")
	}
	a, b := req.Limits, old.Limits
	if a.Tasks < b.Tasks || a.Amendments < b.Amendments || a.Attempts < b.Attempts || a.Retries < b.Retries || a.Expansions < b.Expansions || a.OrchestratorCalls < b.OrchestratorCalls || a.Depth < b.Depth || a.Fanout < b.Fanout || a.Deadline.Before(b.Deadline) {
		return WorkGraph{}, graphError("resuming can only increase resource allowances")
	}
	for _, m := range old.Members {
		if !s.graphSettled(m) {
			return WorkGraph{}, graphError("wait for all agents to stop and their execution claims to be released before resuming")
		}
	}
	for _, d := range old.Decisions {
		if graphDecisionPending(d.Outcome) {
			return WorkGraph{}, graphError("resolve pending graph decisions before resuming")
		}
	}
	g := graphClone(*old)
	if req.StartAutomatic {
		if len(g.Members) != 1 || g.Attempts != 0 || g.Policy.OrchestratorApplication != "" {
			return WorkGraph{}, ErrConflict
		}
		g.Policy, g.Setup = policy, setup
		g.Members[g.RootTask].Sealed = false
		open := false
		for _, x := range g.Expansions {
			if x.Outcome == "" {
				open = true
			}
		}
		if !open {
			if len(g.Expansions) >= req.Limits.Expansions {
				return WorkGraph{}, graphError("increase the planning round allowance to start work")
			}
			key := "resume:" + digest[:16]
			g.Expansions[key] = &GraphExpansion{ID: key, Scope: g.RootTask}
		}
	}
	g.Limits = req.Limits
	g.Revision++
	g.PolicyRevision++
	g.OrchestratorGeneration++
	g.UpdatedAt = now
	pending, orchestrators := 0, 0
	for _, tid := range graphIDs(&g) {
		m := g.Members[tid]
		if m.CancelRequested || m.Replacement != "" || m.Spec.Kind != "application" || strings.HasPrefix(m.OrchestratorFor, "decision:") {
			continue
		}
		x := g.Expansions[m.OrchestratorFor]
		orchestrator := x != nil && x.Outcome == "" && x.OrchestratorTask == tid
		if m.Accepted != nil && !orchestrator {
			continue
		}
		run := s.runs[m.CurrentRunID]
		if run == nil && m.CurrentRunID == "" && len(m.Attempts) > 0 {
			run = s.runs[m.Attempts[len(m.Attempts)-1]]
		}
		retry := orchestrator || run != nil && (run.Status == "failed" || run.Status == "cancelled")
		if retry && run != nil {
			if len(m.Attempts) > g.Limits.Retries {
				return WorkGraph{}, graphError("increase the per-task retry allowance for %s", tid)
			}
			m.ContinueFrom = run.ID
			m.CurrentRunID = ""
			m.Accepted = nil
			m.State = "queued"
			m.FactRevision++
		}
		if m.CurrentRunID == "" {
			if orchestrator {
				m.RefreshContext = true
				orchestrators++
			}
			pending++
			m.History = append(m.History, graphClone(m.Spec))
			m.Revision++
			m.Spec.Brief.Scheduling.Deadline = &g.Limits.Deadline
			m.Spec.Brief.TimeoutMinutes = max(m.Spec.Brief.TimeoutMinutes, req.AttemptMinutes)
		}
	}
	if g.Attempts+max(1, pending) > g.Limits.Attempts || len(g.Receipts)+1 >= g.Limits.Amendments {
		return WorkGraph{}, graphError("increase attempts and amendment allowances to leave room for continuation")
	}
	if g.OrchestratorCalls+orchestrators > g.Limits.OrchestratorCalls {
		return WorkGraph{}, graphError("increase the planning call allowance for continuation")
	}
	for _, x := range g.Expansions {
		if x.Outcome != "" || strings.HasPrefix(x.ID, "decision:") {
			continue
		}
		x.LastError = x.Error
		x.Error = ""
		x.Deadline = g.Limits.Deadline
		x.Generation = g.OrchestratorGeneration
		x.Revision = g.Revision
	}
	g.Receipts[req.Key] = GraphReceipt{Key: req.Key, Digest: digest, Revision: g.Revision, At: now}
	gc := graphChange{Graph: g}
	s.projectGraph(&gc, old)
	if err := s.commit("", "effect", "graph_resumed", change{Graph: &gc}); err != nil {
		return WorkGraph{}, err
	}
	return graphClone(g), nil
}

// Refresh the existing orchestrator after resumed workers settle. Its old
// conversation remains useful, but its old facts and dependency witnesses do not.
// Caller holds the store lock. No new orchestrator identity is created.
func (s *Store) refreshResumedOrchestrator(g *WorkGraph, m *GraphMember) (bool, error) {
	if !m.RefreshContext {
		return true, nil
	}
	facts := []map[string]any{}
	reads := map[string]GraphRead{}
	deps := []workpool.Dependency{}
	for _, id := range graphIDs(g) {
		worker := g.Members[id]
		if worker.OrchestratorFor != "" {
			continue
		}
		if worker.Spec.Kind == "application" && !worker.CancelRequested && worker.Replacement == "" && worker.Accepted == nil {
			if !s.graphSettled(worker) || worker.CurrentRunID == "" && s.tasks[id] != nil && s.graphReadiness(s.tasks[id]).Ready {
				return false, nil
			}
		}
		spec := worker.Spec
		spec.Context = ""
		if spec.Agent != nil {
			spec.Agent = &AgentSelection{Role: spec.Agent.Role}
		}
		f := map[string]any{"id": id, "spec": spec, "state": worker.State, "run_id": worker.CurrentRunID, "accepted": worker.Accepted != nil}
		reads[id] = GraphRead{worker.Revision, worker.FactRevision}
		if r := s.runs[worker.CurrentRunID]; r != nil {
			f["summary"] = r.Summary[:min(len(r.Summary), 2000)]
			f["artifacts"] = r.Artifacts
			names := []string{}
			for _, a := range r.Artifacts {
				if (a.Name == "report.md" || a.Name == "output.txt") && a.Bytes <= 1<<20 {
					names = append(names, a.Name)
				}
			}
			if len(names) > 1 {
				names = []string{"report.md"}
			}
			if len(names) > 0 && s.graphSettled(worker) {
				guard := r.Status
				if guard == "done" && worker.Accepted == nil {
					guard = "candidate_available"
				}
				deps = append(deps, workpool.Dependency{From: id, Guard: guard, Payload: "data", Artifacts: names})
			}
		}
		facts = append(facts, f)
	}
	var context map[string]any
	if err := json.Unmarshal([]byte(m.Spec.Context), &context); err != nil {
		return false, err
	}
	context["facts"], context["remaining_tasks"], context["remaining_attempts"], context["remaining_plans"] = facts, g.Limits.Tasks-len(g.Members), g.Limits.Attempts-g.Attempts, g.Limits.OrchestratorCalls-g.OrchestratorCalls
	context["triggers"] = g.Triggers
	context["inbox"] = graphInboxAddress(g, s.seq)
	context["instruction"] = orchestratorInstructions
	raw, err := json.Marshal(context)
	if err != nil {
		return false, err
	}
	if len(raw) > graphContextLimit(g) {
		return false, graphError("resumed orchestrator context exceeds its limit")
	}
	m.Spec.Context = string(raw)
	s.addOrchestratorEvidence(g, deps)
	m.Spec.Brief.Scheduling.Deps = deps
	x := g.Expansions[m.OrchestratorFor]
	x.Reads, x.Revision, x.TriggerCursor = reads, g.Revision, g.Cursor
	return true, nil
}

func resumeGraphDigest(req ResumeGraph) string {
	return graphDigest(struct {
		Action  string
		Request ResumeGraph
	}{"resume", req})
}
