package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mitos/internal/agentcli"
	"mitos/internal/agentpolicy"
	"mitos/internal/decision"
	"sync/atomic"
	"testing"
	"time"
)

func supervisorSession(t *testing.T, e *Engine, project string) (AgentSession, string) {
	t.Helper()
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Supervision fixture", e.Root, project, "", "", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	return session, token
}
func supervisorRequest(t *testing.T, e *Engine, session AgentSession, token string) AgentRequestView {
	t.Helper()
	request, err := e.Store.IngestAgentRequest(session.ID, token, json.RawMessage(routingQuestion))
	if err != nil {
		t.Fatal(err)
	}
	return request
}
func TestIndependentSupervisorVetoAndDurableHold(t *testing.T) {
	var calls atomic.Int32
	srv := choiceServer(t, func() string { return "stop" }, &calls)
	defer srv.Close()
	e, req := graphEngine(t, false)
	e.Apps = append(e.Apps, Application{ID: "guard", DecisionModel: modelConfig(srv.URL, "before_segment")})
	p := agentpolicy.Default()
	p.Supervision = agentpolicy.Supervision{Mode: "model", Application: "guard", Guidance: "Only read project files", MaxCalls: 3}
	p, err := e.SaveAgentPolicy(req.ProjectID, p)
	if err != nil {
		t.Fatal(err)
	}
	session, token := supervisorSession(t, e, req.ProjectID)
	request := supervisorRequest(t, e, session, token)
	if err = e.ResolveAgentRequest(context.Background(), session.ID, token, request.RequestID); err == nil {
		t.Fatal("supervisor did not veto")
	}
	incidents := e.Store.SupervisionIncidents(req.ProjectID)
	if len(incidents) != 1 || !e.Store.SupervisionHeld(req.ProjectID) || incidents[0].Verdict.Outcome != "stop" {
		t.Fatal(incidents)
	}
	if err = e.reconcileSupervision(); err != nil {
		t.Fatal(err)
	}
	if e.Store.SupervisionIncidents(req.ProjectID)[0].StopConfirmed {
		t.Fatal("unmanaged process falsely confirmed")
	}
	if _, _, err = e.Store.CreateBoundAgentSession("codex", "Blocked", e.Root, req.ProjectID, "", "", decision.LocalOperator()); err == nil {
		t.Fatal("session admitted during hold")
	}
	p.Supervision.Mode = "off"
	if _, err = e.SaveAgentPolicy(req.ProjectID, p); err != nil {
		t.Fatal(err)
	}
	if !e.Store.SupervisionHeld(req.ProjectID) {
		t.Fatal("policy change released incident")
	}
	dir := e.Store.Dir()
	if err = e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	replayed, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer replayed.Close()
	e.Store = replayed
	if !e.Store.SupervisionHeld(req.ProjectID) || len(e.Store.SupervisionIncidents(req.ProjectID)) != 1 {
		t.Fatal("hold lost on restart")
	}
	if _, err = e.ResolveSupervision(req.ProjectID, incidents[0].ID, "Inspected and terminated the external process", false); err == nil {
		t.Fatal("missing confirmation accepted")
	}
	if _, err = e.ResolveSupervision(req.ProjectID, incidents[0].ID, "Inspected and terminated the external process", true); err != nil {
		t.Fatal(err)
	}
	if e.Store.SupervisionHeld(req.ProjectID) || calls.Load() != 1 {
		t.Fatal("resolution/budget", calls.Load())
	}
}
func TestSupervisorClearanceIsNotPermissionAndBudgetIsSeparate(t *testing.T) {
	var calls atomic.Int32
	srv := choiceServer(t, func() string { return "allow" }, &calls)
	defer srv.Close()
	e, req := graphEngine(t, false)
	e.Apps = append(e.Apps, Application{ID: "guard", DecisionModel: modelConfig(srv.URL, "before_segment")})
	p := agentpolicy.Default()
	p.Autonomy = "delegated"
	p.Supervision = agentpolicy.Supervision{Mode: "model", Application: "guard", Guidance: "Review project scope", MaxCalls: 1}
	if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
		t.Fatal(err)
	}
	session, token := supervisorSession(t, e, req.ProjectID)
	raw := json.RawMessage(fmt.Sprintf(`{"id":2,"method":"item/commandExecution/requestApproval","params":{"threadId":"t","turnId":"u","command":"echo hello","cwd":%q}}`, e.Root))
	request, err := e.Store.IngestAgentRequest(session.ID, token, raw)
	if err != nil {
		t.Fatal(err)
	}
	run, _ := e.Store.Get(request.RunIDs[0])
	if _, err = e.ResolveAs(run.ID, run.Decision.ID, "approve", "before supervisor", "api", decision.LocalOperator()); err == nil {
		t.Fatal("human bypassed pending supervision")
	}
	if err = e.ResolveAgentRequest(context.Background(), session.ID, token, request.RequestID); err != nil {
		t.Fatal(err)
	}
	status, err := e.Store.AgentRequestStatus(session.ID, token, request.RequestID)
	if err != nil || status.State != "pending" || status.Route.Route.Resolver != "human" {
		t.Fatal("clearance granted missing authority", status, err)
	}
	// Duplicate observation of the same request consumes no additional call.
	if err = e.ResolveAgentRequest(context.Background(), session.ID, token, request.RequestID); err != nil {
		t.Fatal(err)
	}
	observation := json.RawMessage(fmt.Sprintf(`{"method":"item/started","params":{"item":{"id":"next","type":"commandExecution","command":"echo another","cwd":%q}}}`, e.Root))
	if err = e.ObserveAgent(context.Background(), session.ID, token, observation); err == nil {
		t.Fatal("exhausted supervisor continued")
	}
	incident := e.Store.SupervisionIncidents(req.ProjectID)
	if len(incident) != 1 || incident[0].Verdict.Outcome != "hold" || calls.Load() != 1 {
		t.Fatal(incident, calls.Load())
	}
	for _, v := range e.Store.modelEvaluations {
		if v.Scope != session.ID+"/supervision" {
			t.Fatal("supervision used routing allowance", v.Scope)
		}
	}
}
func TestSupervisorStopsOwnedSessionAndBlocksNativeLaunch(t *testing.T) {
	e, req := graphEngine(t, true)
	interactiveFixture(t, e)
	p := agentpolicy.Default()
	p.Rules = []agentpolicy.Rule{{ID: "forbidden", Command: "forbidden operation", Directory: ".", Action: "deny"}}
	if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
		t.Fatal(err)
	}
	native, err := e.StartAgentSession(req.ProjectID, agentcli.Request{AgentID: "worker", Prompt: "Wait"}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Owned", e.Root, req.ProjectID, native.ID, "", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(fmt.Sprintf(`{"method":"item/started","params":{"item":{"type":"commandExecution","command":"forbidden operation","cwd":%q}}}`, e.Root))
	if err = e.ObserveAgent(context.Background(), session.ID, token, raw); err == nil {
		t.Fatal("denied command accepted")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		v, _ := e.Desk.Session(native.ID)
		if v.Status == "stopped" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err = e.reconcileSupervision(); err != nil {
		t.Fatal(err)
	}
	incidents := e.Store.SupervisionIncidents(req.ProjectID)
	if len(incidents) != 1 || !incidents[0].StopConfirmed {
		t.Fatal("owned stop unconfirmed", incidents)
	}
	if _, err = e.Desk.StartAgent(req.ProjectID, agentcli.Request{AgentID: "worker", Prompt: "Bypass"}, "http://localhost"); err == nil {
		t.Fatal("native launch bypassed hold")
	}
	if _, err = e.StartAutomaticGoal(AutomaticGoal{ProjectID: req.ProjectID, Key: "held", Goal: "Do work"}); err == nil {
		t.Fatal("automatic work bypassed hold")
	}
	if err = e.Store.AgentHeartbeat(session.ID, token); err == nil {
		t.Fatal("revoked bridge remained live")
	}
}
func TestSupervisorPolicyChangeRejectsStaleVerdict(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	srv := choiceServer(t, func() string { close(entered); <-release; return "allow" }, &calls)
	defer srv.Close()
	e, req := graphEngine(t, false)
	e.Apps = append(e.Apps, Application{ID: "guard", DecisionModel: modelConfig(srv.URL, "before_segment")})
	p := agentpolicy.Default()
	p.Supervision = agentpolicy.Supervision{Mode: "model", Application: "guard", Guidance: "Read only", MaxCalls: 3}
	p, err := e.SaveAgentPolicy(req.ProjectID, p)
	if err != nil {
		t.Fatal(err)
	}
	session, token := supervisorSession(t, e, req.ProjectID)
	request := supervisorRequest(t, e, session, token)
	done := make(chan error, 1)
	go func() { done <- e.ResolveAgentRequest(context.Background(), session.ID, token, request.RequestID) }()
	<-entered
	p.Supervision.Guidance = "Read only selected files"
	_, err = e.SaveAgentPolicy(req.ProjectID, p)
	close(release)
	if err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, ErrConflict) {
		t.Fatal("stale verdict accepted", err)
	}
	if len(e.Store.supervisorVerdicts) != 0 || len(e.Store.SupervisionIncidents(req.ProjectID)) != 0 {
		t.Fatal("stale verdict published")
	}
}

