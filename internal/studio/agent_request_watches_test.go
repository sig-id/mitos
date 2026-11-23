package studio

import (
	"context"
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

func agentWatchSpec(owner watch.Owner, scope watch.Scope, kinds []string) watch.Spec {
	return watch.Spec{
		Version: watch.Version, Owner: owner, Description: "test watch",
		Condition:    watch.Condition{Scope: scope, Kinds: kinds},
		Lease:        watch.Lease{ExpiresAt: time.Now().Add(time.Hour), MaxRenewals: 2},
		Grouping:     watch.Grouping{WaitMS: 0, IntervalMS: 1000, MaxEvents: 64, RepeatIntervalMS: 60000},
		DefaultLevel: watch.Digest,
	}
}

func routeAgentTestRequest(t *testing.T, s *Store, session AgentSession, token, raw string) AgentRequestView {
	t.Helper()
	view, err := s.IngestAgentRequest(session.ID, token, json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	// Routing decides who answers; the default policy has no decision
	// provider, so the classifier falls back to a human.
	e := &Engine{Store: s}
	route, err := e.RouteAgentRequest(context.Background(), session.ID, token, view.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if route.Route.Resolver != "human" {
		t.Fatalf("expected a human resolver, got %+v", route.Route)
	}
	return view
}

// An attempt blocked on a human answer is must-deliver: attempt, responsibility
// and project watches each receive the request once at wake, even when their
// kinds filter does not list it, and the answer notifies the same group
// (issue #130).
func TestAgentApprovalRequestReachesEveryWatchScope(t *testing.T) {
	s, r, g := watchAttemptRun(t)
	// The kinds filter deliberately does not list the request events.
	attempt := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "attempt", ID: r.ID}, watch.Scope{Run: r.ID}, []string{"note"}))
	responsibility := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "responsibility", ID: g.ID}, watch.Scope{Graph: g.ID}, []string{"note"}))
	project := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "operator"}, watch.Scope{Project: g.ProjectID}, []string{"note"}))
	session, token, err := s.CreateBoundAgentSession("codex", "Attempt question", t.TempDir(), g.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	routeAgentTestRequest(t, s, session, token, codexQuestion)
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
		if len(got[0].Reasons) != 1 || got[0].Reasons[0] != "agent_answered" {
			t.Fatalf("%s watch answer reasons: %+v", w.name, got[0].Reasons)
		}
	}
}

// A request automatic review can answer wakes nobody at ingest, and wakes its
// scopes when routing falls back to a human (issue #130).
func TestAutomaticResolutionDoesNotWakeUntilRoutingLandsOnHuman(t *testing.T) {
	s, r, g := watchAttemptRun(t)
	project := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "operator"}, watch.Scope{Project: g.ProjectID}, []string{"note"}))
	session, token, err := s.CreateBoundAgentSession("codex", "Attempt question", t.TempDir(), g.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	// Ingest alone stays quiet: the default delegated policy classifies the
	// question before anyone is needed.
	view, err := s.IngestAgentRequest(session.ID, token, json.RawMessage(codexQuestion))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := deliveries(t, s, project.ID); len(got) != 0 {
		t.Fatalf("ingest woke scopes before routing: %+v", got)
	}
	e := &Engine{Store: s}
	if _, err := e.RouteAgentRequest(context.Background(), session.ID, token, view.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, project.ID)
	if len(got) != 1 || got[0].Level != watch.Wake || got[0].Reasons[0] != "agent_awaiting_answer" || got[0].Runs[0] != r.ID {
		t.Fatalf("routed request did not wake the project watch: %+v", got)
	}
}

// A command approval no rule delegated authority for needs a human by policy,
// so it wakes at ingest without waiting for routing (issue #130).
func TestUndelegatedCommandApprovalWakesAtIngest(t *testing.T) {
	s, r, g := watchAttemptRun(t)
	project := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "operator"}, watch.Scope{Project: g.ProjectID}, []string{"note"}))
	session, token, err := s.CreateBoundAgentSession("codex", "Command approval", t.TempDir(), g.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.IngestAgentRequest(session.ID, token, json.RawMessage(codexApproval)); err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, project.ID)
	if len(got) != 1 || got[0].Level != watch.Wake || got[0].Reasons[0] != "agent_awaiting_answer" {
		t.Fatalf("undelegated approval did not wake at ingest: %+v", got)
	}
}

