package studio

import (
	"encoding/json"
	"errors"
	"mitos/internal/agentcli"
	"mitos/internal/cooperation"
	"mitos/internal/desk"
	"strings"
	"testing"
	"time"
)

func TestCooperationSnapshotAndRequestLifecycle(t *testing.T) {
	e, req := graphEngine(t, false)
	doc, err := e.SaveCooperation(req.ProjectID, "missing", "Publish evidence before handoff.")
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := e.freezeSetup(req.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.SaveCooperation(req.ProjectID, doc.Hash, "Updated guidance."); err != nil {
		t.Fatal(err)
	}
	if frozen.Cooperation.Text != doc.Text {
		t.Fatal("snapshot changed")
	}
	s := e.Store
	s.mu.Lock()
	s.tasks["task"] = &Task{ID: "task", ProjectID: req.ProjectID, Revision: 1}
	s.runs["run"] = &Run{ID: "run", ProjectID: req.ProjectID, TaskID: "task", Status: "running"}
	s.mu.Unlock()
	expiry := time.Now().Add(time.Hour)
	m := cooperation.Message{Version: cooperation.Version, ID: "q", TaskID: "task", RunID: "run", Revision: 1, Kind: "question", Body: "Choose", ExpiresAt: &expiry}
	a, err := e.SubmitCooperation(req.ProjectID, m)
	if err != nil {
		t.Fatal(err)
	}
	b, err := e.SubmitCooperation(req.ProjectID, m)
	if err != nil || a.Digest != b.Digest {
		t.Fatal(b, err)
	}
	m.Body = "Different"
	if _, err = e.SubmitCooperation(req.ProjectID, m); !errors.Is(err, ErrConflict) {
		t.Fatal("conflict accepted", err)
	}
	answer := cooperation.Message{Version: cooperation.Version, ID: "a", TaskID: "task", RunID: "run", Revision: 1, Kind: "answer", InReplyTo: "q", Body: "First"}
	if _, err = e.SubmitCooperation(req.ProjectID, answer); err != nil {
		t.Fatal(err)
	}
	answer.ID = "a2"
	if _, err = e.SubmitCooperation(req.ProjectID, answer); !errors.Is(err, ErrConflict) {
		t.Fatal("second answer accepted", err)
	}
	answer.ID = "a3"
	answer.Revision = 2
	if _, err = e.SubmitCooperation(req.ProjectID, answer); !errors.Is(err, ErrConflict) {
		t.Fatal("wrong revision accepted", err)
	}
	if len(s.Cooperation(req.ProjectID)) != 2 {
		t.Fatal("duplicate effects")
	}
}

func TestCooperationEvidenceRetainsNewestWithinByteBudget(t *testing.T) {
	messages := []CooperationRecord{}
	for _, id := range []string{"old", "middle", "new"} {
		messages = append(messages, CooperationRecord{Message: cooperation.Message{ID: id, Body: strings.Repeat("x", 16000)}})
	}
	raw := cooperationEvidence(messages)
	var got []CooperationRecord
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(raw) > 32<<10 || len(got) == 0 || len(got) >= 3 || got[len(got)-1].Message.ID != "new" {
		t.Fatal(len(raw), len(got))
	}
	messages = messages[2:]
	messages[0].Message.Body = strings.Repeat("<", 16000)
	raw = cooperationEvidence(messages)
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if len(raw) > 32<<10 || len(got) != 1 || got[0].Message.ID != "new" || !strings.Contains(got[0].Message.Body, "Evidence excerpt") {
		t.Fatal(len(raw), got)
	}
	if len(messages[0].Message.Body) != 16000 {
		t.Fatal("source evidence mutated")
	}
}
func TestCooperationSessionRetryAfterStopReturnsOriginalReceipt(t *testing.T) {
	e, req := graphEngine(t, true)
	interactiveFixture(t, e)
	task, err := e.Desk.CreateWork(req.ProjectID, desk.NewWork{Key: "cooperation", Title: "Check evidence", Intent: "Inspect evidence", Criteria: "Report findings", Role: "agent"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := e.Desk.StartAgent(req.ProjectID, agentcli.Request{AgentID: "worker", Prompt: task.Intent, ExpectedRevision: task.Revision, Assignment: &agentcli.Assignment{TaskID: task.ID}}, "http://localhost")
	if err != nil {
		t.Fatal(err)
	}
	common, err := e.Store.Task(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	m := cooperation.Message{Version: cooperation.Version, ID: "observation", TaskID: task.ID, SessionID: session.ID, Revision: common.Revision, Kind: "observation", Body: "Recorded evidence"}
	first, err := e.SubmitCooperation(req.ProjectID, m)
	if err != nil {
		t.Fatal(err)
	}
	stopInteractive(t, e.Desk, session.ID)
	again, err := e.SubmitCooperation(req.ProjectID, m)
	if err != nil || graphDigest(first) != graphDigest(again) {
		t.Fatal(again, err)
	}
	m.Body = "conflict"
	if _, err = e.SubmitCooperation(req.ProjectID, m); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	m.ID = "new"
	if _, err = e.SubmitCooperation(req.ProjectID, m); !errors.Is(err, ErrConflict) {
		t.Fatal("stopped origin published new evidence", err)
	}
}