func TestAcceptedAutomatedAnswerCannotBeDeliveredAfterRevocation(t *testing.T) {
	e, req := graphEngine(t, false)
	p := agentpolicy.Default()
	p.Autonomy = "delegated"
	p.Rules = []agentpolicy.Rule{{ID: "approved", Command: "printf reviewed", Directory: ".", Action: "allow"}}
	p, err := e.SaveAgentPolicy(req.ProjectID, p)
	if err != nil {
		t.Fatal(err)
	}
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Approval", "/tmp", req.ProjectID, "", "", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	request, err := e.Store.IngestAgentRequest(session.ID, token, json.RawMessage(codexApproval))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.ResolveAgentRequest(context.Background(), session.ID, token, request.RequestID); err != nil {
		t.Fatal(err)
	}
	status, err := e.Store.AgentRequestStatus(session.ID, token, request.RequestID)
	if err != nil || status.State != "accepted" {
		t.Fatal(status, err)
	}
	p.Rules = nil
	if _, err = e.SaveAgentPolicy(req.ProjectID, p); err != nil {
		t.Fatal(err)
	}
	status, err = e.Store.AgentRequestStatus(session.ID, token, request.RequestID)
	if err == nil || len(status.Body) != 0 {
		t.Fatal("revoked answer released", status, err)
	}
}