// Ending a request without an answer notifies the same scopes once, and a
// session that stays open after a thread ends does not repeat the notice.
func TestAgentRequestEndNotifiesWatchScopesOnce(t *testing.T) {
	s, r, g := watchAttemptRun(t)
	project := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "operator"}, watch.Scope{Project: g.ProjectID}, []string{"note"}))
	session, token, err := s.CreateBoundAgentSession("codex", "Attempt question", t.TempDir(), g.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	routeAgentTestRequest(t, s, session, token, codexQuestion)
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.EndAgentThread(session.ID, token, "thread_1"); err != nil {
		t.Fatal(err)
	}
	// Later session traffic must not repeat the end notice.
	if err := s.AgentHeartbeat(session.ID, token); err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Deliveries are returned newest first: the end notice, then the wake.
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
	project := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "operator"}, watch.Scope{Project: g.ProjectID}, []string{"note"}))
	session, token, err := s.CreateBoundAgentSession("codex", "Operator session", t.TempDir(), g.ProjectID, "", "", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	view := routeAgentTestRequest(t, s, session, token, codexQuestion)
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

// A watch grouped by run receives the attributed answer notification: the
// review run's own parked fact must not consume it by sequence dedup
// (issue #130).
func TestAnswerNotificationSurvivesRunGroupingDedup(t *testing.T) {
	s, r, g := watchAttemptRun(t)
	byRun := mustWatch(t, s, watch.Spec{
		Version: watch.Version, Owner: watch.Owner{Kind: "operator"}, Description: "test watch",
		Condition:    watch.Condition{Scope: watch.Scope{Project: g.ProjectID}},
		Lease:        watch.Lease{ExpiresAt: time.Now().Add(time.Hour), MaxRenewals: 2},
		Grouping:     watch.Grouping{By: []string{"run_id"}, WaitMS: 0, IntervalMS: 1000, MaxEvents: 64, RepeatIntervalMS: 60000},
		DefaultLevel: watch.Digest,
	})
	session, token, err := s.CreateBoundAgentSession("codex", "Attempt question", t.TempDir(), g.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	routeAgentTestRequest(t, s, session, token, codexQuestion)
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
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
	got := deliveries(t, s, byRun.ID)
	if len(got) != 2 || got[0].Level != watch.Notify || !containsReason(got[0].Reasons, "agent_answered") {
		t.Fatalf("run-grouped answer delivery: %+v", got)
	}
	if got[0].Runs[0] != r.ID {
		t.Fatalf("answer not attributed to the blocked attempt: %+v", got[0].Runs)
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
	routeAgentTestRequest(t, s, session, token, codexQuestion)
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
	// The unrouted ingest fact and the routed wake both wait for delivery.
	if v.Pending != 2 || len(v.Groups) != 1 {
		t.Fatalf("replay lost the request facts: %+v", v)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, w.ID)
	if len(got) != 1 || got[0].Level != watch.Wake || !containsReason(got[0].Reasons, "agent_awaiting_answer") {
		t.Fatalf("deliveries after restart: %+v", got)
	}
}

func containsReason(reasons []string, reason string) bool {
	for _, r := range reasons {
		if r == reason {
			return true
		}
	}
	return false
}

const codexMultiQuestion = `{"id":9,"method":"item/tool/requestUserInput","params":{"threadId":"thread_1","turnId":"turn_9","itemId":"item_9","questions":[{"id":"one","question":"First answer?"},{"id":"two","question":"Second answer?"}]}}`

// A request whose ingest projection already required a human is not woken
// again when routing records the same human resolver: the two events share no
// sequence, so a second wake could not be deduplicated away (issue #130
// review).
func TestRoutingDoesNotRepeatAnIngestWake(t *testing.T) {
	s, r, g := watchAttemptRun(t)
	project := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "operator"}, watch.Scope{Project: g.ProjectID}, []string{"note"}))
	session, token, err := s.CreateBoundAgentSession("codex", "Command approval", t.TempDir(), g.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	// An undelegated command approval needs a human by policy at ingest.
	view, err := s.IngestAgentRequest(session.ID, token, json.RawMessage(codexApproval))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, project.ID)
	if len(got) != 1 || got[0].Level != watch.Wake || got[0].Reasons[0] != "agent_awaiting_answer" {
		t.Fatalf("ingest wake missing: %+v", got)
	}
	e := &Engine{Store: s}
	route, err := e.RouteAgentRequest(context.Background(), session.ID, token, view.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if route.Route.Resolver != "human" {
		t.Fatalf("expected a human resolver, got %+v", route.Route)
	}
	if err := s.EvaluateWatches(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if got = deliveries(t, s, project.ID); len(got) != 1 {
		t.Fatalf("routing repeated the ingest wake: %+v", got)
	}
}

// An unbound multi-question request is one fact per journal event, attributed
// to its first review run: per-run facts would share one sequence, and a
// run-grouped watch's first delivery would consume the others (issue #130
// review).
func TestUnboundMultiQuestionRequestIsOneRunGroup(t *testing.T) {
	s, _, g := watchAttemptRun(t)
	byRun := mustWatch(t, s, watch.Spec{
		Version: watch.Version, Owner: watch.Owner{Kind: "operator"}, Description: "test watch",
		Condition:    watch.Condition{Scope: watch.Scope{Project: g.ProjectID}},
		Lease:        watch.Lease{ExpiresAt: time.Now().Add(time.Hour), MaxRenewals: 2},
		Grouping:     watch.Grouping{By: []string{"run_id"}, WaitMS: 0, IntervalMS: 1000, MaxEvents: 64, RepeatIntervalMS: 60000},
		DefaultLevel: watch.Digest,
	})
	session, token, err := s.CreateBoundAgentSession("codex", "Operator session", t.TempDir(), g.ProjectID, "", "", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	view := routeAgentTestRequest(t, s, session, token, codexMultiQuestion)
	if len(view.RunIDs) != 2 {
		t.Fatalf("expected two review runs, got %+v", view.RunIDs)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// One wake for the whole request, in one run group; the second question's
	// group must not have been silently consumed.
	got := deliveries(t, s, byRun.ID)
	if len(got) != 1 || got[0].Level != watch.Wake || got[0].Reasons[0] != "agent_awaiting_answer" {
		t.Fatalf("wake delivery: %+v", got)
	}
	if len(got[0].Runs) != 1 || got[0].Runs[0] != view.RunIDs[0] {
		t.Fatalf("wake attribution: %+v", got[0].Runs)
	}
	if v, _ := s.Watch(byRun.ID); v.Pending != 0 {
		t.Fatalf("undelivered groups remain: %+v", v.Groups)
	}
}

// A request ingested under automatic review still wakes when the operator
// switches the policy to manual before routing: the routing event compares
// against the wake actually delivered, not a projection recomputed under the
// current policy (issue #130 review).
func TestPolicyChangeToManualStillWakesOnRouting(t *testing.T) {
	s, r, g := watchAttemptRun(t)
	project := mustWatch(t, s, agentWatchSpec(watch.Owner{Kind: "operator"}, watch.Scope{Project: g.ProjectID}, []string{"note"}))
	session, token, err := s.CreateBoundAgentSession("codex", "Attempt question", t.TempDir(), g.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	e, err := NewEngine(s, nil, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(e.Close)
	// Delegated autonomy with no decision provider classifies at ingest, so
	// ingest alone wakes nobody.
	view, err := s.IngestAgentRequest(session.ID, token, json.RawMessage(codexQuestion))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := wakeDeliveries(t, s, project.ID); len(got) != 0 {
		t.Fatalf("ingest woke scopes before routing: %+v", got)
	}
	// The operator switches to manual review before the request is routed.
	p := s.AgentPolicy(g.ProjectID)
	p.Autonomy = "manual"
	if _, err := e.SaveAgentPolicy(g.ProjectID, p); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RouteAgentRequest(context.Background(), session.ID, token, view.RequestID); err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := wakeDeliveries(t, s, project.ID)
	if len(got) != 1 || got[0].Level != watch.Wake || got[0].Runs[0] != r.ID {
		t.Fatalf("policy change lost the wake: %+v", got)
	}
}

// wakeDeliveries filters a watch's receipts to agent_awaiting_answer wakes.
func wakeDeliveries(t *testing.T, s *Store, id string) []WatchDelivery {
	t.Helper()
	out := []WatchDelivery{}
	for _, d := range deliveries(t, s, id) {
		if containsReason(d.Reasons, "agent_awaiting_answer") {
			out = append(out, d)
		}
	}
	return out
}

// Closing a session with several outstanding requests delivers one
// notification carrying every affected run: the per-request facts share the
// closing event's sequence, and a run-grouped watch's first delivery would
// otherwise consume the others (issue #130 review).
func TestSessionCloseWithSeveralRequestsDeliversEveryRun(t *testing.T) {
	s, _, g := watchAttemptRun(t)
	byRun := mustWatch(t, s, watch.Spec{
		Version: watch.Version, Owner: watch.Owner{Kind: "operator"}, Description: "test watch",
		Condition:    watch.Condition{Scope: watch.Scope{Project: g.ProjectID}},
		Lease:        watch.Lease{ExpiresAt: time.Now().Add(time.Hour), MaxRenewals: 2},
		Grouping:     watch.Grouping{By: []string{"run_id"}, WaitMS: 0, IntervalMS: 1000, MaxEvents: 64, RepeatIntervalMS: 60000},
		DefaultLevel: watch.Digest,
	})
	session, token, err := s.CreateBoundAgentSession("codex", "Operator session", t.TempDir(), g.ProjectID, "", "", decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	first := routeAgentTestRequest(t, s, session, token, codexQuestion)
	second := routeAgentTestRequest(t, s, session, token, codexMultiQuestion)
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := deliveries(t, s, byRun.ID); len(got) != 2 {
		t.Fatalf("expected one wake per request: %+v", got)
	}
	// Closing the session ends both requests in one journal event.
	if err := s.CloseAgentSession(session.ID, token); err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, byRun.ID)
	if len(got) != 3 || got[0].Level != watch.Notify || !containsReason(got[0].Reasons, "agent_request_ended") {
		t.Fatalf("session close delivery: %+v", got)
	}
	if !containsID(got[0].Runs, first.RunIDs[0]) || !containsID(got[0].Runs, second.RunIDs[0]) {
		t.Fatalf("closed requests missing from the delivery: %+v", got[0].Runs)
	}
	if v, _ := s.Watch(byRun.ID); v.Pending != 0 {
		t.Fatalf("undelivered groups remain: %+v", v.Groups)
	}
}

func containsID(ids []string, id string) bool {
	for _, v := range ids {
		if v == id {
			return true
		}
	}
	return false
}
