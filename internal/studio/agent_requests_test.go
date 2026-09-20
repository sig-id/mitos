package studio

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"mitos/internal/decision"
)

const codexQuestion = `{"id":7,"method":"item/tool/requestUserInput","params":{"threadId":"thread_1","turnId":"turn_1","itemId":"item_1","isBlocking":true,"questions":[{"id":"name","header":"Name","question":"What name should be used?","schema":{"type":"string","minLength":2,"maxLength":10,"pattern":"^[A-Za-z]+$"}}]}}`
const codexApproval = `{"id":8,"method":"item/commandExecution/requestApproval","params":{"threadId":"thread_1","turnId":"turn_1","itemId":"item_2","command":"printf reviewed","cwd":"/tmp","startedAtMs":1}}`
const claudeQuestion = `{"session_id":"thread_1","hook_event_name":"PreToolUse","tool_use_id":"tool_1","tool_name":"AskUserQuestion","tool_input":{"questions":[{"header":"Name","question":"Which name?","options":[{"label":"Alpha","description":"Use Alpha"},{"label":"Beta","description":"Use Beta"}],"multiSelect":false}]}}`
const claudeApproval = `{"session_id":"thread_1","hook_event_name":"PreToolUse","tool_use_id":"tool_2","tool_name":"Bash","tool_input":{"command":"printf reviewed"}}`