func TestSupervisorStopsAutomaticCoordinatorThroughNativeObservation(t *testing.T) {
	e, req := automaticScriptEngine(t, `import sys,time,json,os
sys.stdin.read()
sys.__stdout__.write(json.dumps({'method':'item/started','params':{'threadId':'fixture-thread','turnId':'fixture-turn','item':{'id':'forbidden','type':'commandExecution','command':'forbidden operation','cwd':os.getcwd()}}})+'\n')
sys.__stdout__.flush()
time.sleep(60)
`)
	p := agentpolicy.Default()
	p.Rules = []agentpolicy.Rule{{ID: "boundary", Command: "forbidden operation", Directory: ".", Action: "deny"}}
	if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
		t.Fatal(err)
	}
	graph, err := e.StartAutomaticGoal(AutomaticGoal{Key: "supervisor-stop", ProjectID: req.ProjectID, Goal: "Review project files"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	var incidents []SupervisionIncident
	for time.Now().Before(deadline) {
		incidents = e.Store.SupervisionIncidents(req.ProjectID)
		if len(incidents) == 1 && incidents[0].StopConfirmed {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(incidents) != 1 || !incidents[0].StopConfirmed {
		t.Fatal("automatic process stop was not confirmed", incidents)
	}
	if len(incidents[0].Targets) != 1 || incidents[0].Targets[0].Kind != "run" {
		t.Fatal(incidents[0].Targets)
	}
	run, err := e.Store.Get(incidents[0].Targets[0].ID)
	if err != nil || run.Status != "cancelled" || run.Claim != nil && run.Claim.ReleasedAt == nil {
		t.Fatal(run.Status, err)
	}
	before, _ := e.Store.Graph(graph.ID)
	time.Sleep(400 * time.Millisecond)
	after, _ := e.Store.Graph(graph.ID)
	if after.ControllerCalls != before.ControllerCalls || after.Status == "accepted" {
		t.Fatal("coordinator continued through hold", before.ControllerCalls, after.ControllerCalls, after.Status)
	}
}
