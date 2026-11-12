package studio

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"mitos/internal/decision"
	"mitos/internal/workpool"
)

func (s *Store) Graph(id string) (WorkGraph, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := s.graphs[id]
	if g == nil {
		return WorkGraph{}, ErrNotFound
	}
	return graphClone(*g), nil
}
func (s *Store) Graphs() []WorkGraph {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []WorkGraph{}
	for _, g := range s.graphs {
		out = append(out, graphClone(*g))
	}
	slices.SortFunc(out, func(a, b WorkGraph) int { return strings.Compare(a.ID, b.ID) })
	return out
}
func validGraphKey(v string) bool {
	return len(v) > 0 && len(v) <= 128 && !strings.ContainsAny(v, "\x00\n\r")
}
func validateGraphLimits(l GraphLimits, now time.Time) error {
	if l.Tasks < 1 || l.Tasks > 512 || l.Amendments < 1 || l.Amendments > 2048 || l.Attempts < 1 || l.Attempts > 2048 || l.Retries < 0 || l.Retries > 16 || l.Expansions < 1 || l.Expansions > 256 || l.ControllerCalls < 1 || l.ControllerCalls > 256 || l.Depth < 1 || l.Depth > 32 || l.Fanout < 1 || l.Fanout > 128 || !l.Deadline.After(now) || l.Deadline.After(now.Add(30*24*time.Hour)) {
		return graphError("finite task, attempt, expansion, controller, depth, fanout and deadline limits are required")
	}
	return nil
}
func (e *Engine) resolveGraphSpec(g *WorkGraph, spec GraphSpec) (GraphSpec, *GraphExecutor, error) {
	if !validGraphKey(spec.ID) || len(spec.Criteria) == 0 || len(spec.Criteria) > 10000 || len(spec.Context) > graphContextLimit(g) {
		return spec, nil, graphError("task needs bounded identity, criteria and context")
	}
	if spec.Acceptance != "operator" && spec.Acceptance != "application" {
		return spec, nil, graphError("acceptance must be operator or application")
	}
	if spec.Kind == "scope" {
		if spec.Acceptance != "operator" || spec.Brief.AppID != "" || len(spec.Arguments) > 0 || len(spec.Brief.Scheduling.Deps) > 0 {
			return spec, nil, graphError("scope is a logical obligation, not an executor")
		}
		return spec, nil, nil
	}
	if spec.Kind != "application" {
		return spec, nil, graphError("unsupported adapter %q; registered adapter is application", spec.Kind)
	}
	if !slices.Contains(g.Policy.Applications, spec.Brief.AppID) {
		return spec, nil, graphError("application is outside graph policy")
	}
	if len(spec.Arguments) > 0 && !g.Policy.AllowArguments {
		return spec, nil, graphError("generated arguments are not authorized by policy")
	}
	if len(spec.Arguments) > 128 {
		return spec, nil, graphError("too many generated arguments")
	}
	for _, arg := range spec.Arguments {
		if len(arg) > 16000 || strings.ContainsRune(arg, 0) {
			return spec, nil, graphError("invalid invocation argument")
		}
	}
	spec.Brief.ProjectID = g.ProjectID
	spec.Brief.Scheduling.Deadline = &g.Limits.Deadline
	b := spec.Brief
	if b.Workspace != "" && b.Workspace != g.Workspace {
		return spec, nil, graphError("workspace must match the graph project")
	}
	b.Workspace = g.Workspace
	if err := b.Validate(); err != nil {
		return spec, nil, err
	}
	app, ok := e.App(b.AppID)
	if !ok {
		return spec, nil, graphError("application is no longer registered")
	}
	if app.Agent != nil {
		if !g.Policy.Automatic || len(spec.Arguments) != 0 {
			return spec, nil, graphError("native agents require automatic policy and cannot override invocation")
		}
		if g.Setup != nil {
			app.Agent.Model = g.Setup.Model
		}
		if err := specializeAgent(app.Agent, spec.Agent, g.ID); err != nil {
			return spec, nil, err
		}
		if app.Agent.Specialization != nil {
			app.Name = app.Agent.Specialization.Role
		}
		if app.Agent.Definition.Kind != "planner" {
			spec.Acceptance = "operator"
		}
		b.Ownership.Paths = []string{"."}
	}
	if !app.Builtin && app.Agent == nil {
		if _, err := compileApplication(app); err != nil {
			return spec, nil, err
		}
		if err := app.Acceptance.Validate(); err != nil {
			return spec, nil, err
		}
		if len(b.Ownership.Paths) == 0 {
			b.Ownership.Paths = []string{"."}
		}
	}
	spec.Brief = b
	if app.Builtin {
		for _, dep := range spec.Brief.Scheduling.Deps {
			if dep.Payload == "data" {
				return spec, nil, graphError("builtin inventory does not consume dependency artifacts")
			}
		}
	}
	if app.Supervision != nil {
		if err := app.Supervision.Validate(); err != nil {
			return spec, nil, err
		}
	}
	if app.Observation != nil {
		if err := app.Observation.Validate(); err != nil {
			return spec, nil, err
		}
	}
	if app.DecisionModel != nil {
		return spec, nil, graphError("per-run model hooks are not admitted in graphs; use graph decisions with shared accounting")
	}
	if len(spec.Arguments) > 0 {
		if app.Builtin {
			return spec, nil, graphError("builtin adapter cannot accept arguments")
		}
		app.Command = append(append([]string(nil), app.Command...), spec.Arguments...)
	}
	frozen := &GraphExecutor{Application: graphClone(app), Directory: app.Directory, Builtin: app.Builtin}
	frozen.Digest = graphDigest(frozen)
	return spec, frozen, nil
}
func (e *Engine) CreateGraph(req CreateGraph) (WorkGraph, error) {
	return e.createGraph(req, graphDigest(req))
}
func (e *Engine) createGraph(req CreateGraph, digest string) (WorkGraph, error) {
	if req.Profile != GraphProfile || !validGraphKey(req.Key) || req.ProjectID == "" || req.GoalRevision < 1 || !validGraphKey(req.Activation) {
		return WorkGraph{}, graphError("profile, request key, project, goal revision and activation are required")
	}
	// Check durable deduplication before time-sensitive or catalog validation.
	e.Store.mu.RLock()
	old, exists := e.Store.graphCreations[req.Key]
	e.Store.mu.RUnlock()
	if exists {
		if old.Digest != digest {
			return WorkGraph{}, ErrConflict
		}
		return e.Store.Graph(old.ID)
	}
	now := time.Now().UTC()
	if err := validateGraphLimits(req.Limits, now); err != nil {
		return WorkGraph{}, err
	}
	if req.Policy.Mode != "adaptive" && req.Policy.Mode != "defined" && req.Policy.Mode != "hybrid" {
		return WorkGraph{}, graphError("planning mode must be adaptive, defined or hybrid")
	}
	if len(req.Policy.Applications) > 64 {
		return WorkGraph{}, graphError("too many application grants")
	}
	for i, id := range req.Policy.Applications {
		if _, ok := e.App(id); !ok || slices.Contains(req.Policy.Applications[:i], id) {
			return WorkGraph{}, graphError("unregistered or duplicate application")
		}
	}
	if req.Policy.ControllerApplication != "" && !slices.Contains(req.Policy.Applications, req.Policy.ControllerApplication) {
		return WorkGraph{}, graphError("controller application must be granted")
	}
	if req.Policy.DecisionApplication != "" {
		app, ok := e.App(req.Policy.DecisionApplication)
		if !ok || app.DecisionModel == nil {
			return WorkGraph{}, graphError("decision application needs a registered decision-model configuration")
		}
	}
	project, err := e.Desk.Project(req.ProjectID)
	if err != nil {
		return WorkGraph{}, err
	}
	if len(req.Tasks) == 0 || len(req.Tasks) > req.Limits.Tasks {
		return WorkGraph{}, graphError("initial membership exceeds task allowance")
	}
	g := WorkGraph{Setup: req.setup, Profile: GraphProfile, ID: newID("graph"), Home: e.Store.Home().ID, ProjectID: req.ProjectID, Workspace: project.Directory, RootTask: "", GoalRevision: req.GoalRevision, Revision: 1, PolicyRevision: 1, ControllerGeneration: 1, Activation: req.Activation, Predecessor: req.Predecessor, Policy: req.Policy, Limits: req.Limits, Members: map[string]*GraphMember{}, Expansions: map[string]*GraphExpansion{}, Receipts: map[string]GraphReceipt{}, Decisions: map[string]*GraphDecision{}, Status: "active", CreatedAt: now, UpdatedAt: now}
	refs := map[string]string{}
	for _, spec := range req.Tasks {
		if !validGraphKey(spec.ID) || refs[spec.ID] != "" {
			return WorkGraph{}, graphError("unique task references are required")
		}
		refs[spec.ID] = newID("task")
	}
	for _, spec := range req.Tasks {
		spec = graphClone(spec)
		spec.ID = refs[spec.ID]
		if spec.Parent != "" {
			if refs[spec.Parent] == "" {
				return WorkGraph{}, graphError("unknown parent reference")
			}
			spec.Parent = refs[spec.Parent]
		} else {
			if g.RootTask != "" {
				return WorkGraph{}, graphError("exactly one root is required")
			}
			g.RootTask = spec.ID
		}
		for i := range spec.Brief.Scheduling.Deps {
			dep := &spec.Brief.Scheduling.Deps[i]
			if refs[dep.From] == "" {
				return WorkGraph{}, graphError("unknown dependency reference")
			}
			dep.From = refs[dep.From]
		}
		normalized, executor, err := e.resolveGraphSpec(&g, spec)
		if err != nil {
			return WorkGraph{}, err
		}
		g.Members[spec.ID] = &GraphMember{Spec: normalized, Executor: executor, Revision: 1, FactRevision: 1, State: "queued", Sealed: req.Seal}
	}
	if req.Policy.Mode != "defined" && !req.Seal {
		g.Expansions["initial"] = &GraphExpansion{ID: "initial", Scope: g.RootTask, Generation: 1, Deadline: g.Limits.Deadline}
	}
	if err := validateGraph(&g); err != nil {
		return WorkGraph{}, err
	}
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.graphCreations[req.Key]; ok {
		if old.Digest != digest {
			return WorkGraph{}, ErrConflict
		}
		return graphClone(*s.graphs[old.ID]), nil
	}
	if err := s.validateGraphPools(&g); err != nil {
		return WorkGraph{}, err
	}
	if len(s.graphs) >= 256 {
		return WorkGraph{}, graphError("graph capacity reached")
	}
	if req.Predecessor != "" {
		p := s.graphs[req.Predecessor]
		if p == nil || p.ProjectID != g.ProjectID || p.Status == "active" || p.GoalRevision >= g.GoalRevision {
			return WorkGraph{}, graphError("predecessor must be a terminal graph of an earlier goal revision")
		}
	}
	gc := graphChange{Graph: g, CreateKey: req.Key, CreateDigest: digest}
	s.projectGraph(&gc, nil)
	if err := s.commit("", "effect", "graph_created", change{Graph: &gc}); err != nil {
		return WorkGraph{}, err
	}
	return graphClone(*s.graphs[g.ID]), nil
}
func validateGraph(g *WorkGraph) error {
	if len(g.Members) == 0 || len(g.Members) > g.Limits.Tasks || g.Members[g.RootTask] == nil {
		return graphError("invalid bounded membership")
	}
	requests := map[string]workpool.Request{}
	children := map[string]int{}
	for id, m := range g.Members {
		if id != m.Spec.ID {
			return graphError("member identity mismatch")
		}
		request := m.Spec.Brief.Scheduling
		request.Normalize()
		requests[id] = request
		if id == g.RootTask {
			if m.Spec.Parent != "" || !m.Spec.Required {
				return graphError("root must be required without a parent")
			}
		} else {
			p := g.Members[m.Spec.Parent]
			if p == nil || p.Spec.Kind != "scope" {
				return graphError("parent must be a graph scope")
			}
			children[m.Spec.Parent]++
		}
		seen := map[string]bool{}
		cursor := id
		depth := 0
		for cursor != "" {
			if seen[cursor] {
				return graphError("containment cycle")
			}
			seen[cursor] = true
			parent := g.Members[cursor]
			if parent == nil {
				return graphError("unknown containment parent")
			}
			cursor = parent.Spec.Parent
			depth++
			if depth > g.Limits.Depth {
				return graphError("depth limit exceeded")
			}
		}
		for _, dep := range request.Deps {
			if g.Members[dep.From] == nil {
				return graphError("cross-graph dependencies require an import adapter")
			}
			if dep.Payload == "data" && g.Members[dep.From].Spec.Kind == "scope" {
				return graphError("logical scopes do not produce artifact inputs")
			}
			if dep.Guard != "done" && dep.Guard != "failed" && dep.Guard != "cancelled" && dep.Guard != "candidate_available" {
				return graphError("dependencies must declare accepted done, failed, cancelled or candidate_available guard")
			}
			if dep.Guard == "candidate_available" && m.Spec.Acceptance != "application" && !(g.Policy.Automatic && m.Executor != nil && m.Executor.Application.Agent != nil) {
				return graphError("candidate edges require an application verifier")
			}
		}
	}
	for _, count := range children {
		if count > g.Limits.Fanout {
			return graphError("fanout limit exceeded")
		}
	}
	if g.Members[g.RootTask].Spec.Kind != "scope" {
		return graphError("this adapter requires a logical root scope")
	}
	if err := workpool.Graph(requests); err != nil {
		return err
	}
	return nil
}
func (s *Store) projectGraph(gc *graphChange, previous *WorkGraph) {
	g := &gc.Graph
	for _, id := range graphIDs(g) {
		m := g.Members[id]
		var old *GraphMember
		if previous != nil {
			old = previous.Members[id]
		}
		if old != nil && reflect.DeepEqual(old, m) {
			continue
		}
		var t Task
		if prior := s.tasks[id]; prior != nil {
			t = cloneTask(prior)
		} else {
			t = Task{ID: id, Tenant: decision.LocalTenant, Source: "graph", CreatedAt: g.CreatedAt, ReadyAt: g.UpdatedAt}
		}
		t.GraphID = g.ID
		t.ProjectID = g.ProjectID
		t.GoalID = g.RootTask
		t.ParentTaskID = m.Spec.Parent
		t.Criteria = m.Spec.Criteria
		t.Revision = int(m.Revision)
		t.Brief = m.Spec.Brief
		t.Scheduling = m.Spec.Brief.Scheduling
		t.Scheduling.Normalize()
		t.UpdatedAt = g.UpdatedAt
		if m.Spec.Kind == "scope" {
			t.Status = "waiting"
		} else if m.State == "queued" {
			t.Status = "queued"
		}
		if m.Accepted != nil {
			t.Status = "done"
		}
		if m.CancelRequested && m.CurrentRunID == "" {
			t.Status = "cancelled"
		}
		t.CurrentRunID = m.CurrentRunID
		t.Runs = append([]string(nil), m.Attempts...)
		gc.Tasks = append(gc.Tasks, t)
	}
}
func (s *Store) applyGraph(e Event, c *graphChange) error {
	if c.Graph.ID == "" || c.Graph.Profile != GraphProfile {
		return errors.New("invalid graph journal event")
	}
	if s.graphs == nil {
		s.graphs = map[string]*WorkGraph{}
		s.graphCreations = map[string]graphCreation{}
	}
	g := c.Graph
	s.graphs[g.ID] = &g
	if c.CreateKey != "" {
		s.graphCreations[c.CreateKey] = graphCreation{g.ID, c.CreateDigest}
	}
	if s.tasks == nil {
		s.tasks = map[string]*Task{}
	}
	for _, t := range c.Tasks {
		t.Head = TaskHead{Seq: e.Seq, Hash: e.Hash}
		s.tasks[t.ID] = &t
		if r := s.runs[t.CurrentRunID]; r != nil {
			r.Events = append(r.Events, e)
		}
	}
	for _, r := range c.Runs {
		r.Events = append(r.Events, e)
		s.runs[r.ID] = &r
	}
	return nil
}
func (e *Engine) PreviewGraph(p GraphProposal) (WorkGraph, GraphReceipt, error) {
	return e.amendGraph(p, true, true)
}
func (e *Engine) AmendGraph(p GraphProposal) (WorkGraph, GraphReceipt, error) {
	return e.amendGraph(p, false, true)
}
func (e *Engine) amendGraph(p GraphProposal, preview, operator bool) (WorkGraph, GraphReceipt, error) {
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	g := s.graphs[p.GraphID]
	if g == nil {
		return WorkGraph{}, GraphReceipt{}, ErrNotFound
	}
	digest := graphDigest(p)
	if old, ok := g.Receipts[p.Key]; ok {
		if old.Digest != digest {
			return WorkGraph{}, GraphReceipt{}, ErrConflict
		}
		return graphClone(*g), graphClone(old), nil
	}
	next, receipt, err := e.proposeGraph(g, p, digest, operator)
	if err != nil {
		return WorkGraph{}, GraphReceipt{}, err
	}
	if preview {
		return next, receipt, nil
	}
	gc := graphChange{Graph: next}
	s.projectGraph(&gc, g)
	if err = s.commit("", "effect", "graph_amended", change{Graph: &gc}); err != nil {
		return WorkGraph{}, GraphReceipt{}, err
	}
	return graphClone(*s.graphs[g.ID]), receipt, nil
}
func (e *Engine) proposeGraph(prior *WorkGraph, p GraphProposal, digest string, operator bool) (WorkGraph, GraphReceipt, error) {
	fail := func(err error) (WorkGraph, GraphReceipt, error) { return WorkGraph{}, GraphReceipt{}, err }
	now := time.Now().UTC()
	if p.Profile != GraphProfile || !validGraphKey(p.Key) || len(p.Operations) < 1 || len(p.Operations) > 128 || len(p.Explanation) > 4000 {
		return fail(graphError("invalid proposal envelope"))
	}
	if p.Revision != prior.Revision || p.PolicyRevision != prior.PolicyRevision || p.Generation != prior.ControllerGeneration || !p.Deadline.After(now) || p.Deadline.After(prior.Limits.Deadline) || prior.Status != "active" {
		return fail(ErrConflict)
	}
	if len(prior.Receipts) >= prior.Limits.Amendments {
		return fail(graphError("amendment allowance exhausted"))
	}
	for id, read := range p.Reads {
		m := prior.Members[id]
		if m == nil || m.Revision != read.Specification || m.FactRevision != read.Fact {
			return fail(ErrConflict)
		}
	}
	g := graphClone(*prior)
	g.UpdatedAt = now
	refs := map[string]string{}
	for _, op := range p.Operations {
		if err := validateGraphOperation(op); err != nil {
			return fail(err)
		}
		if op.Kind == "add" || op.Kind == "supersede" {
			if op.Spec == nil || !validGraphKey(op.Spec.ID) || refs[op.Spec.ID] != "" || g.Members[op.Spec.ID] != nil {
				return fail(graphError("add requires a unique proposal-local task reference"))
			}
			refs[op.Spec.ID] = newID("task")
		}
	}
	resolve := func(id string) string {
		if actual := refs[id]; actual != "" {
			return actual
		}
		return id
	}
	pending := func(id string) error {
		m := g.Members[id]
		if m == nil {
			return ErrNotFound
		}
		if len(m.Attempts) > 0 || m.Accepted != nil || m.CancelRequested || m.Replacement != "" {
			return graphError("task specification has already been admitted or settled")
		}
		if id != g.RootTask && g.Members[m.Spec.Parent].Sealed {
			return graphError("parent scope is sealed")
		}
		return nil
	}
	for index, op := range p.Operations {
		if op.Kind == "accept" && index != len(p.Operations)-1 {
			return fail(graphError("acceptance must be the final operation"))
		}
		id := resolve(op.Task)
		m := g.Members[id]
		switch op.Kind {
		case "add", "supersede":
			spec := graphClone(*op.Spec)
			spec.ID = refs[spec.ID]
			spec.Parent = resolve(spec.Parent)
			for i := range spec.Brief.Scheduling.Deps {
				spec.Brief.Scheduling.Deps[i].From = resolve(spec.Brief.Scheduling.Deps[i].From)
			}
			parent := g.Members[spec.Parent]
			if parent == nil || parent.Sealed || parent.Accepted != nil {
				return fail(graphError("add requires an open parent scope"))
			}
			if op.Kind == "supersede" {
				if m == nil || id == g.RootTask || m.Accepted != nil || m.Replacement != "" || m.Spec.Kind == "scope" || spec.Parent != m.Spec.Parent || spec.Required != m.Spec.Required || spec.Criteria != m.Spec.Criteria || spec.Acceptance != m.Spec.Acceptance || op.Rationale == "" {
					return fail(graphError("supersession must retain the obligation, criteria and acceptance with rationale"))
				}
				m.Replacement = spec.ID
				m.CancelRequested = true
			}
			normalized, executor, err := e.resolveGraphSpec(&g, spec)
			if err != nil {
				return fail(err)
			}
			g.Members[spec.ID] = &GraphMember{Spec: normalized, Executor: executor, Revision: 1, FactRevision: 1, State: "queued"}
		case "amend":
			if err := pending(id); err != nil {
				return fail(err)
			}
			if op.Spec == nil {
				return fail(graphError("amend requires a full specification"))
			}
			spec := graphClone(*op.Spec)
			spec.ID = id
			if spec.Parent != m.Spec.Parent || spec.Required != m.Spec.Required || spec.Criteria != m.Spec.Criteria || spec.Acceptance != m.Spec.Acceptance || spec.Kind != m.Spec.Kind {
				return fail(graphError("ordinary amendments cannot weaken or replace obligations"))
			}
			normalized, executor, err := e.resolveGraphSpec(&g, spec)
			if err != nil {
				return fail(err)
			}
			m.History = append(m.History, m.Spec)
			m.Spec = normalized
			m.Executor = executor
			m.Revision++
		case "dependencies":
			if err := pending(id); err != nil {
				return fail(err)
			}
			m.History = append(m.History, m.Spec)
			m.Spec.Brief.Scheduling.Deps = append([]workpool.Dependency(nil), op.Dependencies...)
			for i := range m.Spec.Brief.Scheduling.Deps {
				m.Spec.Brief.Scheduling.Deps[i].From = resolve(m.Spec.Brief.Scheduling.Deps[i].From)
			}
			m.Spec.Brief.Scheduling.Join = op.Join
			m.Spec.Brief.Scheduling.Normalize()
			m.Revision++
		case "priority":
			if err := pending(id); err != nil {
				return fail(err)
			}
			m.History = append(m.History, m.Spec)
			m.Spec.Brief.Scheduling.Priority = op.Priority
			m.Revision++
		case "open_expansion":
			if m == nil || m.Spec.Kind != "scope" || m.Sealed || m.Accepted != nil || !validGraphKey(op.Expansion) || g.Expansions[op.Expansion] != nil || len(g.Expansions) >= g.Limits.Expansions {
				return fail(graphError("expansion requires an open scope and remaining allowance"))
			}
			g.Expansions[op.Expansion] = &GraphExpansion{ID: op.Expansion, Scope: id, Generation: g.ControllerGeneration, Deadline: p.Deadline}
		case "close_expansion":
			x := g.Expansions[op.Expansion]
			if x == nil || x.Outcome != "" || op.Rationale == "" {
				return fail(graphError("closing an expansion requires an unresolved obligation and rationale"))
			}
			if x.Generation != g.ControllerGeneration && !operator {
				return fail(ErrConflict)
			}
			x.Outcome = op.Rationale
		case "classify_trigger":
			found := false
			for i := range g.Triggers {
				tr := &g.Triggers[i]
				if tr.Seq == op.Trigger && tr.Outcome == "" {
					if op.Rationale == "" {
						return fail(graphError("trigger classification requires a rationale"))
					}
					tr.Outcome = op.Rationale
					found = true
				}
			}
			if !found {
				return fail(ErrConflict)
			}
		case "seal":
			if m == nil || m.Spec.Kind != "scope" || op.Cursor != g.Cursor {
				return fail(ErrConflict)
			}
			for _, x := range g.Expansions {
				if x.Outcome == "" {
					return fail(graphError("unresolved expansion prevents sealing"))
				}
			}
			for _, tr := range g.Triggers {
				if tr.Outcome == "" {
					return fail(graphError("unclassified trigger prevents sealing"))
				}
			}
			for _, d := range g.Decisions {
				if graphDecisionPending(d.Outcome) {
					return fail(graphError("unapplied decision prevents sealing"))
				}
			}
			m.Sealed = true
		case "reopen":
			if m == nil || m.Spec.Kind != "scope" || m.Accepted != nil {
				return fail(ErrConflict)
			}
			m.Sealed = false
		case "cancel":
			if m == nil || op.Rationale == "" {
				return fail(graphError("cancellation requires a task and rationale"))
			}
			for tid, child := range g.Members {
				if graphDescendant(&g, tid, id) && child.Accepted == nil {
					child.CancelRequested = true
				}
			}
			if id == g.RootTask {
				g.Status = "cancelled"
			}
		case "dismiss_decision":
			d := g.Decisions[op.Expansion]
			if !operator || d == nil || op.Rationale == "" || d.Outcome == "applied" {
				return fail(graphError("dismissal requires operator review of an unapplied decision"))
			}
			d.Outcome = "dismissed"
			d.Reason = op.Rationale
			if x := g.Expansions["decision:"+op.Expansion]; x != nil {
				x.Outcome = op.Rationale
			}
			if member := g.Members[d.TaskID]; member != nil {
				member.CancelRequested = true
			}
		case "replace_controller":
			if !operator || op.Rationale == "" {
				return fail(graphError("controller replacement requires operator authority and rationale"))
			}
			g.ControllerGeneration++
			for _, x := range g.Expansions {
				if x.Outcome == "" && !strings.HasPrefix(x.ID, "decision:") {
					x.Generation = g.ControllerGeneration
					if member := g.Members[x.ControllerTask]; member != nil {
						member.CancelRequested = true
					}
					x.ControllerTask = ""
					x.Error = ""
				}
			}
		case "retry":
			if m == nil || m.Spec.Kind != "application" || m.Accepted != nil || m.CancelRequested || m.Replacement != "" || len(m.Attempts) == 0 || len(m.Attempts) > g.Limits.Retries {
				return fail(graphError("task cannot retry or retry allowance exhausted"))
			}
			run := e.Store.runs[m.CurrentRunID]
			if run == nil || !run.Terminal() || run.Claim != nil && run.Claim.ReleasedAt == nil {
				return fail(graphError("retry requires settled prior execution"))
			}
			m.CurrentRunID = ""
			m.State = "queued"
			m.FactRevision++
		case "accept":
			if !operator || m == nil || op.Evidence == "" || op.Rationale == "" {
				return fail(graphError("acceptance requires operator authority and evidence"))
			}
			read, ok := p.Reads[id]
			if !ok || read.Fact != m.FactRevision || read.Specification != m.Revision {
				return fail(ErrConflict)
			}
			if m.Spec.Kind == "scope" {
				for tid, child := range g.Members {
					if graphDescendant(&g, tid, id) {
						read, exists := p.Reads[tid]
						if !exists || read.Specification != child.Revision || read.Fact != child.FactRevision {
							return fail(ErrConflict)
						}
					}
				}
			}
			if err := e.Store.acceptGraphMember(&g, id, op.Evidence, op.Rationale, now); err != nil {
				return fail(err)
			}
		default:
			return fail(graphError("unknown operation %q", op.Kind))
		}
	}
	if err := validateGraph(&g); err != nil {
		return fail(err)
	}
	if err := e.Store.validateGraphPools(&g); err != nil {
		return fail(err)
	}
	g.Revision++
	receipt := GraphReceipt{Key: p.Key, Digest: digest, Revision: g.Revision, IDs: refs, At: now}
	g.Receipts[p.Key] = receipt
	return g, receipt, nil
}
func graphDescendant(g *WorkGraph, id, parent string) bool {
	for id != "" {
		if id == parent {
			return true
		}
		m := g.Members[id]
		if m == nil {
			return false
		}
		id = m.Spec.Parent
	}
	return false
}
func (s *Store) graphSettled(m *GraphMember) bool {
	for _, id := range m.Attempts {
		r := s.runs[id]
		if r == nil || !r.Terminal() || r.Claim != nil && r.Claim.ReleasedAt == nil {
			return false
		}
	}
	return true
}
func (s *Store) acceptGraphMember(g *WorkGraph, id, evidence, rationale string, at time.Time) error {
	m := g.Members[id]
	if m.Accepted != nil || m.CancelRequested || m.Replacement != "" {
		return ErrConflict
	}
	if m.Spec.Kind == "scope" {
		if !m.Sealed {
			return graphError("scope is not sealed")
		}
		for _, x := range g.Expansions {
			if x.Outcome == "" {
				return graphError("expansion remains unresolved")
			}
		}
		for _, tr := range g.Triggers {
			if tr.Outcome == "" {
				return graphError("relevant event remains unclassified")
			}
		}
		for _, d := range g.Decisions {
			if graphDecisionPending(d.Outcome) {
				return graphError("decision remains unapplied")
			}
		}
		for tid, child := range g.Members {
			if tid == id || !graphDescendant(g, tid, id) {
				continue
			}
			if !s.graphSettled(child) {
				return graphError("child execution or claims remain unsettled")
			}
			if child.Spec.Required && child.Accepted == nil {
				if !graphObligationAccepted(g, child) {
					return graphError("required obligation %s is not accepted", tid)
				}
			}
			if child.Spec.Kind == "scope" && child.Spec.Required && !child.Sealed {
				return graphError("required child scope is open")
			}
		}
	} else {
		r := s.runs[m.CurrentRunID]
		if r == nil || !r.Terminal() || r.Status != "done" || !s.graphSettled(m) {
			return graphError("candidate execution must finish and settle before acceptance")
		}
	}
	manifest := graphDigest(struct {
		Members  map[string]*GraphMember
		Revision int64
		Policy   int64
	}{g.Members, g.Revision + 1, g.PolicyRevision})
	m.Accepted = &GraphAcceptance{SpecRevision: m.Revision, FactRevision: m.FactRevision, RunID: m.CurrentRunID, Evidence: evidence, Rationale: rationale, At: at, Manifest: manifest}
	m.State = "accepted"
	m.FactRevision++
	if id == g.RootTask {
		g.Status = "accepted"
	}
	return nil
}

