package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mitos/internal/agentcli"
	"mitos/internal/agentpolicy"
	"mitos/internal/decision"
	"mitos/internal/harness"
	"net"
	"path/filepath"
	"strings"
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

func blockedAnswerFixture(t *testing.T) (*Engine, CreateGraph, agentpolicy.Policy, AgentSession, string, AgentRequestView, string) {
	t.Helper()
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
	if err != nil || status.State != "accepted" || len(status.Body) == 0 {
		t.Fatal(status, err)
	}
	// The native process could not apply the recorded answer; the adapter recorded
	// a failed delivery, which stays retryable against the same request.
	if err = e.Store.AgentDelivery(session.ID, token, request.RequestID, status.Digest, "application_blocked"); err != nil {
		t.Fatal(err)
	}
	return e, req, p, session, token, status, request.RequestID
}

// A blocked delivery is retryable, so every retry must revalidate the live
// authorization that produced the answer instead of replaying the saved body.
func TestBlockedAutomatedAnswerRevalidatesAuthorizationOnRetry(t *testing.T) {
	t.Run("revoked delegation rule", func(t *testing.T) {
		e, req, p, session, token, status, key := blockedAnswerFixture(t)
		p.Rules = nil
		if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
			t.Fatal(err)
		}
		retried, err := e.Store.AgentRequestStatus(session.ID, token, key)
		if err == nil || len(retried.Body) != 0 {
			t.Fatal("revoked answer released after failed delivery", retried, err)
		}
		if !strings.Contains(fmt.Sprint(err), "automated_answer_policy_changed") {
			t.Fatal("unexpected refusal", err)
		}
		if err = e.Store.AgentDelivery(session.ID, token, key, status.Digest, "applied"); err == nil {
			t.Fatal("revoked retry acknowledged")
		}
	})
	t.Run("supervision hold withholds the body", func(t *testing.T) {
		e, req, _, session, token, status, key := blockedAnswerFixture(t)
		incident := SupervisionIncident{ID: "hold", ProjectID: req.ProjectID, State: "stopping", At: time.Now().UTC()}
		e.Store.mu.Lock()
		err := e.Store.commit("", "effect", "supervision_incident_opened", change{SupervisionIncident: &incident})
		e.Store.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		retried, err := e.Store.AgentRequestStatus(session.ID, token, key)
		if err == nil || len(retried.Body) != 0 {
			t.Fatal("supervision hold did not withhold the blocked answer", retried, err)
		}
		if err = e.Store.AgentDelivery(session.ID, token, key, status.Digest, "applied"); err == nil {
			t.Fatal("answer under supervision hold acknowledged")
		}
	})
	t.Run("revocation survives restart between failure and retry", func(t *testing.T) {
		e, req, p, session, token, _, key := blockedAnswerFixture(t)
		p.Rules = nil
		if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
			t.Fatal(err)
		}
		dir := e.Store.Dir()
		if err := e.Store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		if retried, err := reopened.AgentRequestStatus(session.ID, token, key); err == nil || len(retried.Body) != 0 {
			t.Fatal("restart released the revoked answer", retried, err)
		}
	})
	t.Run("authorized retry preserves the exact answer", func(t *testing.T) {
		e, _, _, session, token, status, key := blockedAnswerFixture(t)
		dir := e.Store.Dir()
		if err := e.Store.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := OpenStore(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		retried, err := reopened.AgentRequestStatus(session.ID, token, key)
		if err != nil || retried.State != "application_blocked" || !bytes.Equal(retried.Body, status.Body) || retried.Digest != status.Digest {
			t.Fatal("authorized retry lost the recorded answer", retried, status, err)
		}
		if err = reopened.AgentDelivery(session.ID, token, key, retried.Digest, "applied"); err != nil {
			t.Fatal(err)
		}
		run, err := reopened.Get(retried.RunIDs[0])
		if err != nil || run.Decision.AppliedAt == nil || run.Decision.ApplicationError != "" {
			t.Fatalf("retry not applied: %+v %v", run.Decision, err)
		}
	})
	t.Run("applied acknowledgment stays historical", func(t *testing.T) {
		e, req, p, session, token, status, key := blockedAnswerFixture(t)
		if err := e.Store.AgentDelivery(session.ID, token, key, status.Digest, "applied"); err != nil {
			t.Fatal(err)
		}
		p.Rules = nil
		if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
			t.Fatal(err)
		}
		// The applied acknowledgment is durable evidence of a past authorized
		// delivery; it must stay readable without granting a fresh retry.
		historical, err := e.Store.AgentRequestStatus(session.ID, token, key)
		if err != nil || historical.State != "applied" || !bytes.Equal(historical.Body, status.Body) {
			t.Fatal("applied evidence lost", historical, err)
		}
		if err = e.Store.AgentDelivery(session.ID, token, key, status.Digest, "application_blocked"); err != nil {
			t.Fatal(err)
		}
		replayed, err := e.Store.AgentRequestStatus(session.ID, token, key)
		if err != nil || replayed.State != "applied" {
			t.Fatal("historical acknowledgment rewritten", replayed, err)
		}
	})
}

