package studio

import (
	"encoding/json"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/watch"
)

// A graph attempt run whose agent session is bound to it, inside a real
// active graph so attempt and responsibility owners resolve.
func watchAttemptRun(t *testing.T) (*Store, Run, WorkGraph) {
	t.Helper()
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	s := e.Store
	s.mu.Lock()
	s.runs["owner"] = &Run{ID: "owner", TaskID: "worker", ProjectID: g.ProjectID, Brief: Brief{ProjectID: g.ProjectID, Title: "Worker"}, Status: "running", GraphAttempt: &GraphAttempt{GraphID: g.ID}}
	s.mu.Unlock()
	r, err := s.Get("owner")
	if err != nil {
		t.Fatal(err)
	}
	return s, r, g
}

func containsReason(reasons []string, reason string) bool {
	for _, r := range reasons {
		if r == reason {
			return true
		}
	}
	return false
}

func agentWatchSpec(owner watch.Owner, scope watch.Scope, kinds []string) watch.Spec {
	return watch.Spec{
		Version: watch.Version, Owner: owner, Description: "test watch",
		Condition:    watch.Condition{Scope: scope, Kinds: kinds},
		Lease:        watch.Lease{ExpiresAt: time.Now().Add(time.Hour), MaxRenewals: 2},
		Grouping:     watch.Grouping{WaitMS: 0, IntervalMS: 1000, MaxEvents: 64, RepeatIntervalMS: 60000},
		DefaultLevel: watch.Digest,
	}
}

// An attempt blocked on a human answer is must-deliver: attempt, responsibility
// and project watches each receive the request once at wake, even when their
// kinds filter does not list it, and the answer notifies the same group
// (issue #130).
func TestAgentApprovalRequestReachesEveryWatchScope(t *testing.T) {
	s, r, g := watchAttemptRun(t)
	// The kinds filter deliberately does not list agent_approval_requested.
	attempt := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "attempt", ID: r.ID}, watch.Scope{Run: r.ID}, []string{"note"}))
	responsibility := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "responsibility", ID: g.ID}, watch.Scope{Graph: g.ID}, []string{"note"}))
	project := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "operator"}, watch.Scope{Project: g.ProjectID}, []string{"note"}))
	session, token, err := s.CreateBoundAgentSession("codex", "Attempt question", t.TempDir(), g.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestAgentRequest(session.ID, token, json.RawMessage(codexQuestion)); err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		id   string
		name string
	}{{attempt.ID, "attempt"}, {responsibility.ID, "responsibility"}, {project.ID, "project"}} {
		got := deliveries(t, s, w.id)
		if len(got) != 1 || got[0].Level != watch.Wake || got[0].Count != 1 {
			t.Fatalf("%s watch missed the request: %+v", w.name, got)
		}
		if len(got[0].Reasons) != 1 || got[0].Reasons[0] != "agent_awaiting_answer" {
			t.Fatalf("%s watch reasons: %+v", w.name, got[0].Reasons)
		}
		if got[0].Runs == nil || got[0].Runs[0] != r.ID {
			t.Fatalf("%s watch attribution: %+v", w.name, got[0].Runs)
		}
	}
	// Answering the request notifies the same group so the open item closes.
	_, deviceToken := pairTestDevice(t, s, decision.LocalOperator())
	snap, err := s.DeviceSnapshot(deviceToken)
	if err != nil {
		t.Fatal(err)
	}
	card := snap.Cards[0]
	command := DeviceCommand{1, "answer_command", card.DecisionID, card.RunID, card.Revision, "answer", json.RawMessage(`"Alpha"`)}
	body, err := json.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Store: s}
	if _, err := e.ResolveDevice(deviceToken, command, body); err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, w := range []struct {
		id   string
		name string
	}{{attempt.ID, "attempt"}, {responsibility.ID, "responsibility"}, {project.ID, "project"}} {
		got := deliveries(t, s, w.id)
		if len(got) != 2 || got[0].Level != watch.Notify {
			t.Fatalf("%s watch answer delivery: %+v", w.name, got)
		}
		// A project watch also sees the review run's own parked notice; every
		// scope must at least carry the answer notification.
		if !containsReason(got[0].Reasons, "agent_answered") {
			t.Fatalf("%s watch answer reasons: %+v", w.name, got[0].Reasons)
		}
	}
}

// Ending a request without an answer notifies the same scopes once.
func TestAgentRequestEndNotifiesWatchScopes(t *testing.T) {
	s, r, g := watchAttemptRun(t)
	project := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "operator"}, watch.Scope{Project: g.ProjectID}, nil))
	session, token, err := s.CreateBoundAgentSession("codex", "Attempt question", t.TempDir(), g.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestAgentRequest(session.ID, token, json.RawMessage(codexQuestion)); err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseAgentSession(session.ID, token); err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Deliveries are returned newest first: the end notice, then the question.
	got := deliveries(t, s, project.ID)
	if len(got) != 2 || got[0].Level != watch.Notify || got[1].Level != watch.Wake {
		t.Fatalf("unexpected deliveries: %+v", got)
	}
	if len(got[0].Reasons) != 1 || got[0].Reasons[0] != "agent_request_ended" {
		t.Fatalf("end reasons: %+v", got[0].Reasons)
	}
}

// An unbound interactive session attributes its questions to the review runs
// it opened, so project watches still see them (issue #130).
func TestUnboundAgentRequestReachesProjectWatch(t *testing.T) {
	s, _, g := watchAttemptRun(t)
	project := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "operator"}, watch.Scope{Project: g.ProjectID}, nil))
	session, token, err := s.CreateBoundAgentSession("codex", "Operator session", t.TempDir(), g.ProjectID, "", "", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.IngestAgentRequest(session.ID, token, json.RawMessage(codexQuestion))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, project.ID)
	if len(got) != 1 || got[0].Level != watch.Wake || got[0].Reasons[0] != "agent_awaiting_answer" {
		t.Fatalf("project watch deliveries: %+v", got)
	}
	if len(got[0].Runs) != 1 || got[0].Runs[0] != view.RunIDs[0] {
		t.Fatalf("review run attribution: %+v", got[0].Runs)
	}
}

// The fold is a pure function of the journal: replay rebuilds the same
// undelivered request facts.
func TestAgentRequestWatchFactsSurviveRestart(t *testing.T) {
	s, r, g := watchAttemptRun(t)
	dir := s.Dir()
	// The injected attempt run is not journalled, so after restart the fact
	// attributes to the review runs; a project scope survives either way.
	w := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "operator"}, watch.Scope{Project: g.ProjectID}, nil))
	session, token, err := s.CreateBoundAgentSession("codex", "Attempt question", t.TempDir(), g.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestAgentRequest(session.ID, token, json.RawMessage(codexQuestion)); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err := s.Watch(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v.Pending != 1 || len(v.Groups) != 1 || v.Groups[0].Count != 1 {
		t.Fatalf("replay lost the request fact: %+v", v)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, w.ID)
	if len(got) != 1 || got[0].Level != watch.Wake || got[0].Reasons[0] != "agent_awaiting_answer" {
		t.Fatalf("deliveries after restart: %+v", got)
	}
}
