package studio

import (
	"encoding/json"
	"testing"
)

func TestGraphReadPagesDetailForOrchestratorsOnly(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Policy.OrchestratorApplication = "workspace-inventory"
	g := createGraphTest(t, e, req)
	spec := graphWorker("helper", g.RootTask)
	spec.Acceptance = "operator"
	g, _ = amendGraphTest(t, e, g, "helper", GraphOperation{Kind: "add", Spec: &spec})
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var orchestratorRun, helperRun string
	for _, m := range g.Members {
		if m.CurrentRunID == "" {
			continue
		}
		if m.OrchestratorFor != "" {
			orchestratorRun = m.CurrentRunID
			if err := e.Store.Transition(orchestratorRun, "fixture_started", "running", "Running", "fixture"); err != nil {
				t.Fatal(err)
			}
		} else if m.Spec.Kind == "application" {
			helperRun = m.CurrentRunID
			if err := e.Store.Transition(helperRun, "fixture_started", "running", "Running", "fixture"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if orchestratorRun == "" || helperRun == "" {
		t.Fatalf("orchestrator %q helper %q", orchestratorRun, helperRun)
	}
	raw, _ := json.Marshal(GraphTaskRead{Cursor: 0, Limit: 2})
	pageAny, err := e.handoffCall(orchestratorRun, "", "graph_read", raw)
	if err != nil {
		t.Fatal(err)
	}
	page, _ := json.Marshal(pageAny)
	if len(page) == 0 || !json.Valid(page) {
		t.Fatal("empty graph_read page")
	}
	var detail struct {
		Tasks []GraphTaskDetail `json:"tasks"`
	}
	if json.Unmarshal(page, &detail) != nil || len(detail.Tasks) != 2 {
		t.Fatalf("expected two tasks, got %s", page)
	}
	if detail.Tasks[0].Spec.Criteria == "" || detail.Tasks[0].Spec.Context != "" || detail.Tasks[0].State == "" {
		t.Fatalf("detail incomplete: %+v", detail.Tasks[0])
	}
	// A worker attempt cannot page graph detail.
	if _, err = e.handoffCall(helperRun, "", "graph_read", raw); err == nil {
		t.Fatal("worker paged graph detail")
	}
	single, _ := json.Marshal(GraphTaskRead{Task: detail.Tasks[0].Task})
	one, err := e.handoffCall(orchestratorRun, "", "graph_read", single)
	if err != nil {
		t.Fatal(err)
	}
	oneJSON, _ := json.Marshal(one)
	var only struct {
		Tasks []GraphTaskDetail `json:"tasks"`
	}
	if json.Unmarshal(oneJSON, &only) != nil || len(only.Tasks) != 1 || only.Tasks[0].Task != detail.Tasks[0].Task {
		t.Fatalf("single task read failed: %s", oneJSON)
	}
	missing, _ := json.Marshal(GraphTaskRead{Task: "task_missing"})
	if _, err = e.handoffCall(orchestratorRun, "", "graph_read", missing); err == nil {
		t.Fatal("unknown task read succeeded")
	}
}
