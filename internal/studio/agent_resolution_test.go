package studio

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mitos/internal/agentpolicy"
	"mitos/internal/decision"
	"mitos/internal/harness"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestDelegatedResolutionIsDistinctFromClassification(t *testing.T) {
	var calls atomic.Int32
	srv := choiceServer(t, func() string {
		if calls.Load() == 1 {
			return "decision"
		}
		return "option_0"
	}, &calls)
	defer srv.Close()
	e, req := graphEngine(t, false)
	e.Apps = append(e.Apps, Application{ID: "decisions", DecisionModel: modelConfig(srv.URL, "before_segment")})
	p := agentpolicy.Default()
	p.Autonomy = "delegated"
	p.DecisionApplication = "decisions"
	if _, err := e.SaveAgentPolicy(req.ProjectID, p); err != nil {
		t.Fatal(err)
	}
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Decisions", e.Root, req.ProjectID, "", "", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	request, err := e.Store.IngestAgentRequest(session.ID, token, json.RawMessage(routingQuestion))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.ResolveAgentRequest(context.Background(), session.ID, token, request.RequestID); err != nil {
		t.Fatal(err)
	}
	status, err := e.Store.AgentRequestStatus(session.ID, token, request.RequestID)
	if err != nil || status.State != "accepted" || !strings.Contains(string(status.Body), "Alpha") {
		t.Fatal(status, err)
	}
	if calls.Load() != 2 {
		t.Fatal("classification and answer must be separate", calls.Load())
	}
	run, _ := e.Store.Get(request.RunIDs[0])
	if run.Decision.Resolution.Resolver != "mitos-automation" || run.Decision.Resolution.Via != "automation" {
		t.Fatal("automation impersonated operator")
	}
}
func TestExactCommandApprovalAndRevocation(t *testing.T) {
	e, req := graphEngine(t, false)
	p := agentpolicy.Default()
	p.Autonomy = "delegated"
	p.Rules = []agentpolicy.Rule{{ID: "reviewed", Command: "printf reviewed", Directory: ".", Action: "allow"}}
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
	route, err := e.RouteAgentRequest(context.Background(), session.ID, token, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	p.Rules = nil
	if _, err = e.SaveAgentPolicy(req.ProjectID, p); err != nil {
		t.Fatal(err)
	}
	saved := e.Store.agentRequests[route.Key]
	if err = e.applyAgentResolution(session, saved, route, 0, "approve", nil); err != ErrConflict {
		t.Fatal("revoked grant applied", err)
	}
	status, _ := e.Store.AgentRequestStatus(session.ID, token, request.RequestID)
	if status.State != "pending" {
		t.Fatal(status)
	}
}

func TestHeadlessAgentQuestionUsesSharedInbox(t *testing.T) {
	e, req := graphEngine(t, true)
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Worker question", e.Root, req.ProjectID, "", "", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	client, server := net.Pipe()
	defer server.Close()
	go func() {
		scan := bufio.NewScanner(server)
		for scan.Scan() {
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			_ = json.Unmarshal(scan.Bytes(), &msg)
			switch msg.Method {
			case "initialize":
				fmt.Fprintln(server, `{"id":1,"result":{}}`)
			case "thread/start":
				fmt.Fprintln(server, `{"id":2,"result":{"thread":{"id":"t"}}}`)
			case "turn/start":
				fmt.Fprintln(server, `{"id":3,"result":{"turn":{"id":"u"}}}`)
				fmt.Fprintln(server, routingQuestion)
			case "":
				if string(msg.ID) == "1" {
					fmt.Fprintln(server, `{"method":"serverRequest/resolved","params":{"threadId":"t","requestId":1}}`)
					fmt.Fprintln(server, `{"method":"item/completed","params":{"threadId":"t","turnId":"u","item":{"id":"final","type":"agentMessage","phase":"final_answer","text":"Result after answer"}}}`)
					fmt.Fprintln(server, `{"method":"turn/completed","params":{"threadId":"t","turn":{"id":"u","status":"completed"}}}`)
				}
			}
		}
	}()
	result := ""
	done := make(chan error, 1)
	go func() {
		done <- harness.RunCodex(ctx, engineBroker{e: e, id: session.ID, token: token}, client, strings.NewReader(""), io.Discard, e.Root, "Assignment", harness.CodexOptions{Headless: true, Result: func(s string) error { result = s; return nil }})
	}()
	var card Run
	for ctx.Err() == nil {
		for _, r := range e.Store.List() {
			if r.Decision != nil && r.Decision.Source != nil && r.Decision.Source.SessionID == session.ID {
				card = r
				break
			}
		}
		if card.ID != "" {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if card.ID == "" {
		t.Fatal("request did not reach Inbox")
	}
	if _, err = e.ResolveAs(card.ID, card.Decision.ID, "option_0", "Reviewed", "api", decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil || result != "Result after answer" {
		t.Fatal(result, err)
	}
}

func TestProjectDecisionProviderResolvesAndCanBeRevoked(t *testing.T) {
	var calls atomic.Int32
	srv := choiceServer(t, func() string { return "approve" }, &calls)
	defer srv.Close()
	e, req := graphEngine(t, false)
	p := agentpolicy.Default()
	p.Autonomy = "decision"
	p.Guidance = "Allow printf reviewed."
	p.DecisionProvider = modelConfig(srv.URL, "before_segment")
	saved, err := e.SaveAgentPolicy(req.ProjectID, p)
	if err != nil {
		t.Fatal(err)
	}
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Review", "/tmp", req.ProjectID, "", "", decision.LocalOperator())
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
	if err != nil || status.State != "accepted" || calls.Load() != 1 {
		t.Fatal(status, err, calls.Load())
	}
	if len(e.Store.Attention()) != 0 {
		t.Fatal("resolved request remains in notifications")
	}
	saved.Autonomy = "manual"
	if _, err = e.SaveAgentPolicy(req.ProjectID, saved); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Store.AgentRequestStatus(session.ID, token, request.RequestID); err == nil {
		t.Fatal("revoked automation answer delivered")
	}
}

func TestAttentionTracksOwningGraphAndClearsAfterAnswer(t *testing.T) {
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	// A native request is stored under a separate approval run, but belongs to this worker.
	e.Store.mu.Lock()
	e.Store.runs["owner"] = &Run{ID: "owner", TaskID: "worker", GraphAttempt: &GraphAttempt{GraphID: g.ID}}
	e.Store.mu.Unlock()
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Worker approval", "/tmp", req.ProjectID, "", "owner", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	request, err := e.Store.IngestAgentRequest(session.ID, token, json.RawMessage(codexApproval))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.RouteAgentRequest(context.Background(), session.ID, token, request.RequestID); err != nil {
		t.Fatal(err)
	}
	notices := e.Store.Attention()
	if len(notices) != 1 {
		t.Fatal(notices)
	}
	n := notices[0]
	if n.ProjectID != req.ProjectID || n.GraphID != g.ID || n.TaskID != "worker" || n.Reason != "automatic resolution is disabled" {
		t.Fatal(n)
	}
	e.Store.mu.RLock()
	state := e.Store.graphDisplayState(&g, &GraphMember{State: "running", CurrentRunID: "owner"})
	e.Store.mu.RUnlock()
	if state != "waiting_for_answer" {
		t.Fatal(state)
	}
	if _, err = e.ResolveAs(n.Card.RunID, n.ID, "deny", "", "api", decision.LocalOperator()); err != nil {
		t.Fatal(err)
	}
	if len(e.Store.Attention()) != 0 {
		t.Fatal("answered notice retained")
	}
}

func TestAsyncAttentionDoesNotMarkWorkerBlocked(t *testing.T) {
	e, req := graphEngine(t, false)
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Question", "/tmp", req.ProjectID, "", "owner", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	raw := strings.Replace(routingQuestion, "item/tool/requestUserInput", "mitos/asyncQuestion", 1)
	if _, err = e.Store.IngestAgentRequest(session.ID, token, json.RawMessage(raw)); err != nil {
		t.Fatal(err)
	}
	notices := e.Store.Attention()
	if len(notices) != 1 || notices[0].Blocking {
		t.Fatal(notices)
	}
	e.Store.mu.RLock()
	waiting := e.Store.runWaitingForAnswer("owner")
	e.Store.mu.RUnlock()
	if waiting {
		t.Fatal("nonblocking clarification paused worker display")
	}
}
