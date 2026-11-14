package studio

import (
	"encoding/json"
	"strings"
	"testing"

	"mitos/internal/workpool"
)

func TestOrchestratorContractRejectsOldShorthandAndUnknownFields(t *testing.T) {
	for _, body := range []string{
		`{"steps":[]}`, `{"retry":[]}`, `{"operations":[{"kind":"add","spec":{"after":["a"]}}]}`,
		`{"operations":[{"kind":"add","spec":{"brief":{"scheduling":{"joim":{"rule":"any"}}}}}]}`,
	} {
		var plan OrchestratorPlan
		if DecodeGraphJSON([]byte(body), &plan) == nil {
			t.Fatalf("accepted invalid contract: %s", body)
		}
	}
	var schema map[string]any
	if err := json.Unmarshal(OrchestratorSchema(), &schema); err != nil {
		t.Fatal(err)
	}
	props := schema["properties"].(map[string]any)
	if props["operations"] == nil || props["steps"] != nil || props["retry"] != nil {
		t.Fatal("native schema diverges from shared plan")
	}
	encoded := string(OrchestratorSchema())
	for _, field := range []string{`"deps"`, `"join"`, `"guard"`, `"inbox"`, `"after_sequence"`} {
		if !strings.Contains(encoded, field) {
			t.Fatalf("native schema omits %s", field)
		}
	}
}

func TestSharedOrchestratorPlanPreservesScheduling(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		t.Run(map[bool]string{false: "registered", true: "native-policy"}[automatic], func(t *testing.T) {
			e, req := graphEngine(t, false)
			app := executionApp(t, "printf result > result.txt")
			app.GraphOrchestrator = true
			e.Apps = append(e.Apps, app)
			req.Policy.Applications = append(req.Policy.Applications, app.ID)
			req.Policy.OrchestratorApplication = app.ID
			req.Policy.Automatic = automatic
			g := createGraphTest(t, e, req)
			if err := e.reconcileGraphOrchestrators(); err != nil {
				t.Fatal(err)
			}
			g, _ = e.Store.Graph(g.ID)
			x := g.Expansions["initial"]
			a, b, consumer := graphWorker("a", g.RootTask), graphWorker("b", g.RootTask), graphWorker("consumer", g.RootTask)
			inbox := signalDependency(g)
			inbox.Payload = "control"
			consumer.Brief.Scheduling = workpool.Request{Join: workpool.Join{Rule: "k_of_n", K: 2}, Deps: []workpool.Dependency{{From: "a", Guard: "failed"}, {From: "b", Guard: "candidate_available"}, inbox}}
			plan := OrchestratorPlan{Explanation: "Run after two matching outcomes or signals", Operations: []GraphOperation{{Kind: "add", Spec: &a}, {Kind: "add", Spec: &b}, {Kind: "add", Spec: &consumer}}}
			body, _ := json.Marshal(plan)
			var decoded OrchestratorPlan
			if err := DecodeGraphJSON(body, &decoded); err != nil {
				t.Fatal(err)
			}
			run := Run{ID: "proposal", TaskID: x.OrchestratorTask}
			if err := e.applyOrchestratorPlan(g, "initial", run, decoded); err != nil {
				t.Fatal(err)
			}
			next, _ := e.Store.Graph(g.ID)
			ids := next.Receipts["orchestrator:proposal"].IDs
			schedule := next.Members[ids["consumer"]].Spec.Brief.Scheduling
			if schedule.Join.Rule != "k_of_n" || schedule.Join.K != 2 || schedule.Deps[0].Guard != "failed" || schedule.Deps[1].Guard != "candidate_available" || schedule.Deps[2].Inbox == nil || schedule.Deps[0].From != ids["a"] || schedule.Deps[2].From != "release" {
				t.Fatalf("scheduling contract changed during application: %+v", schedule)
			}
		})
	}
}

func TestStoredGraphOrchestratorMigration(t *testing.T) {
	raw := `{"profile":"work-graph/v1","controller_generation":3,"controller_calls":7,"limits":{"controller_calls":24},"policy":{"controller_application":"agent:mitos.planner"},"members":{"x":{"controller_for":"initial"}},"expansions":{"initial":{"controller_task":"x"}}}`
	var g WorkGraph
	if err := DecodeGraphJSON([]byte(raw), &g); err != nil {
		t.Fatal(err)
	}
	if g.OrchestratorGeneration != 3 || g.OrchestratorCalls != 7 || g.Limits.OrchestratorCalls != 24 || g.Policy.OrchestratorApplication == "" || g.Members["x"].OrchestratorFor != "initial" || g.Expansions["initial"].OrchestratorTask != "x" {
		t.Fatal("lost historical orchestration state", g)
	}
	out, _ := json.Marshal(g)
	for _, field := range []string{`"controller_generation"`, `"controller_calls"`, `"controller_for"`, `"controller_task"`, `"controller_application"`} {
		if strings.Contains(string(out), field) {
			t.Fatal("legacy field emitted", field)
		}
	}
	for _, body := range []string{`{"controller_calls":1,"orchestrator_calls":2}`, `{"controller_calls":1,"typo":2}`} {
		if DecodeGraphJSON([]byte(body), &g) == nil {
			t.Fatal("ambiguous or unknown fields accepted", body)
		}
	}
}
