package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"mitos/internal/workpool"
	"slices"
	"strings"
	"time"
)

const (
	// The planning journal is compacted at this fixed entry boundary and keeps
	// the newest entries verbatim, so prompts stay cache-stable except at rare,
	// predictable compaction points.
	graphJournalCompactAt   = 96
	graphJournalCompactKeep = 32
	// Summaries are bounded by their serialized JSON size, not rune count:
	// multibyte and JSON-escaped text can inflate one rune up to sixfold.
	graphJournalSummary = 512
	// The serialized journal is also bounded by bytes independently of the
	// entry count. Escaped or multibyte summaries can exceed the planning
	// payload allowance well before the entry boundary, which would otherwise
	// park valid work behind orchestrator_context_limit.
	graphJournalByteBudget = 32 << 10
	// The inline journal is small; detail is behind graph_read. The planning
	// payload allowance is halved from the old full-snapshot budget.
	orchestratorPlanLimit = 64 << 10
)

// boundJournalSummary truncates a summary until its serialized JSON form fits
// the per-entry byte budget, counting multibyte and escaped bytes alike.
func boundJournalSummary(summary string) string {
	runes := []rune(summary)
	lo, hi := 0, len(runes)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if encoded, err := json.Marshal(string(runes[:mid])); err == nil && len(encoded) <= graphJournalSummary {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return string(runes[:lo])
}

// appendFactJournal records one immutable entry per newly observed task fact
// revision. Entries freeze the observed state and are never rewritten, so
// consecutive planning calls share their prefix up to the newest entry.
func appendFactJournal(g *WorkGraph, s *Store) {
	last := map[string]int64{}
	for _, e := range g.FactJournal {
		if e.Compacted > 0 {
			for task, fact := range e.Facts {
				if fact > last[task] {
					last[task] = fact
				}
			}
			continue
		}
		if e.Fact > last[e.Task] {
			last[e.Task] = e.Fact
		}
	}
	for _, tid := range graphIDs(g) {
		m := g.Members[tid]
		if m == nil || m.OrchestratorFor != "" || m.FactRevision <= last[tid] {
			continue
		}
		entry := GraphFactEntry{Task: tid, Kind: m.Spec.Kind, Parent: m.Spec.Parent, Fact: m.FactRevision, State: m.State, RunID: m.CurrentRunID, Accepted: m.Accepted != nil}
		if r := s.runs[m.CurrentRunID]; r != nil {
			entry.Summary = r.Summary
			entry.ArtifactCount = len(r.Artifacts)
		}
		entry.Summary = boundJournalSummary(entry.Summary)
		g.FactJournal = append(g.FactJournal, entry)
	}
	journal := func() int {
		raw, _ := json.Marshal(g.FactJournal)
		return len(raw)
	}
	// Compact at the fixed entry boundary, or earlier when serialized summaries
	// (multibyte or JSON-escaped) already exceed the journal's byte budget.
	if len(g.FactJournal) >= graphJournalCompactAt || (len(g.FactJournal) > graphJournalCompactKeep && journal() > graphJournalByteBudget) {
		absorbed := map[string]int64{}
		absorbedCount := 0
		for _, e := range g.FactJournal[:len(g.FactJournal)-graphJournalCompactKeep] {
			if e.Compacted > 0 {
				absorbedCount += e.Compacted
				for task, fact := range e.Facts {
					if fact > absorbed[task] {
						absorbed[task] = fact
					}
				}
				continue
			}
			absorbedCount++
			if e.Fact > absorbed[e.Task] {
				absorbed[e.Task] = e.Fact
			}
		}
		kept := append([]GraphFactEntry(nil), g.FactJournal[len(g.FactJournal)-graphJournalCompactKeep:]...)
		g.FactJournal = append([]GraphFactEntry{{Compacted: absorbedCount, Facts: absorbed}}, kept...)
	}
}

// renderPlanJournal renders the inline planning state as an append-only JSON
// document: a stable header plus the journal in commit order.
func renderPlanJournal(g *WorkGraph) string {
	entries := append([]GraphFactEntry(nil), g.FactJournal...)
	if entries == nil {
		entries = []GraphFactEntry{}
	}
	raw, _ := json.Marshal(map[string]any{"journal_version": 1, "graph_id": g.ID, "root_task": g.RootTask, "journal": entries})
	return string(raw)
}

// sharedStatePrefix measures the cache-stable prefix between consecutive
// planning renders, including its digest for receipts.
func sharedStatePrefix(previous, current string) (int, string) {
	bound := min(len(previous), len(current))
	n := 0
	for n < bound && previous[n] == current[n] {
		n++
	}
	if n == 0 {
		return 0, ""
	}
	sum := sha256.Sum256([]byte(current[:n]))
	return n, hex.EncodeToString(sum[:])
}

func (e *Engine) reconcileGraphOrchestrators() error {
	for _, snapshot := range e.Store.Graphs() {
		if e.Store.GraphPrerequisiteBlocked(snapshot.ID) {
			continue
		}
		if e.Store.SupervisionHeld(snapshot.ProjectID) || snapshot.Status != "active" || snapshot.Policy.OrchestratorApplication == "" || !time.Now().Before(snapshot.Limits.Deadline) {
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
			if x.Outcome != "" || x.Error != "" || x.OrchestratorTask == "" {
				continue
			}
			m := snapshot.Members[x.OrchestratorTask]
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
				if err = e.graphOrchestratorError(snapshot.ID, key, x.OrchestratorTask, "orchestrator_execution_needs_review"); err != nil {
					return err
				}
				continue
			}
			// A orchestrator deliberately exposes one typed proposal file. Native stdout
			// remains untouched and cannot accidentally become a control command.
			proposalName := "graph-proposal.json"
			for _, artifact := range r.Artifacts {
				if strings.HasSuffix(artifact.Name, "graph-proposal.json") {
					proposalName = artifact.Name
				}
			}
			body, err := e.Store.Artifact(r.ID, proposalName)
			if err != nil {
				if err = e.graphOrchestratorError(snapshot.ID, key, x.OrchestratorTask, "orchestrator_proposal_missing"); err != nil {
					return err
				}
				continue
			}
			var plan OrchestratorPlan
			err = DecodeGraphJSON(body, &plan)
			if err == nil {
				err = e.applyOrchestratorPlan(snapshot, key, r, plan)
			}
			if err != nil {
				if err = e.graphOrchestratorError(snapshot.ID, key, x.OrchestratorTask, "Orchestrator could not apply its proposal: "+err.Error()); err != nil {
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
		if s.graphPrerequisiteBlocked(old) {
			continue
		}
		if s.supervisionHeld(old.ProjectID) || old.Status != "active" || old.Policy.OrchestratorApplication == "" || !time.Now().Before(old.Limits.Deadline) {
			continue
		}
		if old.Policy.Automatic {
			waiting := false
			for _, m := range old.Members {
				if m.Spec.Kind == "application" && m.OrchestratorFor == "" && !m.CancelRequested && m.Replacement == "" {
					if m.CurrentRunID != "" && !s.graphSettled(m) {
						waiting = true
					}
					if m.CurrentRunID == "" && s.tasks[m.Spec.ID] != nil && s.graphReadiness(s.tasks[m.Spec.ID]).Ready {
						waiting = true
					}
				}
			}
			if waiting {
				continue
			}
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
					g.Expansions[key] = &GraphExpansion{ID: key, Scope: g.RootTask, Generation: g.OrchestratorGeneration, Deadline: g.Limits.Deadline}
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
		// One orchestrator in flight per graph. Parallel workers remain independently runnable.
		busy := false
		for _, x := range g.Expansions {
			if x.Outcome == "" && x.OrchestratorTask != "" && x.Error == "" {
				busy = true
			}
		}
		if !busy {
			for _, key := range keys {
				x := g.Expansions[key]
				if strings.HasPrefix(key, "decision:") {
					continue
				}
				if x.Outcome != "" || x.OrchestratorTask != "" || x.Error != "" {
					continue
				}
				if g.OrchestratorCalls >= g.Limits.OrchestratorCalls || len(g.Members) >= g.Limits.Tasks || g.Attempts >= g.Limits.Attempts {
					if x.Error == "" {
						x.Error = "orchestrator_allowance_exhausted"
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
				// Disclosure is explicitly enabled by configuring the orchestrator application;
				// include bounded facts and references, not arbitrary workspace contents.
				type fact struct {
					ID            string     `json:"id"`
					Spec          GraphSpec  `json:"spec"`
					State         string     `json:"state"`
					RunID         string     `json:"run_id"`
					Accepted      bool       `json:"accepted"`
					Summary       string     `json:"summary,omitempty"`
					Artifacts     []Artifact `json:"artifacts,omitempty"`
					ResultID      string     `json:"result_id,omitempty"`
					ArtifactCount int        `json:"artifact_count"`
				}
				facts := []fact{}
				reads := map[string]GraphRead{}
				dependencies := []workpool.Dependency{}
				for _, tid := range graphIDs(&g) {
					m := g.Members[tid]
					if m.OrchestratorFor != "" {
						continue
					}
					f := fact{ID: tid, Spec: m.Spec, State: m.State, RunID: m.CurrentRunID, Accepted: m.Accepted != nil}
					if r := s.runs[m.CurrentRunID]; r != nil {
						f.Summary = r.Summary
						if len(f.Summary) > 2000 {
							f.Summary = f.Summary[:2000]
						}
						f.ArtifactCount = len(r.Artifacts)
						f.Artifacts = append([]Artifact(nil), r.Artifacts[:min(8, len(r.Artifacts))]...)
						if r.Result != nil {
							f.ResultID = r.Result.ID
						}
						if s.graphSettled(m) && r.Result == nil {
							names := []string{}
							for _, a := range r.Artifacts {
								if (a.Name == "output.txt" || a.Name == "report.md") && a.Bytes <= 1<<20 {
									names = append(names, a.Name)
								}
							}
							if g.Policy.Automatic && len(names) > 1 {
								if slices.Contains(names, "report.md") {
									names = []string{"report.md"}
								} else {
									names = names[:1]
								}
							}
							if len(names) > 0 && r.Status != "parked" {
								guard := r.Status
								if guard == "done" && m.Accepted == nil {
									guard = "candidate_available"
								}
								dependencies = append(dependencies, workpool.Dependency{From: tid, Guard: guard, Payload: "data", Artifacts: names})
							}
						}
					}
					f.Spec.Context = ""
					if f.Spec.Agent != nil {
						f.Spec.Agent = &AgentSelection{Role: f.Spec.Agent.Role}
					}
					facts = append(facts, f)
					reads[tid] = GraphRead{m.Revision, m.FactRevision}
				}
				if g.Policy.Automatic {
					s.addOrchestratorEvidence(&g, dependencies)
				}
				raw, _ := json.Marshal(map[string]any{"graph_id": g.ID, "root_task": g.RootTask, "expansion": key, "inbox": graphInboxAddress(&g, s.seq), "facts": facts, "triggers": g.Triggers, "policy": g.Policy, "remaining_tasks": g.Limits.Tasks - len(g.Members) - 1, "instruction": "Propose the next work from the goal and available evidence. Write graph-proposal.json using the shared OrchestratorPlan: operations, accept, complete, result and explanation. Use the same scheduling dependencies and joins as the graph API. Ordinary worker output remains native. Do not close this expansion or classify triggers; the kernel does that. Scope acceptance requires operator review."})
				if g.Policy.Automatic {
					// The automatic planning state is an append-only journal: entries are
					// frozen once written and only compacted at fixed boundaries, so
					// consecutive planning prompts share their prefix.
					appendFactJournal(&g, s)
					state := renderPlanJournal(&g)
					prefixBytes, prefixSHA := sharedStatePrefix(g.LastPlanState, state)
					g.LastPlanState = state
					g.PlanPrefixCharacters = prefixBytes
					g.PlanPrefixSHA256 = prefixSHA
					library := e.AutomaticAgents()["agents"]
					raw, _ = json.Marshal(map[string]any{"graph_id": g.ID, "root_task": g.RootTask, "graph_state": json.RawMessage(state), "agent_library": library, "execution_policy": graphExecution(&g), "remaining_tasks": g.Limits.Tasks - len(g.Members) - 1, "remaining_attempts": g.Limits.Attempts - g.Attempts, "remaining_plans": g.Limits.OrchestratorCalls - g.OrchestratorCalls, "inbox": graphInboxAddress(&g, s.seq), "previous_error": x.LastError, "plan_prefix": map[string]any{"bytes": prefixBytes, "sha256": prefixSHA}})
				}
				limit := graphContextLimit(&g)
				if g.Policy.Automatic {
					limit = orchestratorPlanLimit
				}
				if len(raw) > limit {
					x.Error = "orchestrator_context_limit"
					changed = true
					break
				}
				tid := newID("task")
				spec := GraphSpec{ID: tid, Parent: x.Scope, Kind: "application", Required: false, Criteria: "Produce a valid graph proposal within policy", Acceptance: "application", Context: string(raw), Brief: Brief{Title: "Plan the next work", Intent: "Read the graph context and propose a bounded amendment. Write graph-proposal.json; leave normal analysis in your native output.", AppID: g.Policy.OrchestratorApplication, TimeoutMinutes: 5}}
				if g.Policy.Automatic {
					spec.Brief.Title = "Plan goal"
					if g.OrchestratorCalls > 0 {
						spec.Brief.Title = "Review progress"
					}
					spec.Brief.Intent = "Read current evidence and choose the next assignments or review the completed goal."
				}
				spec.Brief.Scheduling.Deps = dependencies
				normalized, executor, err := e.resolveGraphSpec(&g, spec)
				if err != nil {
					x.Error = "orchestrator_adapter_unavailable"
					changed = true
					break
				}
				g.Members[tid] = &GraphMember{OrchestratorFor: key, Spec: normalized, Executor: executor, Revision: 1, FactRevision: 1, State: "queued"}
				g.OrchestratorCalls++
				g.Revision++
				x.OrchestratorTask = tid
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
			if err := s.commit("", "effect", "graph_orchestrator_scheduled", change{Graph: &gc}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (e *Engine) graphOrchestratorError(id, key, task, reason string) error {
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.graphs[id]
	if old == nil {
		return ErrNotFound
	}
	x := old.Expansions[key]
	if x == nil || x.OrchestratorTask != task || x.Outcome != "" || x.Error != "" {
		return nil
	}
	g := graphClone(*old)
	g.Expansions[key].Error = reason
	if g.Policy.Automatic && strings.HasPrefix(reason, "Orchestrator could not apply") && g.OrchestratorCalls < g.Limits.OrchestratorCalls && g.Expansions[key].Failures < 2 {
		g.Expansions[key].Error = ""
		g.Expansions[key].OrchestratorTask = ""
		g.Revision++
		g.Expansions[key].LastError = reason
		g.Expansions[key].Failures++
	}
	return s.commit("", "effect", "graph_orchestrator_blocked", change{Graph: &graphChange{Graph: g}})
}

// graphTaskRead pages bounded graph detail for orchestrator attempts. The
// inline planning journal stays small; specs, criteria and artifact references
// are fetched on demand. Workers cannot read beyond their own graph facts.
type GraphTaskDetail struct {
	Task      string     `json:"task"`
	Spec      GraphSpec  `json:"spec"`
	State     string     `json:"state"`
	RunID     string     `json:"run_id,omitempty"`
	Accepted  bool       `json:"accepted,omitempty"`
	ResultID  string     `json:"result_id,omitempty"`
	Artifacts []Artifact `json:"artifacts,omitempty"`
	Revision  int64      `json:"revision"`
}
type GraphTaskPage struct {
	Tasks      []GraphTaskDetail `json:"tasks"`
	NextCursor int               `json:"next_cursor"`
}

func (s *Store) graphTaskRead(run string, in GraphTaskRead) (any, error) {
	if in.Cursor < 0 || in.Limit > 16 || len(in.Task) > 160 || in.Task == "" && in.Limit < 1 {
		return nil, handoffError("invalid_graph_read", 0)
	}
	limit := in.Limit
	if in.Task != "" {
		limit = 1
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[run]
	if r == nil || r.GraphAttempt == nil {
		return nil, handoffError("run_binding_required", 0)
	}
	g := s.graphs[r.GraphAttempt.GraphID]
	if g == nil {
		return nil, handoffError("graph_unavailable", 0)
	}
	orchestrator := false
	for _, m := range g.Members {
		if m.OrchestratorFor != "" && slices.Contains(m.Attempts, run) {
			orchestrator = true
		}
	}
	if !orchestrator {
		return nil, handoffError("graph_read_requires_orchestrator", 0)
	}
	ids := []string{}
	for _, tid := range graphIDs(g) {
		if g.Members[tid].OrchestratorFor == "" && (in.Task == "" || tid == in.Task) {
			ids = append(ids, tid)
		}
	}
	if in.Task != "" && len(ids) == 0 {
		return nil, handoffError("task_unavailable", 0)
	}
	page := GraphTaskPage{Tasks: []GraphTaskDetail{}}
	end := in.Cursor + limit
	for i, tid := range ids {
		if i < in.Cursor {
			continue
		}
		if i >= end {
			break
		}
		m := g.Members[tid]
		detail := GraphTaskDetail{Task: tid, Spec: m.Spec, State: m.State, RunID: m.CurrentRunID, Accepted: m.Accepted != nil, Revision: m.Revision}
		detail.Spec.Context = ""
		if current := s.runs[m.CurrentRunID]; current != nil {
			detail.Artifacts = append([]Artifact(nil), current.Artifacts[:min(8, len(current.Artifacts))]...)
			if current.Result != nil {
				detail.ResultID = current.Result.ID
			}
		}
		page.Tasks = append(page.Tasks, detail)
	}
	if in.Cursor+len(page.Tasks) < len(ids) {
		page.NextCursor = in.Cursor + len(page.Tasks)
	}
	return page, nil
}

// Caller holds the store lock. Reports retain priority over supplementary files.
func (s *Store) addOrchestratorEvidence(g *WorkGraph, deps []workpool.Dependency) {
	count := 0
	var size int64
	for _, dep := range deps {
		m := g.Members[dep.From]
		r := s.runs[m.CurrentRunID]
		for _, a := range r.Artifacts {
			if slices.Contains(dep.Artifacts, a.Name) {
				count++
				size += a.Bytes
			}
		}
	}
	for i := range deps {
		m := g.Members[deps[i].From]
		if m.Accepted != nil {
			continue
		}
		r := s.runs[m.CurrentRunID]
		for _, name := range []string{"changes.patch", "changes.json"} {
			for _, a := range r.Artifacts {
				if a.Name == name && a.Bytes <= 1<<20 && count < dependencyFiles && size+a.Bytes <= dependencyBytes {
					deps[i].Artifacts = append(deps[i].Artifacts, name)
					count++
					size += a.Bytes
				}
			}
		}
	}
}
