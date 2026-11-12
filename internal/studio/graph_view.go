package studio

import (
	"slices"
	"strings"
	"time"
)

// Summary reads never clone transcripts, graph receipts or executor configuration.
type GraphSummary struct {
	ID        string         `json:"id"`
	ProjectID string         `json:"project_id"`
	RootTask  string         `json:"root_task"`
	Title     string         `json:"title"`
	Status    string         `json:"status"`
	Revision  int64          `json:"revision"`
	Members   int            `json:"members"`
	Counts    map[string]int `json:"counts"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func (s *Store) GraphSummaries(project, after string, limit int) ([]GraphSummary, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := []string{}
	for id, g := range s.graphs {
		if id > after && (project == "" || g.ProjectID == project) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	next := ""
	if len(ids) > limit {
		next = ids[limit-1]
		ids = ids[:limit]
	}
	out := make([]GraphSummary, 0, len(ids))
	for _, id := range ids {
		g := s.graphs[id]
		counts := map[string]int{}
		for _, m := range g.Members {
			if m.Spec.Kind != "scope" {
				counts[s.graphDisplayState(g, m)]++
			}
		}
		out = append(out, GraphSummary{ID: id, ProjectID: g.ProjectID, RootTask: g.RootTask, Title: g.Members[g.RootTask].Spec.Brief.Title, Status: g.Status, Revision: g.Revision, Members: len(g.Members), Counts: counts, CreatedAt: g.CreatedAt, UpdatedAt: g.UpdatedAt})
	}
	return out, next
}

type GraphNodeView struct {
	CreatedAt time.Time `json:"created_at"`
	State     string    `json:"state"`
	Executor  string    `json:"executor,omitempty"`
	Settled   bool      `json:"settled"`
	Waiting   []string  `json:"waiting,omitempty"`
}
type GraphView struct {
	Graph      WorkGraph                `json:"graph"`
	Nodes      map[string]GraphNodeView `json:"nodes"`
	ObservedAt time.Time                `json:"observed_at"`
}

func (s *Store) graphDisplayState(g *WorkGraph, m *GraphMember) string {
	if m.Accepted != nil {
		return "accepted"
	}
	if m.Replacement != "" {
		return "superseded"
	}
	if m.CancelRequested || g.Status == "cancelled" {
		if !s.graphSettled(m) {
			return "stopping"
		}
		return "cancelled"
	}
	if m.Spec.Kind == "scope" {
		if g.Policy.ControllerApplication == "" && len(g.Members) == 1 {
			return "idle"
		}
		if m.Sealed {
			return "review"
		}
		return "planning"
	}
	if m.State == "done" {
		return "review"
	}
	if m.State == "parked" {
		return "needs_attention"
	}
	if m.State == "queued" {
		if task := s.tasks[m.Spec.ID]; task != nil && !s.graphReadiness(task).Ready {
			return "blocked"
		}
	}
	return m.State
}
func (s *Store) GraphView(id, selected string) (GraphView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := s.graphs[id]
	if g == nil {
		return GraphView{}, ErrNotFound
	}
	// Build the lightweight projection BEFORE cloning; large histories never enter
	// a status response. Selection controls assignment details, not status coverage.
	copy := *g
	copy.Expansions = map[string]*GraphExpansion{}
	for key, x := range g.Expansions {
		v := *x
		v.Reads = nil
		copy.Expansions[key] = &v
	}
	copy.Members = map[string]*GraphMember{}
	copy.Receipts = nil
	copy.Decisions = map[string]*GraphDecision{}
	nodes := map[string]GraphNodeView{}
	for id, m := range g.Members {
		member := *m
		member.History = nil
		member.Executor = nil
		member.Spec.Context = ""
		member.Spec.Arguments = nil
		if id != selected && id != g.RootTask {
			member.Spec.Brief.Intent = ""
			if member.Spec.Agent != nil {
				member.Spec.Agent = &AgentSelection{Role: member.Spec.Agent.Role}
			}
			member.Spec.Criteria = ""
			if m.Accepted != nil {
				accepted := *m.Accepted
				accepted.Evidence = ""
				accepted.Rationale = ""
				member.Accepted = &accepted
			}
		}
		copy.Members[id] = &member
		n := GraphNodeView{CreatedAt: g.CreatedAt, State: s.graphDisplayState(g, m), Settled: s.graphSettled(m)}
		if len(m.Attempts) > 0 {
			if r := s.runs[m.Attempts[0]]; r != nil {
				n.CreatedAt = r.CreatedAt
			}
		}
		if m.Executor != nil {
			n.Executor = m.Executor.Application.Name
		}
		if t := s.tasks[id]; t != nil && m.Spec.Kind != "scope" {
			n.Waiting = s.graphReadiness(t).Waiting
		}
		nodes[id] = n
	}
	// Model requests contain disclosed context/configuration; omit them from UI
	// polling. Candidate operations are included only when inspecting that decision.
	for key, d := range g.Decisions {
		v := *d
		v.Request.State = nil
		v.Config.APIKeyEnv = ""
		v.Proposal.Reads = nil
		if d.TaskID != selected {
			v.Candidates = nil
		}
		copy.Decisions[key] = &v
	}
	copy = graphClone(copy)
	return GraphView{Graph: copy, Nodes: nodes, ObservedAt: time.Now().UTC()}, nil
}
func (s *Store) GraphRun(graph, task, id string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[id]
	if r == nil || r.TaskID != task || r.GraphAttempt == nil || r.GraphAttempt.GraphID != graph {
		return Run{}, ErrNotFound
	}
	copy := *r
	copy.Events = nil
	copy.Execution = nil
	copy.GraphAttempt = nil
	copy.ModelDecisions = nil
	return cloneRun(&copy), nil
}

// GraphController is discoverability metadata, not an additional authority grant.
// Older explicit controller bindings remain valid; ordinary applications are not
// silently treated as planners by the UI.
func (e *Engine) GraphApplications() []map[string]any {
	out := []map[string]any{}
	for _, a := range e.Apps {
		if a.DecisionModel != nil {
			continue
		}
		out = append(out, map[string]any{"id": a.ID, "name": a.Name, "description": a.Description, "controller": a.GraphController})
	}
	slices.SortFunc(out, func(a, b map[string]any) int { return strings.Compare(a["name"].(string), b["name"].(string)) })
	return out
}