func TestSupervisorStopsAutomaticOrchestratorThroughNativeObservation(t *testing.T) {
	e, req := automaticScriptEngine(t, `import sys,time,json,os
sys.stdin.read()
sys.__stdout__.write(json.dumps({'method':'item/started','params':{'threadId':'fixture-thread','turnId':'fixture-turn','item':{'id':'forbidden','type':'commandExecution','command':'forbidden operation','cwd':os.getcwd()}}})+'\n')
sys.__stdout__.flush()
time.sleep(60)
`)
	p := e.Store.AgentPolicy(req.ProjectID)
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
	if after.OrchestratorCalls != before.OrchestratorCalls || after.Status == "accepted" {
		t.Fatal("orchestrator continued through hold", before.OrchestratorCalls, after.OrchestratorCalls, after.Status)
	}
}

func TestObservationRenewsOnlyLiveSessions(t *testing.T) {
	for _, state := range []string{"live", "closed", "expired"} {
		t.Run(state, func(t *testing.T) {
			e, req := graphEngine(t, false)
			p := agentpolicy.Default()
			p.Supervision.Mode = "rules"
			if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
				t.Fatal(err)
			}
			session, token := supervisorSession(t, e, req.ProjectID)
			if state == "closed" {
				if err := e.Store.CloseAgentSession(session.ID, token); err != nil {
					t.Fatal(err)
				}
			}
			e.Store.mu.Lock()
			before := e.Store.agentSessions[session.ID]
			if state == "expired" {
				before.LeaseUntil = time.Now().Add(-time.Second)
				e.Store.agentSessions[session.ID] = before
			}
			e.Store.mu.Unlock()
			raw, _ := json.Marshal(map[string]any{"method": "item/completed", "params": map[string]any{"item": map[string]any{"type": "commandExecution", "cwd": e.Root, "command": "echo hello"}}})
			err := e.ObserveAgent(context.Background(), session.ID, token, raw)
			e.Store.mu.RLock()
			after := e.Store.agentSessions[session.ID]
			e.Store.mu.RUnlock()
			if state == "live" {
				if err != nil || !after.LeaseUntil.After(before.LeaseUntil) {
					t.Fatal("successful observation did not renew liveness", err)
				}
			} else if err == nil || after.Closed != before.Closed || !after.LeaseUntil.Equal(before.LeaseUntil) {
				t.Fatal("observation revived an inactive session", err)
			}
		})
	}
}

