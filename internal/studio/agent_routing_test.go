package studio

import (
	"context"
	"encoding/json"
	"mitos/internal/agentpolicy"
	"mitos/internal/decision"
	"mitos/internal/decisionmodel"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

const routingQuestion = `{"id":1,"method":"item/tool/requestUserInput","params":{"threadId":"t","turnId":"u","questions":[{"id":"q","question":"Which approach fits the supplied evidence?","options":[{"label":"Alpha","description":"Use Alpha"},{"label":"Beta","description":"Use Beta"}]}]}}`

func choiceServer(t *testing.T, selectChoice func() string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var body struct {
			Questions map[string]struct {
				Criteria map[string]string `json:"criteria"`
			} `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		choice := selectChoice()
		probs := map[string]float64{}
		for k := range body.Questions["action"].Criteria {
			probs[k] = 0
		}
		probs[choice] = 1
		json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "answers": map[string]any{"action": map[string]any{"type": "choice", "choice": choice, "confidence": 1, "probabilities": probs}}})
	}))
}
func TestAgentRouteClassificationIsDurableAndDoesNotAnswer(t *testing.T) {
	var calls atomic.Int32
	srv := choiceServer(t, func() string { return "decision" }, &calls)
	defer srv.Close()
	e, req := graphEngine(t, false)
	e.Apps = append(e.Apps, Application{ID: "classifier", DecisionModel: modelConfig(srv.URL, "before_segment")})
	p := agentpolicy.Default()
	p.Autonomy = "delegated"
	p.DecisionApplication = "classifier"
	if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
		t.Fatal(err)
	}
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Routing", e.Root, req.ProjectID, "", "", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	request, err := e.Store.IngestAgentRequest(session.ID, token, json.RawMessage(routingQuestion))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		route, err := e.RouteAgentRequest(context.Background(), session.ID, token, request.RequestID)
		if err != nil || route.Route.Resolver != "decision" || route.Route.Source != "classifier" {
			t.Fatal(route, err)
		}
	}
	if calls.Load() != 1 {
		t.Fatal("duplicate inference", calls.Load())
	}
	status, err := e.Store.AgentRequestStatus(session.ID, token, request.RequestID)
	if err != nil || status.State != "pending" {
		t.Fatal("classification resolved approval", status, err)
	}
}
func TestModelEvaluationUnknownConsumptionAndBudget(t *testing.T) {
	var calls atomic.Int32
	srv := choiceServer(t, func() string { return "yes" }, &calls)
	defer srv.Close()
	e, _ := graphEngine(t, false)
	cfg := *modelConfig(srv.URL, "before_segment")
	request := decisionmodel.Request{State: json.RawMessage(`{}`), Choices: []decisionmodel.Choice{{Value: "yes", Description: "Yes"}, {Value: "no", Description: "No"}}}
	if _, err := e.evaluateModel(context.Background(), "project", "scope", "one", 1, 1, cfg, request); err != nil {
		t.Fatal(err)
	}
	if _, err := e.evaluateModel(context.Background(), "project", "scope", "two", 2, 1, cfg, request); err == nil {
		t.Fatal("budget reset by policy revision")
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
	e.Store.mu.Lock()
	v := ModelEvaluation{ID: graphDigest([]any{"other", "route", 1}), Scope: "other", Status: "pending", Config: cfg, Request: request}
	err := e.Store.commit("", "effect", "model_evaluation_requested", change{ModelEvaluation: &v})
	e.Store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Store.recoverModelEvaluations(); err != nil {
		t.Fatal(err)
	}
	if _, err = e.evaluateModel(context.Background(), "project", "other", "route", 1, 4, cfg, request); err == nil {
		t.Fatal("unknown consumption resent")
	}
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}

func TestAgentRouteRejectsPolicyChangeDuringClassification(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "answers": map[string]any{"action": map[string]any{"type": "choice", "choice": "decision", "confidence": 1, "probabilities": map[string]float64{"decision": 1, "human": 0}}}})
	}))
	defer srv.Close()
	e, req := graphEngine(t, false)
	e.Apps = append(e.Apps, Application{ID: "classifier", DecisionModel: modelConfig(srv.URL, "before_segment")})
	p := agentpolicy.Default()
	p.Autonomy = "delegated"
	p.DecisionApplication = "classifier"
	p, err := e.SaveAgentPolicy(req.ProjectID, p)
	if err != nil {
		t.Fatal(err)
	}
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Routing", e.Root, req.ProjectID, "", "", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	request, err := e.Store.IngestAgentRequest(session.ID, token, json.RawMessage(routingQuestion))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := e.RouteAgentRequest(context.Background(), session.ID, token, request.RequestID)
		done <- err
	}()
	<-entered
	p.Autonomy = "manual"
	if _, err = e.SaveAgentPolicy(req.ProjectID, p); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err = <-done; err != ErrConflict {
		t.Fatal("stale route applied", err)
	}
	route, err := e.RouteAgentRequest(context.Background(), session.ID, token, request.RequestID)
	if err != nil || route.Route.Resolver != "human" {
		t.Fatal(route, err)
	}
}
