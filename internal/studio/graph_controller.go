package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"mitos/internal/workpool"
	"slices"
	"strings"
	"time"
)

// GraphPlan is the optional structured tool boundary of a registered controller,
// never the reply protocol of ordinary workers. Workers' output.txt is evidence;
// it is interpreted only by an explicitly configured controller application.
type GraphPlan struct {
	Operations  []GraphOperation `json:"operations"`
	Explanation string           `json:"explanation"`
}

func (e *Engine) reconcileGraphControllers() error {
	for _, snapshot := range e.Store.Graphs() {
		if snapshot.Status != "active" || snapshot.Policy.ControllerApplication == "" || !time.Now().Before(snapshot.Limits.Deadline) {
			continue
		}
		keys := []string{}
		for key := range snapshot.Expansions {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			x := snapshot.Expansions[key]
			if strings.HasPrefix(key, "decision:") {
				continue
			}
			if x.Outcome != "" || x.Error != "" || x.ControllerTask == "" {
				continue
			}
			m := snapshot.Members[x.ControllerTask]
			if m == nil {
				continue
			}
			r, err := e.Store.Get(m.CurrentRunID)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if r.Status != "done" && r.Status != "failed" && r.Status != "parked" && r.Status != "cancelled" {
				continue
			}
			if r.Status != "done" || !r.Verified {
				if err = e.graphControllerError(snapshot.ID, key, x.ControllerTask, "controller_execution_needs_review"); err != nil {
					return err
				}
				continue
			}
			// A controller deliberately exposes one typed proposal file. Native stdout
			// remains untouched and cannot accidentally become a control command.
			proposalName := "graph-proposal.json"
			for _, artifact := range r.Artifacts {
				if strings.HasSuffix(artifact.Name, "graph-proposal.json") {
					proposalName = artifact.Name
				}
			}
			body, err := e.Store.Artifact(r.ID, proposalName)
			if err != nil {
				if err = e.graphControllerError(snapshot.ID, key, x.ControllerTask, "controller_proposal_missing"); err != nil {
					return err
				}
				continue
			}
			var plan GraphPlan
			if err = DecodeGraphJSON(body, &plan); err != nil {
				if err = e.graphControllerError(snapshot.ID, key, x.ControllerTask, "controller_proposal_invalid"); err != nil {
					return err
				}
				continue
			}
			ops := []GraphOperation{}
			// Controller generation and triggering facts are captured before launch.
			// The authority to close this planning obligation is supplied by the kernel.
			ops = append(ops, GraphOperation{Kind: "close_expansion", Expansion: key, Rationale: plan.Explanation})
			for _, tr := range snapshot.Triggers {
				if tr.Seq <= x.TriggerCursor && tr.Outcome == "" {
					ops = append(ops, GraphOperation{Kind: "classify_trigger", Trigger: tr.Seq, Rationale: "Considered by controller attempt " + r.ID})
				}
			}
			ops = append(ops, plan.Operations...)
			p := GraphProposal{Profile: GraphProfile, Key: "controller:" + r.ID, GraphID: snapshot.ID, Revision: x.Revision, PolicyRevision: snapshot.PolicyRevision, Generation: x.Generation, Deadline: x.Deadline, Reads: x.Reads, Operations: ops, Explanation: plan.Explanation}
			if _, _, err = e.amendGraph(p, false, false); err != nil {
				if err = e.graphControllerError(snapshot.ID, key, x.ControllerTask, "controller_proposal_stale_or_rejected"); err != nil {
					return err
				}
			}
		}
	}
	// Register event-driven planning obligations and their executable Task in a
	// single commit. No placeholder live supervisor occupies a worker slot.
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
		if old.Status != "active" || old.Policy.ControllerApplication == "" || !time.Now().Before(old.Limits.Deadline) {
			continue
		}
		g := graphClone(*old)
		changed := false
		open := false
		for _, x := range g.Expansions {
			if x.Outcome == "" {
				open = true
			}
		}
		if !open {
			for _, tr := range g.Triggers {
				if tr.Outcome == "" && len(g.Expansions) < g.Limits.Expansions {
					key := fmt.Sprintf("event:%d", tr.Seq)
					g.Expansions[key] = &GraphExpansion{ID: key, Scope: g.RootTask, Generation: g.ControllerGeneration, Deadline: g.Limits.Deadline}
					changed = true
					break
				}
			}
		}
		keys := []string{}
		for key := range g.Expansions {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		// One planner in flight per graph. Parallel workers remain independently runnable.
		busy := false
		for _, x := range g.Expansions {
			if x.Outcome == "" && x.ControllerTask != "" && x.Error == "" {
				busy = true
			}
		}
		if !busy {
			for _, key := range keys {
				x := g.Expansions[key]
				if strings.HasPrefix(key, "decision:") {
					continue
				}
				if x.Outcome != "" || x.ControllerTask != "" || x.Error != "" {
					continue
				}
				if g.ControllerCalls >= g.Limits.ControllerCalls || len(g.Members) >= g.Limits.Tasks || g.Attempts >= g.Limits.Attempts {
					if x.Error == "" {
						x.Error = "controller_allowance_exhausted"
						changed = true
					}
					break
				}
				if !time.Now().Before(x.Deadline) {
					x.Error = "expansion_deadline_expired"
					changed = true
					break
				}
				if g.Members[x.Scope].Sealed {
					x.Error = "scope_sealed"
					changed = true
					break
				}
				// Disclosure is explicitly enabled by configuring the controller application;
				// include bounded facts and references, not arbitrary workspace contents.
				type fact struct {
					ID        string     `json:"id"`
					Spec      GraphSpec  `json:"spec"`
					State     string     `json:"state"`
					RunID     string     `json:"run_id"`
					Accepted  bool       `json:"accepted"`
					Summary   string     `json:"summary,omitempty"`
					Artifacts []Artifact `json:"artifacts,omitempty"`
				}
				facts := []fact{}
				reads := map[string]GraphRead{}
				dependencies := []workpool.Dependency{}
				for _, tid := range graphIDs(&g) {
					m := g.Members[tid]
					if m.ControllerFor != "" {
						continue
					}
					f := fact{ID: tid, Spec: m.Spec, State: m.State, RunID: m.CurrentRunID, Accepted: m.Accepted != nil}
					if r := s.runs[m.CurrentRunID]; r != nil {
						f.Summary = r.Summary
						if len(f.Summary) > 2000 {
							f.Summary = f.Summary[:2000]
						}
						f.Artifacts = append([]Artifact(nil), r.Artifacts...)
						if s.graphSettled(m) {
							names := []string{}
							for _, a := range r.Artifacts {
								if (a.Name == "output.txt" || a.Name == "report.md") && a.Bytes <= 1<<20 {
									names = append(names, a.Name)
								}
							}
							if len(names) > 0 {
								guard := r.Status
								if guard == "done" && m.Accepted == nil {
									guard = "candidate_available"
								}
								dependencies = append(dependencies, workpool.Dependency{From: tid, Guard: guard, Payload: "data", Artifacts: names})
							}
						}
					}
					facts = append(facts, f)
					reads[tid] = GraphRead{m.Revision, m.FactRevision}
				}
				raw, _ := json.Marshal(map[string]any{"graph_id": g.ID, "root_task": g.RootTask, "expansion": key, "facts": facts, "triggers": g.Triggers, "policy": g.Policy, "remaining_tasks": g.Limits.Tasks - len(g.Members) - 1, "instruction": "Propose the next work from the goal and available evidence. Write graph-proposal.json in the workspace with operations and explanation. Ordinary worker output remains native. Do not close this expansion or classify triggers; the kernel does that. Scope acceptance requires operator review."})
				if len(raw) > 16000 {
					x.Error = "controller_context_limit"
					changed = true
					break
				}
				tid := newID("task")
				spec := GraphSpec{ID: tid, Parent: x.Scope, Kind: "application", Required: false, Criteria: "Produce a valid graph proposal within policy", Acceptance: "application", Context: string(raw), Brief: Brief{Title: "Plan the next work", Intent: "Read the graph context and propose a bounded amendment. Write graph-proposal.json; leave normal analysis in your native output.", AppID: g.Policy.ControllerApplication, TimeoutMinutes: 5}}
				spec.Brief.Scheduling.Deps = dependencies
				normalized, executor, err := e.resolveGraphSpec(&g, spec)
				if err != nil {
					x.Error = "controller_adapter_unavailable"
					changed = true
					break
				}
				g.Members[tid] = &GraphMember{ControllerFor: key, Spec: normalized, Executor: executor, Revision: 1, FactRevision: 1, State: "queued"}
				g.ControllerCalls++
				g.Revision++
				x.ControllerTask = tid
				x.Revision = g.Revision
				x.Reads = reads
				x.TriggerCursor = g.Cursor
				changed = true
				break
			}
		}
		if changed {
			g.UpdatedAt = time.Now().UTC()
			if err := validateGraph(&g); err != nil {
				return err
			}
			gc := graphChange{Graph: g}
			s.projectGraph(&gc, old)
			if err := s.commit("", "effect", "graph_controller_scheduled", change{Graph: &gc}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (e *Engine) graphControllerError(id, key, task, reason string) error {
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.graphs[id]
	if old == nil {
		return ErrNotFound
	}
	x := old.Expansions[key]
	if x == nil || x.ControllerTask != task || x.Outcome != "" || x.Error != "" {
		return nil
	}
	g := graphClone(*old)
	g.Expansions[key].Error = reason
	return s.commit("", "effect", "graph_controller_blocked", change{Graph: &graphChange{Graph: g}})
}
