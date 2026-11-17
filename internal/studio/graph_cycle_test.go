package studio

import (
	"strings"
	"testing"

	"mitos/internal/workpool"
)

func cyclicGraph(req CreateGraph) (CreateGraph, GraphSpec) {
	worker := graphWorker("child", "goal")
	worker.Brief.Scheduling.Deps = []workpool.Dependency{{From: "goal", Guard: "done", Payload: "control"}}
	cyclic := req
	cyclic.Key = "cyclic"
	cyclic.Tasks = append(append([]GraphSpec{}, req.Tasks...), worker)
	return cyclic, worker
}

// A required child whose scheduling waits for an ancestor scope's completion can
// never run: the ancestor cannot be accepted before the child is accepted. The
// graph must be refused before any graph, receipt or attempt is published.
func TestGraphRejectsRequiredAncestorDependencyCycle(t *testing.T) {
	e, req := graphEngine(t, false)
	cyclic, _ := cyclicGraph(req)
	if _, err := e.CreateGraph(cyclic); err == nil || !strings.Contains(err.Error(), "required dependency cycle") {
		t.Fatalf("cyclic graph admitted: %v", err)
	}
	e.Store.mu.RLock()
	graphs, tasks := len(e.Store.graphs), len(e.Store.tasks)
	e.Store.mu.RUnlock()
	if graphs != 0 || tasks != 0 {
		t.Fatalf("rejected creation published state: graphs %d tasks %d", graphs, tasks)
	}
	// Indirect cycles through nested scopes are equally impossible.
	nested := req
	nested.Key = "nested"
	mid := GraphSpec{ID: "mid", Parent: "goal", Kind: "scope", Required: true, Criteria: "Nested scope", Acceptance: "operator", Brief: Brief{Title: "Nested scope"}}
	deep := graphWorker("deep", "mid")
	deep.Brief.Scheduling.Deps = []workpool.Dependency{{From: "goal", Guard: "done", Payload: "control"}}
	nested.Tasks = append(append([]GraphSpec{}, req.Tasks...), mid, deep)
	if _, err := e.CreateGraph(nested); err == nil || !strings.Contains(err.Error(), "required dependency cycle") {
		t.Fatalf("nested cycle admitted: %v", err)
	}
	// A wait on the direct parent scope is the same impossibility.
	parentWait := req
	parentWait.Key = "parent-wait"
	direct := graphWorker("direct", "goal")
	direct.Brief.Scheduling.Deps = []workpool.Dependency{{From: "goal", Guard: "candidate_available", Payload: "control"}}
	direct.Acceptance = "application"
	parentWait.Tasks = append(append([]GraphSpec{}, req.Tasks...), direct)
	if _, err := e.CreateGraph(parentWait); err == nil || !strings.Contains(err.Error(), "required dependency cycle") {
		t.Fatalf("parent-wait cycle admitted: %v", err)
	}
}

// Optional work, cross-scope ordering and joins whose semantics do not require
// the ancestor to be accepted first stay valid.
func TestGraphKeepsValidOrderingWithoutRequiredCycles(t *testing.T) {
	e, req := graphEngine(t, false)
	valid := req
	valid.Key = "valid"
	mid := GraphSpec{ID: "mid", Parent: "goal", Kind: "scope", Required: true, Criteria: "Second scope", Acceptance: "operator", Brief: Brief{Title: "Second scope"}}
	first := graphWorker("first", "goal")
	first.Brief.Scheduling = workpool.Request{}
	ordered := graphWorker("ordered", "mid")
	ordered.Brief.Scheduling.Deps = []workpool.Dependency{{From: "first", Guard: "done", Payload: "control"}}
	joined := graphWorker("joined", "goal")
	joined.Required = false
	joined.Brief.Scheduling.Deps = []workpool.Dependency{{From: "goal", Guard: "done", Payload: "control"}, {From: "first", Guard: "done", Payload: "control"}}
	joined.Brief.Scheduling.Join = workpool.Join{Rule: "any"}
	recoverable := graphWorker("recoverable", "goal")
	recoverable.Brief.Scheduling.Deps = []workpool.Dependency{{From: "goal", Guard: "cancelled", Payload: "control"}}
	valid.Tasks = append(append([]GraphSpec{}, req.Tasks...), mid, first, ordered, joined, recoverable)
	g := createGraphTest(t, e, valid)
	if len(g.Members) != 6 {
		t.Fatalf("members: %d", len(g.Members))
	}
}

// Amendments and multi-operation proposals face the same refusal before any
// partial graph change, receipt or attempt is published.
func TestGraphAmendmentRejectsRequiredAncestorDependencyCycle(t *testing.T) {
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	blocked := graphWorker("blocked", g.RootTask)
	blocked.Brief.Scheduling.Deps = []workpool.Dependency{{From: g.RootTask, Guard: "done", Payload: "control"}}
	if _, _, err := e.AmendGraph(proposal(g, "cyclic-add", GraphOperation{Kind: "add", Spec: &blocked}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "Add cyclic work"})); err == nil || !strings.Contains(err.Error(), "required dependency cycle") {
		t.Fatalf("cyclic amendment admitted: %v", err)
	}
	current, err := e.Store.Graph(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != g.Revision || len(current.Members) != len(g.Members) || len(current.Receipts) != len(g.Receipts) {
		t.Fatalf("rejected amendment published partial changes: revision %d members %d receipts %d", current.Revision, len(current.Members), len(current.Receipts))
	}
	if current.Attempts != 0 {
		t.Fatalf("rejected amendment allocated attempts: %d", current.Attempts)
	}
}

// The refused wait stays available as optional scheduling: an optional task may
// wait on its ancestor because the ancestor's acceptance does not require it.
func TestGraphOptionalAncestorWaitStaysAdmissible(t *testing.T) {
	e, req := graphEngine(t, false)
	valid := req
	valid.Key = "explained"
	optional := graphWorker("optional", "goal")
	optional.Required = false
	optional.Brief.Scheduling.Deps = []workpool.Dependency{{From: "goal", Guard: "done", Payload: "control"}}
	valid.Tasks = append(append([]GraphSpec{}, req.Tasks...), optional)
	g := createGraphTest(t, e, valid)
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	found := false
	for _, m := range g.Members {
		if m.Spec.Kind == "application" && !m.Spec.Required {
			// Admitted, but its wait on the ancestor keeps it without attempts.
			found = len(m.Attempts) == 0 && m.CurrentRunID == "" && !e.Store.graphMemberReadiness(&g, m).Ready
		}
	}
	if !found {
		t.Fatal("optional ancestor wait was not admitted")
	}
}
