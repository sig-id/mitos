package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGraphTypedDecisionSelectionAndIdempotency(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"fixture","answers":{"action":{"type":"choice","choice":"0","confidence":0.99,"probabilities":{"0":0.99,"1":0.01}}}}`))
	}))
	defer server.Close()
	e, req := graphEngine(t, false)
	app := executionApp(t, "true")
	app.ID = "routing-model"
	app.DecisionModel = modelConfig(server.URL, "before_segment")
	e.Apps = append(e.Apps, app)
	req.Policy.DecisionApplication = app.ID
	g := createGraphTest(t, e, req)
	spec := graphWorker("new-worker", g.RootTask)
	p := proposal(g, "choose")
	p.Operations = nil
	request := GraphDecisionRequest{Proposal: p, Context: "Choose a useful next investigation", Candidates: []OrchestratorPlan{{Explanation: "Inspect the workspace", Operations: []GraphOperation{{Kind: "add", Spec: &spec}}}, {Explanation: "Need more information", Operations: []GraphOperation{{Kind: "reopen", Task: g.RootTask}}}}}
	d, err := e.RequestGraphDecision(request)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Proposal.Deadline.Equal(request.Proposal.Deadline) {
		t.Fatal("provider timeout shortened the graph amendment deadline")
	}
	duplicate, err := e.RequestGraphDecision(request)
	if err != nil || duplicate.RunID != d.RunID {
		t.Fatal("decision request not idempotent")
	}
	if ok, err := e.Store.Schedule(d.RunID, nil); err != nil || !ok {
		t.Fatalf("decision not scheduled: %v", err)
	}
	run, _ := e.Store.Get(d.RunID)
	if err = e.executeGraphDecision(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	if g.Decisions[d.Key].Outcome != "selected" {
		t.Fatalf("decision failed: %+v", g.Decisions[d.Key])
	}
	receipt, err := e.ApplyGraphDecision(g.ID, d.Key)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.ApplyGraphDecision(g.ID, d.Key)
	if err != nil || !reflect.DeepEqual(receipt, again) {
		t.Fatal("choice applied twice")
	}
	if calls.Load() != 1 || receipt.IDs["new-worker"] == "" {
		t.Fatal("choice not bound to supplied amendment")
	}
	g, _ = e.Store.Graph(g.ID)
	if g.OrchestratorCalls != 1 || g.Attempts != 1 {
		t.Fatal("inference not counted against responsibility")
	}
}
func TestGraphPendingInferenceRecoveryDoesNotCallProvider(t *testing.T) {
	e, req := graphEngine(t, false)
	app := executionApp(t, "true")
	app.ID = "routing-model"
	app.DecisionModel = modelConfig("http://127.0.0.1:1", "before_segment")
	e.Apps = append(e.Apps, app)
	req.Policy.DecisionApplication = app.ID
	g := createGraphTest(t, e, req)
	p := proposal(g, "unknown")
	p.Operations = nil
	request := GraphDecisionRequest{Proposal: p, Candidates: []OrchestratorPlan{{Explanation: "Wait for context", Operations: []GraphOperation{{Kind: "reopen", Task: g.RootTask}}}, {Explanation: "Wait for evidence", Operations: []GraphOperation{{Kind: "reopen", Task: g.RootTask}}}}}
	d, err := e.RequestGraphDecision(request)
	if err != nil {
		t.Fatal(err)
	}
	e.Store.mu.Lock()
	copy := graphClone(*e.Store.graphs[g.ID])
	copy.Decisions[d.Key].Outcome = "pending"
	err = e.Store.commit("", "effect", "test_requested", change{Graph: &graphChange{Graph: copy}})
	e.Store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Store.recoverGraphDecisions(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	if g.Decisions[d.Key].Outcome != "rejected" || g.Decisions[d.Key].Reason != "interrupted_unknown_consumption" || g.OrchestratorCalls != 1 {
		t.Fatal("pending inference was replayed or its charge reset")
	}
}
func TestGraphOrchestratorDynamicallyAddsWorkFromNativeEvidence(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 fixture unavailable")
	}
	e, req := graphEngine(t, false)
	orchestrator := executionApp(t, "unused")
	orchestrator.ID = "orchestrator"
	orchestrator.Command = []string{python, "-c", `import json,pathlib,sys
request=json.load(sys.stdin)
ctx=json.loads(request['graph']['context'])
ops=[]
if ctx['expansion']=='initial':
 ops=[{'kind':'add','spec':{'id':'discovered','parent':ctx['root_task'],'kind':'application','required':True,'criteria':'Inventory verified','acceptance':'application','brief':{'title':'Discover the workspace','app_id':'workspace-inventory','timeout_minutes':1}}}]
pathlib.Path('graph-proposal.json').write_text(json.dumps({'operations':ops,'explanation':'Investigated the available facts'}))
pathlib.Path('result.txt').write_text('proposal written')
print('# Native orchestrator analysis\nNo worker response tags required.')`}
	e.Apps = append(e.Apps, orchestrator)
	req.Policy.Applications = append(req.Policy.Applications, orchestrator.ID)
	req.Policy.OrchestratorApplication = orchestrator.ID
	g := createGraphTest(t, e, req)
	// Run each admitted command through the real executor, keeping the fixture
	// deterministic while exercising the same reconciliation/admission handlers.
	for iteration := 0; iteration < 8; iteration++ {
		if err = e.reconcileGraphs(); err != nil {
			t.Fatal(err)
		}
		ready, err := e.Store.ReadyRuns(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range ready {
			ok, err := e.Store.Schedule(candidate.Run.ID, nil)
			if err != nil || !ok {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			e.execute(ctx, candidate.Run)
			cancel()
		}
		g, _ = e.Store.Graph(g.ID)
		if len(g.Expansions) > 1 {
			allClosed := true
			for _, x := range g.Expansions {
				if x.Outcome == "" {
					allClosed = false
				}
			}
			if allClosed {
				break
			}
		}
	}
	g, _ = e.Store.Graph(g.ID)
	workers, orchestrators := 0, 0
	for _, m := range g.Members {
		if m.Spec.Kind != "application" {
			continue
		}
		if m.OrchestratorFor != "" {
			orchestrators++
			out, err := e.Store.Artifact(m.CurrentRunID, "output.txt")
			if err != nil || !strings.Contains(string(out), "Native orchestrator analysis") {
				t.Fatal("native output lost")
			}
		} else {
			workers++
			if m.Accepted == nil {
				t.Fatal("discovered worker was not accepted")
			}
		}
	}
	if workers != 1 || orchestrators != 2 {
		data, _ := json.Marshal(g)
		t.Fatalf("orchestrator did not perform bounded dynamic expansion: workers=%d orchestrators=%d %s", workers, orchestrators, data)
	}
	for _, x := range g.Expansions {
		if x.Outcome == "" {
			t.Fatalf("unsettled expansion %+v", x)
		}
	}
	if g.Status == "accepted" {
		t.Fatal("orchestrator narrative accepted the root")
	}
}