func (s *Store) GraphBlockers(id string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	g := s.graphs[id]
	if g == nil {
		return nil, ErrNotFound
	}
	out := []string{}
	if g.Status != "active" {
		out = append(out, "graph:"+g.Status)
	}
	if !time.Now().Before(g.Limits.Deadline) {
		out = append(out, "deadline_expired")
	}
	for _, key := range graphIDs(g) {
		m := g.Members[key]
		if m.Spec.Required && m.Accepted == nil {
			out = append(out, "unaccepted:"+key)
		}
		if m.Spec.Kind == "scope" && !m.Sealed {
			out = append(out, "open_scope:"+key)
		}
		if !s.graphSettled(m) {
			out = append(out, "unsettled:"+key)
		}
	}
	for id, x := range g.Expansions {
		if x.Outcome == "" {
			out = append(out, "expansion:"+id)
			if x.Error != "" {
				out = append(out, "controller:"+x.Error)
			}
		}
	}
	for _, tr := range g.Triggers {
		if tr.Outcome == "" {
			out = append(out, fmt.Sprintf("trigger:%d", tr.Seq))
		}
	}
	slices.Sort(out)
	return out, nil
}

func graphObligationAccepted(g *WorkGraph, m *GraphMember) bool {
	seen := map[string]bool{}
	for m != nil {
		if m.Accepted != nil {
			return true
		}
		if seen[m.Spec.ID] {
			return false
		}
		seen[m.Spec.ID] = true
		m = g.Members[m.Replacement]
	}
	return false
}

func (s *Store) validateGraphPools(g *WorkGraph) error {
	for _, m := range g.Members {
		request := m.Spec.Brief.Scheduling
		request.Normalize()
		if _, ok := s.configuredPools()[request.Pool]; !ok {
			return graphError("unregistered scheduling pool")
		}
	}
	return nil
}