func TestLargeSupervisionObservationsContinueNativeProxy(t *testing.T) {
	for _, mode := range []string{"off", "rules", "model"} {
		for _, kind := range []string{"commandExecution", "fileChange", "mcpToolCall"} {
			t.Run(mode+"/"+kind, func(t *testing.T) {
				var calls atomic.Int32
				srv := choiceServer(t, func() string { return "allow" }, &calls)
				defer srv.Close()
				e, req := graphEngine(t, false)
				e.Apps = append(e.Apps, Application{ID: "guard", DecisionModel: modelConfig(srv.URL, "before_segment")})
				p := agentpolicy.Default()
				p.Supervision = agentpolicy.Supervision{Mode: mode, Application: "guard", Guidance: "Keep file actions inside the project workspace", MaxCalls: 3, WorkspaceOnly: true}
				if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
					t.Fatal(err)
				}
				session, token := supervisorSession(t, e, req.ProjectID)
				item := map[string]any{"type": kind, "cwd": e.Root, "command": "echo hello"}
				setContent := func(content string) {
					switch kind {
					case "commandExecution":
						item["aggregatedOutput"] = content
					case "fileChange":
						item["changes"] = []any{map[string]string{"path": "new.txt", "diff": content}}
					case "mcpToolCall":
						item["result"] = map[string]string{"text": content}
						item["arguments"] = map[string]string{"path": e.Root}
					}
				}
				setContent("")
				event := map[string]any{"method": "item/completed", "params": map[string]any{"item": item}}
				empty, _ := json.Marshal(event)
				setContent(strings.Repeat("x", harness.MaxObservationBytes-len(empty)))
				raw, _ := json.Marshal(event)
				if len(raw) != harness.MaxObservationBytes {
					t.Fatal("fixture must exercise the inclusive limit", len(raw))
				}
				// Full 16 MiB evidence is parsed and journaled under the race
				// detector; the former 1 MiB fixture's deadline is too short.
				ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				client, server := net.Pipe()
				defer server.Close()
				input, writer := io.Pipe()
				defer input.Close()
				defer writer.Close()
				next := `{"method":"thread/started","params":{"thread":{"id":"still-live"}}}`
				go func() { defer server.Close(); fmt.Fprintf(server, "%s\n%s\n", raw, next) }()
				var output bytes.Buffer
				if err := harness.ProxyCodex(ctx, engineBroker{e: e, id: session.ID, token: token}, client, input, &output); err != nil {
					t.Fatal(err)
				}
				if output.String() != string(raw)+"\n"+next+"\n" {
					t.Fatal("native output changed or continuation lost")
				}
				if e.Store.SupervisionHeld(req.ProjectID) {
					t.Fatal("large output held project")
				}
				if mode != "off" {
					verdict := e.Store.supervisorVerdicts[session.ID+"/observation:"+graphDigest(json.RawMessage(raw))]
					if !bytes.Equal(verdict.Evidence, raw) {
						t.Fatal("full audit evidence lost")
					}
				}
				if (calls.Load() == 1) != (mode == "model") {
					t.Fatal("unexpected model calls", calls.Load())
				}
			})
		}
	}
}

func TestClaudeOutsideFileCannotReceiveOperatorApproval(t *testing.T) {
	for _, tool := range []string{"Write", "Edit"} {
		t.Run(tool, func(t *testing.T) {
			e, req := graphEngine(t, false)
			p := agentpolicy.Default()
			p.Supervision.WorkspaceOnly = true
			if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
				t.Fatal(err)
			}
			session, token, err := e.Store.CreateBoundAgentSession("claude", "Claude boundary", e.Root, req.ProjectID, "", "", decision.LocalOperator())
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"session_id": "s", "tool_use_id": "t", "hook_event_name": "PreToolUse", "tool_name": tool, "cwd": e.Root, "tool_input": map[string]string{"file_path": filepath.Join(t.TempDir(), "outside.txt")}})
			request, err := e.Store.IngestAgentRequest(session.ID, token, raw)
			if err != nil {
				t.Fatal(err)
			}
			run, _ := e.Store.Get(request.RunIDs[0])
			if _, err = e.ResolveAs(run.ID, run.Decision.ID, "approve", "operator approval", "api", decision.LocalOperator()); err == nil {
				t.Fatal("operator bypassed boundary before supervisor")
			}
			if err = e.ResolveAgentRequest(context.Background(), session.ID, token, request.RequestID); err == nil {
				t.Fatal("outside file cleared")
			}
			incidents := e.Store.SupervisionIncidents(req.ProjectID)
			if len(incidents) != 1 || incidents[0].Verdict.Outcome != "stop" {
				t.Fatal(incidents)
			}
			if _, err = e.ResolveAs(run.ID, run.Decision.ID, "approve", "operator approval", "api", decision.LocalOperator()); err == nil {
				t.Fatal("operator bypassed supervisor veto")
			}
		})
	}
}

func TestInvalidObservationExplainsFailureWithoutEchoingPayload(t *testing.T) {
	e := &Engine{}
	for _, tc := range []struct{ name, raw, want string }{
		{"malformed", `{"private":"secret",`, "contains invalid JSON"},
		{"oversized", `{"private":"` + strings.Repeat("s", harness.MaxObservationBytes) + `"}`, fmt.Sprintf("received %d bytes, limit %d bytes", harness.MaxObservationBytes+14, harness.MaxObservationBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := e.ObserveAgent(context.Background(), "", "", json.RawMessage(tc.raw))
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("expected safe, specific observation diagnostic, got %v", err)
			}
		})
	}
}