func TestAgentNativeAnswerOutboxAndRestart(t *testing.T) {
	for _, test := range []struct{ adapter, raw, choice, value, expected string }{{"codex", codexQuestion, "answer", `"Alpha"`, `"answers":["Alpha"]`}, {"codex", codexApproval, "approve", "null", `"decision":"accept"`}, {"claude", claudeQuestion, "answer", `"Alpha"`, `"Which name?":"Alpha"`}, {"claude", claudeApproval, "deny", "null", `"permissionDecision":"deny"`}} {
		t.Run(test.adapter+test.choice, func(t *testing.T) {
			dir := t.TempDir()
			s, err := OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			session, secret, err := s.CreateAgentSession(test.adapter, "Agent / project", t.TempDir(), decision.LocalOperator())
			if err != nil {
				t.Fatal(err)
			}
			request, err := s.IngestAgentRequest(session.ID, secret, json.RawMessage(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			duplicate, err := s.IngestAgentRequest(session.ID, secret, json.RawMessage(test.raw))
			if err != nil || len(s.List()) != 1 || duplicate.RunIDs[0] != request.RunIDs[0] {
				t.Fatal("source request not deduplicated", err)
			}
			_, token := pairTestDevice(t, s, decision.LocalOperator())
			snap, err := s.DeviceSnapshot(token)
			if err != nil {
				t.Fatal(err)
			}
			c := snap.Cards[0]
			if test.choice == "answer" && c.Input != "text" {
				t.Fatal(c.Input)
			}
			command := DeviceCommand{1, "answer_command", c.DecisionID, c.RunID, c.Revision, test.choice, json.RawMessage(test.value)}
			body, _ := json.Marshal(command)
			e := &Engine{Store: s}
			receipt, err := e.ResolveDevice(token, command, body)
			if err != nil || receipt.State != "accepted" {
				t.Fatal(receipt, err)
			}
			answer, err := s.AgentRequestStatus(session.ID, secret, request.RequestID)
			if err != nil || answer.State != "accepted" || !bytes.Contains(answer.Body, []byte(test.expected)) {
				t.Fatal(string(answer.Body), err)
			}
			run, _ := s.Get(c.RunID)
			if run.Decision.AppliedAt != nil {
				t.Fatal("pipe not acknowledged yet")
			}
			// Delivery failure preserves the exact answer for the same source request.
			if err = s.AgentDelivery(session.ID, secret, request.RequestID, answer.Digest, "application_blocked"); err != nil {
				t.Fatal(err)
			}
			if err = s.Close(); err != nil {
				t.Fatal(err)
			}
			s, err = OpenStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			saved, err := s.AgentRequestStatus(session.ID, secret, request.RequestID)
			if err != nil || saved.State != "application_blocked" || !bytes.Equal(saved.Body, answer.Body) {
				t.Fatal(saved, err)
			}
			if err = s.AgentDelivery(session.ID, secret, request.RequestID, "wrong", "applied"); err == nil {
				t.Fatal("unbound ack")
			}
			if err = s.AgentDelivery(session.ID, secret, request.RequestID, answer.Digest, "applied"); err != nil {
				t.Fatal(err)
			}
			run, _ = s.Get(c.RunID)
			if run.Decision.AppliedAt == nil || run.Status != "done" || run.Decision.ApplicationError != "" {
				t.Fatalf("not delivered: %+v", run.Decision)
			}
			replay, _, err := s.acceptDeviceCommand(token, command, body, false)
			if err != nil || replay != receipt {
				t.Fatal("receipt changed", err)
			}
			if len(s.List()) != 1 {
				t.Fatal("native answer created execution child")
			}
		})
	}
}
func TestAgentSchemaSessionEndAndSourceConflict(t *testing.T) {
	s := testStore(t)
	a, secret, err := s.CreateAgentSession("codex", "Review", t.TempDir(), decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.IngestAgentRequest(a.ID, secret, json.RawMessage(codexQuestion))
	if err != nil {
		t.Fatal(err)
	}
	_, token := pairTestDevice(t, s, decision.LocalOperator())
	snap, err := s.DeviceSnapshot(token)
	if err != nil {
		t.Fatal(err)
	}
	c := snap.Cards[0]
	changed := bytes.ReplaceAll([]byte(codexQuestion), []byte("What name"), []byte("Which name"))
	if _, err = s.IngestAgentRequest(a.ID, secret, changed); err == nil {
		t.Fatal("changed source request accepted")
	}
	seq := s.seq
	command := DeviceCommand{1, "bad_value", c.DecisionID, c.RunID, c.Revision, "answer", json.RawMessage(`"x"`)}
	body, _ := json.Marshal(command)
	if _, _, err = s.acceptDeviceCommand(token, command, body, false); err == nil || s.seq != seq {
		t.Fatal("device skipped schema validation", err)
	}
	if err = s.ResolveChoice(c.RunID, decision.Resolution{DecisionID: c.DecisionID, Choice: "answer", Value: json.RawMessage(`"123"`), Via: "api"}, decision.LocalOperator(), nil); err == nil {
		t.Fatal("console skipped schema validation")
	}
	if err = s.EndAgentThread(a.ID, secret, "thread_1"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AgentRequestStatus(a.ID, secret, view.RequestID); err == nil {
		t.Fatal("ended request still waiting")
	}
	command.Value = json.RawMessage(`"Alpha"`)
	body, _ = json.Marshal(command)
	if _, _, err = s.acceptDeviceCommand(token, command, body, false); err == nil {
		t.Fatal("ended session approved")
	}
	snap, err = s.DeviceSnapshot(token)
	if err != nil || snap.Cards[0].State != "superseded" {
		t.Fatal(snap, err)
	}
	if _, err = s.IngestAgentRequest(a.ID, secret, json.RawMessage(codexApproval)); err == nil {
		t.Fatal("ended source resurrected")
	}
	newThread := bytes.ReplaceAll([]byte(codexApproval), []byte("thread_1"), []byte("thread_2"))
	if _, err = s.IngestAgentRequest(a.ID, secret, newThread); err != nil {
		t.Fatal("new native session blocked", err)
	}
	a = s.agentSessions[a.ID]
	a.LeaseUntil = time.Now().Add(-time.Second)
	s.agentSessions[a.ID] = a
	if err = s.expireAgentSessions(); err != nil {
		t.Fatal(err)
	}
	snap, err = s.DeviceSnapshot(token)
	if err != nil || snap.PendingTotal != 0 {
		t.Fatal("dead adapter remained actionable", snap, err)
	}
}
func TestAgentMultiQuestionAtomicResponse(t *testing.T) {
	s := testStore(t)
	a, token, err := s.CreateAgentSession("codex", "Questions", t.TempDir(), decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	raw := `{"id":"r","method":"item/tool/requestUserInput","params":{"threadId":"t","turnId":"u","itemId":"i","questions":[{"id":"one","question":"First answer?"},{"id":"two","question":"Second answer?"}]}}`
	request, err := s.IngestAgentRequest(a.ID, token, json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range request.RunIDs {
		r, _ := s.Get(id)
		if err = s.ResolveChoice(id, decision.Resolution{DecisionID: r.Decision.ID, Choice: "answer", Value: json.RawMessage(`"Yes"`), Via: "console"}, decision.LocalOperator(), nil); err != nil {
			t.Fatal(err)
		}
		answer, err := s.AgentRequestStatus(a.ID, token, request.RequestID)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && answer.State != "pending" {
			t.Fatal("partial native response")
		}
		if i == 1 && answer.State != "accepted" {
			t.Fatal("missing complete response")
		}
	}
}

func TestAgentOperatorScopeAndRecentSupersession(t *testing.T) {
	s := testStore(t)
	owner := decision.LocalOperator()
	a, secret, err := s.CreateAgentSession("codex", "Review", t.TempDir(), owner)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.IngestAgentRequest(a.ID, secret, json.RawMessage(codexApproval))
	if err != nil {
		t.Fatal(err)
	}
	_, ownerToken := pairTestDevice(t, s, owner)
	other := owner
	other.ID = "another-operator"
	_, otherToken := pairTestDevice(t, s, other)
	snapshot, err := s.DeviceSnapshot(otherToken)
	if err != nil || len(snapshot.Cards) != 0 || len(snapshot.Runs) != 0 {
		t.Fatal("source leaked across operators", snapshot, err)
	}
	r, _ := s.Get(view.RunIDs[0])
	if err = s.ResolveChoice(r.ID, decision.Resolution{DecisionID: r.Decision.ID, Choice: "approve", Via: "console"}, other, nil); err == nil {
		t.Fatal("unoffered operator approved")
	}
	// A long-waiting request must remain in recent outcomes after termination.
	s.runs[r.ID].Decision.RequestedAt = time.Now().Add(-48 * time.Hour)
	if err = s.EndAgentThread(a.ID, secret, "thread_1"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = s.DeviceSnapshot(ownerToken)
	if err != nil || len(snapshot.Cards) != 1 || snapshot.Cards[0].State != "superseded" {
		t.Fatal("recent termination lost", snapshot, err)
	}
	if bytes.Contains([]byte(snapshot.Cards[0].Impact), []byte("Spend: 0")) {
		t.Fatal("native request implied a zero-cost budget")
	}
}
