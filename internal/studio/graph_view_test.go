package studio

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGraphViewKeepsStatusesWithoutExecutionHistory(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Policy.Mode = "defined"
	spec := graphWorker("worker", "goal")
	spec.Brief.Intent = "selected assignment"
	spec.Context = "private executor context"
	req.Tasks = append(req.Tasks, spec)
	g := createGraphTest(t, e, req)
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var id string
	for tid, m := range g.Members {
		if m.Spec.Kind == "application" {
			id = tid
		}
	}
	summary, next := e.Store.GraphSummaries(req.ProjectID, "", 1)
	if len(summary) != 1 || next != "" || summary[0].Counts["queued"] != 1 {
		t.Fatalf("summary: %+v", summary)
	}
	unselected, err := e.Store.GraphView(g.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(unselected)
	if strings.Contains(string(b), "private executor context") || strings.Contains(string(b), "selected assignment") {
		t.Fatal("poll leaked unselected details")
	}
	selected, err := e.Store.GraphView(g.ID, id)
	if err != nil || selected.Graph.Members[id].Spec.Brief.Intent != "selected assignment" || selected.Graph.Members[id].Executor != nil {
		t.Fatal("selected projection incomplete")
	}
	selected.Graph.Members[id].Spec.Brief.Intent = "mutated"
	canonical, _ := e.Store.Graph(g.ID)
	if canonical.Members[id].Spec.Brief.Intent != "selected assignment" {
		t.Fatal("read mutated state")
	}
	runID := g.Members[id].CurrentRunID
	run, err := e.Store.GraphRun(g.ID, id, runID)
	if err != nil || run.Events != nil || run.GraphAttempt != nil || run.Execution != nil {
		t.Fatal("run projection included execution history")
	}
	if unselected.Nodes[id].Summary != run.Summary || run.Summary == "" {
		t.Fatal("unselected attempt explanation missing from snapshot")
	}
	if _, err = e.Store.GraphRun(g.ID, g.RootTask, runID); err != ErrNotFound {
		t.Fatal("wrong task binding read")
	}
	if err = e.Store.Complete(runID, "evidence", true); err != nil {
		t.Fatal(err)
	}
	view, _ := e.Store.GraphView(g.ID, id)
	if view.Nodes[id].State != "accepted" || !view.Nodes[id].Settled {
		t.Fatal("finished state not projected")
	}
}
func TestGraphPlannerDiscoveryIsExplicit(t *testing.T) {
	e, _ := graphEngine(t, false)
	app := executionApp(t, "true")
	app.GraphController = true
	e.Apps = append(e.Apps, app)
	got := e.GraphApplications()
	found := false
	for _, a := range got {
		if a["id"] == app.ID {
			found = a["controller"] == true
		}
		if a["id"] == "workspace-inventory" && a["controller"] != false {
			t.Fatal("inventory advertised as planner")
		}
	}
	if !found {
		t.Fatal("registered planner undiscoverable")
	}
}
