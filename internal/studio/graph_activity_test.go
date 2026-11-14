package studio

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mitos/internal/agentpolicy"
	"mitos/internal/decision"
)

func TestGraphActivityJoinsNativeRequestsToTheirAttempt(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Policy.Mode = "defined"
	req.Tasks = append(req.Tasks, graphWorker("worker", "goal"))
	g := createGraphTest(t, e, req)
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var task, run string
	for id, member := range g.Members {
		if member.Spec.Kind == "application" {
			task, run = id, member.CurrentRunID
		}
	}
	if err := e.Store.Transition(run, "agent_started", "running", "Agent working", "Started execution."); err != nil {
		t.Fatal(err)
	}
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Worker", e.Root, req.ProjectID, "", run, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	request, err := e.Store.IngestAgentRequest(session.ID, token, json.RawMessage(codexApproval))
	if err != nil {
		t.Fatal(err)
	}
	view, err := e.Store.GraphView(g.ID, task)
	if err != nil || view.Nodes[task].State != "waiting_input" || view.Nodes[task].PendingRequests != 1 {
		t.Fatalf("hidden waiting request: %+v %v", view.Nodes[task], err)
	}
	if len(view.Graph.Members[task].Attempts) != 1 {
		t.Fatal("request incorrectly became another attempt")
	}
	activity, err := e.Store.GraphActivity(g.ID, task, run)
	if err != nil || len(activity.Requests) != 1 || activity.StartedAt == nil || len(activity.Events) == 0 {
		t.Fatalf("incomplete activity: %+v %v", activity, err)
	}
	if activity.Requests[0].Decision.RunID != request.RunIDs[0] || !activity.Requests[0].Blocking {
		t.Fatal("request lost its resolution binding")
	}
	activity.Requests[0].Decision.Options[0].Label = "mutated"
	activity, _ = e.Store.GraphActivity(g.ID, task, run)
	if activity.Requests[0].Decision.Options[0].Label == "mutated" {
		t.Fatal("read mutated canonical decision")
	}
	for _, binding := range [][2]string{{"other-graph", task}, {g.ID, g.RootTask}} {
		if _, err := e.Store.GraphActivity(binding[0], binding[1], run); err != ErrNotFound {
			t.Fatal("cross-binding read", err)
		}
	}
	d := activity.Requests[0].Decision
	if err := e.Store.Resolve(d.RunID, d.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	view, _ = e.Store.GraphView(g.ID, task)
	if view.Nodes[task].State != "resuming" {
		t.Fatal("recorded answer misreported as delivered", view.Nodes[task])
	}
	answer, err := e.Store.AgentRequestStatus(session.ID, token, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Store.AgentDelivery(session.ID, token, request.RequestID, answer.Digest, "applied"); err != nil {
		t.Fatal(err)
	}
	view, _ = e.Store.GraphView(g.ID, task)
	if view.Nodes[task].State != "running" || view.Nodes[task].PendingRequests != 0 {
		t.Fatal("delivered answer still blocks agent", view.Nodes[task])
	}
	async := json.RawMessage(`{"id":11,"method":"mitos/asyncQuestion","params":{"threadId":"thread_1","turnId":"turn_1","questions":[{"id":"q","question":"Which name?"}]}}`)
	if _, err := e.Store.IngestAgentRequest(session.ID, token, async); err != nil {
		t.Fatal(err)
	}
	view, _ = e.Store.GraphView(g.ID, task)
	if view.Nodes[task].State != "running" || view.Nodes[task].PendingRequests != 1 {
		t.Fatal("async question falsely blocked execution", view.Nodes[task])
	}
	if err := e.Store.CloseAgentSession(session.ID, token); err != nil {
		t.Fatal(err)
	}
	activity, _ = e.Store.GraphActivity(g.ID, task, run)
	if len(activity.Requests) != 0 {
		t.Fatal("closed session still offers an answer")
	}
}

// Automatic review has its own lifecycle; it must not raise a user-facing
// approval while an independent decision call is still choosing an answer.
func TestGraphApprovalReviewAndSetupStatus(t *testing.T) {
	for _, outcome := range []string{"approve", "escalate", "unavailable"} {
		t.Run(outcome, func(t *testing.T) {
			e, req := graphEngine(t, false)
			req.Policy.Mode = "defined"
			req.Tasks = append(req.Tasks, graphWorker("worker", "goal"))
			g := createGraphTest(t, e, req)
			if err := e.reconcileGraphs(); err != nil {
				t.Fatal(err)
			}
			g, _ = e.Store.Graph(g.ID)
			var task, run string
			for id, m := range g.Members {
				if m.Spec.Kind == "application" {
					task, run = id, m.CurrentRunID
				}
			}
			if err := e.Store.Transition(run, "agent_started", "running", "Working", "Started"); err != nil {
				t.Fatal(err)
			}
			session, token, err := e.Store.CreateBoundAgentSession("codex", "Worker", e.Root, req.ProjectID, "", run, decision.LocalOperator())
			if err != nil {
				t.Fatal(err)
			}
			request, err := e.Store.IngestAgentRequest(session.ID, token, json.RawMessage(codexApproval))
			if err != nil {
				t.Fatal(err)
			}
			check := func(state string, human, reviewing int, setup bool) {
				t.Helper()
				view, err := e.Store.GraphView(g.ID, task)
				n := view.Nodes[task]
				if err != nil || n.State != state || n.HumanRequests != human || n.ReviewingRequests != reviewing || n.DecisionSetup != setup {
					t.Fatalf("unexpected status: %+v %v", n, err)
				}
				attention := e.Store.Attention()
				if human+reviewing > 0 && (len(attention) != 1 || (attention[0].Resolver == "human") != (human > 0) || attention[0].SetupRequired != setup) {
					t.Fatalf("attention disagrees: %+v", attention)
				}
			}
			check("waiting_input", 1, 0, true)
			// A persisted manual route must not hide the later configuration change.
			if _, err := e.RouteAgentRequest(context.Background(), session.ID, token, request.RequestID); err != nil {
				t.Fatal(err)
			}
			check("waiting_input", 1, 0, true)
			var calls atomic.Int32
			entered, release := make(chan struct{}), make(chan struct{})
			srv := choiceServer(t, func() string { close(entered); <-release; return outcome }, &calls)
			defer srv.Close()
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			p := agentpolicy.Default()
			p.Autonomy, p.Guidance = "decision", "Allow read-only repository inspection."
			p.DecisionProvider = modelConfig(srv.URL, "before_segment")
			if outcome == "unavailable" {
				t.Setenv("MITOS_TEST_DECISION_CREDENTIAL", "")
				p.DecisionProvider.APIKeyEnv = "MITOS_TEST_DECISION_CREDENTIAL"
			}
			if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
				t.Fatal(err)
			}
			check("reviewing_approval", 0, 1, false)
			if outcome == "unavailable" {
				if err := e.ResolveAgentRequest(context.Background(), session.ID, token, request.RequestID); err != nil {
					t.Fatal(err)
				}
				check("waiting_input", 1, 0, true)
				return
			}
			done := make(chan error, 1)
			go func() { done <- e.ResolveAgentRequest(context.Background(), session.ID, token, request.RequestID) }()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("review did not start")
			}
			// Reads stay responsive while the independent reviewer waits for the API.
			check("reviewing_approval", 0, 1, false)
			activity, err := e.Store.GraphActivity(g.ID, task, run)
			if err != nil || activity.Requests[0].Resolver != "decision" {
				t.Fatalf("missing review context: %+v %v", activity, err)
			}
			unblock()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if outcome == "approve" {
				check("resuming", 0, 0, false)
			} else {
				check("waiting_input", 1, 0, false)
			}
			if calls.Load() != 1 {
				t.Fatal("approval did not use one independent decision call", calls.Load())
			}
		})
	}
}
