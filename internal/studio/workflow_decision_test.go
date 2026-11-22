package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/decisionmodel"
)

func workflowTestEngine(t *testing.T) (*Engine, CreateGraph) {
	t.Helper()
	e, req := graphEngine(t, true)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Questions map[string]struct{ Criteria map[string]string }
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		choices := body.Questions["action"].Criteria
		probs := map[string]float64{}
		selected := "apply"
		if _, ok := choices["repair"]; ok {
			selected = "repair"
		}
		for key := range choices {
			probs[key] = 0
		}
		probs[selected] = 1
		json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "answers": map[string]any{"action": map[string]any{"type": "choice", "choice": selected, "confidence": 1, "probabilities": probs}}})
	}))
	t.Cleanup(server.Close)
	if _, err := e.SaveSystemConfiguration(SystemConfiguration{DecisionProvider: modelConfig(server.URL, "before_segment")}); err != nil {
		t.Fatal(err)
	}
	return e, req
}

func TestWorkflowDecisionFallsBackToOperator(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Tasks = append(req.Tasks, graphWorker("worker", "goal"))
	g := createGraphTest(t, e, req)
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var r Run
	for _, m := range g.Members {
		if m.Spec.Kind == "application" {
			r, _ = e.Store.Get(m.CurrentRunID)
		}
	}
	if err := e.Store.Transition(r.ID, "agent_started", "running", "Working", "Started"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		selection, err := e.chooseWorkflowAction(ctx, g, r, "workspace_capacity", map[string]int{"files": 12000}, []decisionmodel.Choice{{Value: "expand", Description: "Allocate a larger workspace."}, {Value: "stop", Description: "Keep the existing limit."}})
		if err == nil && selection.Choice != "expand" {
			t.Errorf("wrong choice: %+v", selection)
		}
		done <- err
	}()
	for ctx.Err() == nil {
		items := e.Store.Attention()
		if len(items) == 1 {
			card := items[0].Card
			if items[0].OwnerRunID != r.ID || !card.HasOption("expand") {
				t.Fatal(items)
			}
			if _, err := e.ResolveAs(card.RunID, card.ID, "expand", "", "cli", decision.LocalOperator()); err != nil {
				t.Fatal(err)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(e.Store.Attention()) != 0 {
		t.Fatal("settled review still needs attention")
	}
}

func TestRegisteredOrchestratorHonorsDecisionToStop(t *testing.T) {
	e, req := graphEngine(t, true)
	var calls atomic.Int32
	provider := choiceServer(t, func() string { return "stop" }, &calls)
	defer provider.Close()
	if _, err := e.SaveSystemConfiguration(SystemConfiguration{DecisionProvider: modelConfig(provider.URL, "before_segment")}); err != nil {
		t.Fatal(err)
	}
	app := executionApp(t, `printf '{"explanation":"Wait for additional evidence"}' > graph-proposal.json
printf 'proposed' > result.txt`)
	app.GraphOrchestrator = true
	e.Apps = append(e.Apps, app)
	req.Policy.Applications = append(req.Policy.Applications, app.ID)
	req.Policy.OrchestratorApplication = app.ID
	g := createGraphTest(t, e, req)
	for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); {
		g, _ = e.Store.Graph(g.ID)
		x := g.Expansions["initial"]
		if x.Error != "" {
			if calls.Load() != 1 || x.Outcome != "" || x.Failures != 0 || len(g.Receipts) != 0 {
				t.Fatalf("stopped proposal had effects: calls %d expansion %+v receipts %+v", calls.Load(), x, g.Receipts)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("planner's stop decision was not recorded")
}

func TestStandaloneWorkspaceDecisionUsesInheritedProvider(t *testing.T) {
	e, req := graphEngine(t, true)
	var calls atomic.Int32
	provider := choiceServer(t, func() string { return "stop" }, &calls)
	defer provider.Close()
	if _, err := e.SaveSystemConfiguration(SystemConfiguration{DecisionProvider: modelConfig(provider.URL, "before_segment")}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(e.Root, "large")
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, 65<<20); err != nil {
		t.Fatal(err)
	}
	brief := testBrief(e.Root)
	brief.ProjectID = req.ProjectID
	r, err := e.Launch(brief, "")
	if err != nil {
		t.Fatal(err)
	}
	r = waitRun(t, e.Store, r.ID, func(r Run) bool { return r.Terminal() })
	if r.Status != "failed" || r.Workspace != nil || calls.Load() != 1 {
		t.Fatal(r.Status, r.Workspace, calls.Load())
	}
}
