package studio

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mitos/internal/watch"
)

// An agent member in an adaptive graph, ready for supervision tests.
func supervisedGraph(t *testing.T) (*Store, WorkGraph, string) {
	t.Helper()
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	s := e.Store
	agent := &AgentBinding{Runtime: "codex", Selection: &ExecutionSelection{Harness: "codex", HarnessName: "codex", Traits: TaskTraits{Nature: "implement", Size: "small", Intelligence: "low"}, Budget: SizeBudget{Minutes: 5, MaxTurns: 40}}}
	s.mu.Lock()
	clone := graphClone(g)
	clone.Members["worker"] = &GraphMember{Spec: GraphSpec{ID: "worker", Parent: g.RootTask, Kind: "application", Required: true, Acceptance: "operator", Criteria: "Report 42", Brief: Brief{Title: "Produce answer"}}, Executor: &GraphExecutor{Application: Application{Agent: agent}}, Revision: 1, FactRevision: 1, State: "queued"}
	s.mu.Unlock()
	return s, clone, "worker"
}

// commitSupervision assigns members through the kernel path and commits the
// graph and watch records together, as the dispatch backstop does.
func commitSupervision(t *testing.T, s *Store, g WorkGraph, assigns []string, request map[string]*GraphWatchSelection) WorkGraph {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := graphClone(g)
	wc, err := s.planSupervision(&clone, assigns, nil, request)
	if err != nil {
		t.Fatal(err)
	}
	gc := graphChange{Graph: clone}
	s.projectGraph(&gc, s.graphs[g.ID])
	if err := s.commit("", "effect", "graph_supervision_test", change{Graph: &gc, Watch: wc}); err != nil {
		t.Fatal(err)
	}
	return clone
}

// Every agent task joins the graph's default supervising watch, created from
// the child-terminal-states template; a second member joins the same watch;
// and the kernel ends the watch when its last member settles (#126).
func TestSupervisionDefaultWatchAndSettlement(t *testing.T) {
	s, g, tid := supervisedGraph(t)
	g = commitSupervision(t, s, g, []string{tid}, nil)
	first := g.Members[tid].SupervisionWatch
	if first == "" || g.SupervisionWatch != first {
		t.Fatalf("member not assigned to the default watch: %q graph default %q", first, g.SupervisionWatch)
	}
	// A second agent member joins the same watch: one watch, two members.
	s.mu.Lock()
	clone := graphClone(g)
	clone.Members["second"] = &GraphMember{Spec: GraphSpec{ID: "second", Parent: g.RootTask, Kind: "application", Required: true, Acceptance: "operator", Criteria: "Report 43", Brief: Brief{Title: "Second answer"}}, Executor: clone.Members[tid].Executor, Revision: 1, FactRevision: 1, State: "queued"}
	s.mu.Unlock()
	clone = commitSupervision(t, s, clone, []string{"second"}, nil)
	if second := clone.Members["second"].SupervisionWatch; second != first {
		t.Fatalf("second member joined %q, want the default %q", second, first)
	}
	w := s.watches[first]
	if w == nil || w.Template != "child-terminal-states" || len(w.Membership()) != 2 {
		t.Fatalf("default watch wrong: %+v", w)
	}
	if w.Spec.Owner.Kind != watch.OwnerResponsibility || w.Spec.Owner.ID != g.ID {
		t.Fatalf("supervision watch not owned by the responsibility: %+v", w.Spec.Owner)
	}
	// The kernel ends the watch once every member settles.
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Watch(first); v.State != "active" {
		t.Fatalf("watch ended with live members: %s", v.State)
	}
	s.mu.Lock()
	settled := graphClone(clone)
	settled.Members[tid].CancelRequested = true
	gc := graphChange{Graph: settled}
	s.projectGraph(&gc, s.graphs[settled.ID])
	if err := s.commit("", "effect", "graph_supervision_settled", change{Graph: &gc}); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	// Renewal near expiry, then settlement once the last member settles.
	s.mu.Lock()
	if w := s.watches[first]; w != nil {
		w.Spec.Lease.ExpiresAt = time.Now().Add(10 * time.Minute)
	}
	s.mu.Unlock()
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	last := graphClone(settled)
	last.Members["second"].Accepted = &GraphAcceptance{SpecRevision: 1, At: time.Now().UTC()}
	gc = graphChange{Graph: last}
	s.projectGraph(&gc, s.graphs[last.ID])
	if err := s.commit("", "effect", "graph_supervision_done", change{Graph: &gc}); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Watch(first); v.State != "settled" {
		t.Fatalf("watch not ended after settlement: %s", v.State)
	}
}

// A restart between plan application and the first attempt still finds the
// assignment: it is part of the graph record (#126).
func TestSupervisionAssignmentSurvivesRestart(t *testing.T) {
	s, g, tid := supervisedGraph(t)
	dir := s.Dir()
	g = commitSupervision(t, s, g, []string{tid}, nil)
	assigned := g.Members[tid].SupervisionWatch
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	replayed, err := s.Graph(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if m := replayed.Members[tid]; m == nil || m.SupervisionWatch != assigned {
		t.Fatalf("assignment lost on replay: %+v", m)
	}
	if w := s.watches[assigned]; w == nil || len(w.Membership()) != 1 || w.Membership()[0] != tid {
		t.Fatalf("membership lost on replay: %+v", w)
	}
}

// attempt_overrun and attempt_stalled reach the supervising watch whatever
// its kinds filter says (#126).
func TestAttemptBudgetEventsReachSupervisingWatch(t *testing.T) {
	s, g, tid := supervisedGraph(t)
	g = commitSupervision(t, s, g, []string{tid}, nil)
	supervising := g.Members[tid].SupervisionWatch
	// A second watch of another owner whose kinds filter lists neither
	// event: must-deliver bypasses the filter.
	filtered, err := s.CreateWatch(watch.Spec{
		Version: watch.Version, Owner: watch.Owner{Kind: watch.OwnerOperator}, Description: "unfiltered",
		Condition:    watch.Condition{Scope: watch.Scope{Project: g.ProjectID}, Kinds: []string{"note"}},
		Lease:        watch.Lease{ExpiresAt: time.Now().Add(time.Hour), MaxRenewals: 2},
		Grouping:     watch.Grouping{WaitMS: 0, IntervalMS: 1000, MaxEvents: 64, RepeatIntervalMS: 60000},
		DefaultLevel: watch.Digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := createRun(t, s)
	s.mu.Lock()
	current := s.runs[run.ID]
	current.TaskID = tid
	current.ProjectID = g.ProjectID
	current.GraphAttempt = &GraphAttempt{GraphID: g.ID}
	s.runs[run.ID] = current
	s.mu.Unlock()
	for _, kind := range []string{"attempt_overrun", "attempt_stalled"} {
		if err := s.RecordEvidence(run.ID, kind, "The attempt exceeded its soft budget.", map[string]int{"window": 1}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{supervising, filtered.ID} {
		got := deliveries(t, s, id)
		if len(got) == 0 {
			t.Fatalf("watch %s saw no budget events", id)
		}
		overrun, stalled := false, false
		for _, d := range got {
			if d.Level != watch.Wake {
				t.Fatalf("budget event not delivered at wake: %+v", d)
			}
			overrun = overrun || containsReason(d.Reasons, "attempt_overrun")
			stalled = stalled || containsReason(d.Reasons, "attempt_stalled")
		}
		if !overrun || !stalled {
			t.Fatalf("watch %s missed an event: %+v", id, got)
		}
	}
}

// watch_assign moves a task between supervising watches, and cancelling a
// watch with live members is refused (#126).
func TestWatchAssignMovesMembership(t *testing.T) {
	s, g, tid := supervisedGraph(t)
	g = commitSupervision(t, s, g, []string{tid}, nil)
	first := g.Members[tid].SupervisionWatch
	// A second kernel watch from a template, then the move.
	s.mu.Lock()
	clone := graphClone(g)
	spec, err := watchTemplate("child-terminal-states", g.ID)
	if err != nil {
		t.Fatal(err)
	}
	spec.Condition.Scope.Project = g.ProjectID
	spec.Lease.ExpiresAt = time.Now().Add(watch.MaxLease)
	created := &Watch{ID: newID("watch"), Spec: spec, State: "active", Grant: watch.Scope{Project: g.ProjectID, Graph: g.ID}, Template: "child-terminal-states", CreatedAt: time.Now().UTC(), CreatedSeq: s.seq + 1}
	created.Groups, created.LastDelivered = map[string]*watch.Group{}, map[string]time.Time{}
	if err := s.commit("", "effect", "watch_created", change{Watch: &watchChange{Creates: []*Watch{created}}}); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	s.mu.Lock()
	moved := graphClone(clone)
	wc, err := s.assignSupervisionWatch(&moved, tid, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if wc == nil {
		t.Fatal("move produced no records")
	}
	gc := graphChange{Graph: moved}
	s.projectGraph(&gc, s.graphs[moved.ID])
	if err := s.commit("", "effect", "watch_assignment_test", change{Graph: &gc, Watch: wc}); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	if m := s.watches[first]; m != nil && len(m.Membership()) != 0 {
		t.Fatalf("old watch kept the member: %v", m.Membership())
	}
	if m := s.watches[created.ID]; m == nil || len(m.Membership()) != 1 || m.Membership()[0] != tid {
		t.Fatalf("new watch missing the member: %+v", m)
	}
	// Cancelling a watch with a live member is refused by the tool; the
	// kernel settles it instead.
	s.mu.RLock()
	live := s.watchMembersLive(s.watches[created.ID])
	s.mu.RUnlock()
	if !live {
		t.Fatal("member with an unsettled task not live")
	}
}

// A responsibility watch with no kinds filter and a wake default is clamped
// to notify (#126).
func TestResponsibilityWakeWatchWithoutKindsIsClamped(t *testing.T) {
	s, g, _ := supervisedGraph(t)
	created, err := s.CreateWatch(watch.Spec{
		Version: watch.Version, Owner: watch.Owner{Kind: watch.OwnerResponsibility, ID: g.ID}, Description: "everything",
		Condition:    watch.Condition{Scope: watch.Scope{Graph: g.ID}},
		Lease:        watch.Lease{ExpiresAt: time.Now().Add(time.Hour), MaxRenewals: 2},
		Grouping:     watch.Grouping{WaitMS: 0, IntervalMS: 1000, MaxEvents: 64, RepeatIntervalMS: 60000},
		DefaultLevel: watch.Wake,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Spec.DefaultLevel != watch.Notify {
		t.Fatalf("unfiltered wake watch not clamped: %s", created.Spec.DefaultLevel)
	}
}

// Authors cannot set member tasks; the kernel maintains them (#126).
func TestWatchMembersAreKernelOnly(t *testing.T) {
	s, g, _ := supervisedGraph(t)
	_, err := s.CreateWatch(watch.Spec{
		Version: watch.Version, Owner: watch.Owner{Kind: watch.OwnerResponsibility, ID: g.ID}, Description: "hand-written members",
		Condition:    watch.Condition{Scope: watch.Scope{Graph: g.ID}, Members: []string{"worker"}},
		Lease:        watch.Lease{ExpiresAt: time.Now().Add(time.Hour), MaxRenewals: 2},
		Grouping:     watch.Grouping{WaitMS: 0, IntervalMS: 1000, MaxEvents: 64, RepeatIntervalMS: 60000},
		DefaultLevel: watch.Notify,
	})
	if err == nil || !strings.Contains(err.Error(), "kernel") {
		t.Fatalf("author-set members accepted: %v", err)
	}
}

// Assigning an unknown watch is refused while the plan is being built (#126).
func TestSupervisionAssignUnknownWatchRefused(t *testing.T) {
	s, g, tid := supervisedGraph(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	clone := graphClone(g)
	_, err := s.planSupervision(&clone, []string{tid}, nil, map[string]*GraphWatchSelection{tid: {Assign: "watch_missing"}})
	if err == nil || !strings.Contains(err.Error(), "responsibility") {
		t.Fatalf("unknown watch accepted: %v", err)
	}
}

// The supervision record projects members, leases and unassigned tasks (#126).
func TestSupervisionRecordProjection(t *testing.T) {
	s, g, tid := supervisedGraph(t)
	g = commitSupervision(t, s, g, []string{tid}, nil)
	s.mu.RLock()
	record := s.supervisionRecordLocked(&g)
	s.mu.RUnlock()
	if record.Default != g.SupervisionWatch || len(record.Watches) != 1 || len(record.Watches[0].Members) != 1 || record.Watches[0].Members[0].Task != tid {
		raw, _ := json.Marshal(record)
		t.Fatalf("record wrong: %s", raw)
	}
	if record.Watches[0].Template != "child-terminal-states" || record.Watches[0].DefaultLevel != watch.Notify {
		t.Fatalf("record watch wrong: %+v", record.Watches[0])
	}
}

// Plan rules refuse malformed watch selections in-session (#126).
func TestPlanRulesRefuseMalformedWatchSelections(t *testing.T) {
	plan := func(watchField string) json.RawMessage {
		return json.RawMessage(`{"operations":[{"kind":"add","spec":{"id":"w","parent":"goal","kind":"application","required":true,"acceptance":"operator","criteria":"c","brief":{"title":"t","intent":"i","app_id":"workspace-inventory"}},"watch":` + watchField + `}],"accept":[],"complete":false}`)
	}
	for _, test := range []struct {
		name  string
		watch string
		want  string
	}{
		{"both", `{"assign":"watch_x","create":{"template":"child-terminal-states"}}`, "not both"},
		{"neither", `{}`, "needs assign"},
		{"bad id", `{"assign":"not a watch"}`, "watch.assign"},
		{"empty create", `{"create":{}}`, "watch.create"},
		{"wrong op", `{"assign":"watch_xxxxxxxx"}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			problems := mcpPlanRules(plan(test.watch))
			if test.want == "" {
				for _, p := range problems {
					if strings.Contains(p, "watch") {
						t.Fatalf("unexpected watch problem: %v", problems)
					}
				}
				return
			}
			found := false
			for _, p := range problems {
				if strings.Contains(p, test.want) {
					found = true
				}
			}
			if !found {
				t.Fatalf("problem %q not raised: %v", test.want, problems)
			}
		})
	}
	// watch on an operation that cannot carry it is refused.
	problems := mcpPlanRules(json.RawMessage(`{"operations":[{"kind":"seal","task":"goal","cursor":1,"watch":{"assign":"watch_xxxxxxxx"}}],"accept":[]}`))
	found := false
	for _, p := range problems {
		if strings.Contains(p, "add, supersede and retry") {
			found = true
		}
	}
	if !found {
		t.Fatalf("watch on seal not refused: %v", problems)
	}
}

// The orchestrator's supervision_graph and watch_assign tools read and change
// the record (#126).
func TestSupervisionTools(t *testing.T) {
	e, g, orchestrator, _ := ownerGraph(t)
	s := e.Store
	agent := &AgentBinding{Runtime: "codex", Selection: &ExecutionSelection{Harness: "codex", HarnessName: "codex", Traits: TaskTraits{Nature: "implement", Size: "small", Intelligence: "low"}}}
	s.mu.Lock()
	clone := graphClone(g)
	clone.Members["worker"] = &GraphMember{Spec: GraphSpec{ID: "worker", Parent: g.RootTask, Kind: "application", Required: true, Acceptance: "operator", Criteria: "Report 42", Brief: Brief{Title: "Produce answer"}}, Executor: &GraphExecutor{Application: Application{Agent: agent}}, Revision: 1, FactRevision: 1, State: "queued"}
	s.mu.Unlock()
	g = commitSupervision(t, s, clone, []string{"worker"}, nil)
	grantWatchTools(t, s, orchestrator)
	out, err := e.handoffCall(orchestrator, "", "supervision_graph", json.RawMessage(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), "worker") || !strings.Contains(string(raw), "child-terminal-states") {
		t.Fatalf("supervision_graph view wrong: %s", raw)
	}
	// A watch created through the tool with the same template is returned,
	// not duplicated.
	created, err := e.handoffCall(orchestrator, "", "watch_create", json.RawMessage(`{"owner":"responsibility","template":"child-terminal-states"}`))
	if err != nil {
		t.Fatal(err)
	}
	if view, ok := created.(Watch); !ok || view.ID != g.SupervisionWatch {
		raw, _ := json.Marshal(created)
		t.Fatalf("template creation not deduplicated: %s", raw)
	}
	// Moving the worker to a fresh watch through watch_assign.
	fresh, err := e.handoffCall(orchestrator, "", "watch_create", json.RawMessage(`{"owner":"responsibility","description":"stalls only","condition":{"scope":{},"kinds":["attempt_stalled","attempt_overrun"]},"grouping":{"by":["task_id"],"wait_ms":0,"interval_ms":1000,"max_events":8,"repeat_interval_ms":60000},"lease_ms":3600000,"max_renewals":4,"default_level":"wake"}`))
	if err != nil {
		t.Fatal(err)
	}
	target, _ := fresh.(Watch)
	body, _ := json.Marshal(WatchAssign{Task: "worker", WatchID: target.ID})
	if _, err := e.handoffCall(orchestrator, "", "watch_assign", body); err != nil {
		t.Fatal(err)
	}
	moved, _ := s.Graph(g.ID)
	if m := moved.Members["worker"]; m == nil || m.SupervisionWatch != target.ID {
		t.Fatalf("watch_assign did not move the member: %+v", m)
	}
	if old := s.watches[g.SupervisionWatch]; old == nil || len(old.Membership()) != 0 {
		t.Fatalf("old watch kept the member: %+v", old)
	}
	// Cancelling a watch with a live member is refused; after the move the
	// default watch has no live members and can be cancelled.
	cancel, _ := json.Marshal(WatchCancel{WatchID: target.ID})
	if _, err := e.handoffCall(orchestrator, "", "watch_cancel", cancel); err == nil || !strings.Contains(err.Error(), "live member") {
		t.Fatalf("cancel with live members not refused: %v", err)
	}
	cancel, _ = json.Marshal(WatchCancel{WatchID: g.SupervisionWatch})
	if _, err := e.handoffCall(orchestrator, "", "watch_cancel", cancel); err != nil {
		t.Fatalf("cancel of an empty watch refused: %v", err)
	}
}

// Two planning rounds that each add a task with the same template end with
// one watch and two members; an add without watch joins the default; an add
// naming an unknown watch is refused while the session runs (#126).
func TestAutomaticSupervisionAcrossPlanningRounds(t *testing.T) {
	e, req := automaticScriptEngine(t, `import sys,json,pathlib
prompt=sys.stdin.read();out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
if '--output-schema' in sys.argv:
 ctx=json.loads(section(prompt,'graph_state'));latest={}
 for e in ctx['journal']:latest[e['task']]=e
 workers=[{'id':e['task'],'kind':e['kind'],'state':e['state']} for e in latest.values() if e['kind']=='application']
 p={'explanation':'Add supervised workers.','operations':[],'accept':[],'complete':False}
 def add(i,watch):
  op={'kind':'add','spec':{'id':'w%d'%i,'parent':ctx['root_task'],'kind':'application','required':True,'acceptance':'operator','criteria':'Report %d'%i,'traits':{'nature':'','size':'small','intelligence':'low'},'brief':{'title':'Worker %d'%i,'intent':'Work','app_id':'agent:mitos.generalist','timeout_minutes':30,'scheduling':{'deps':[]}}}}
  if watch is not None:op['watch']=watch
  return op
 if len(workers)==0:p['operations']=[add(0,{'create':{'template':'child-terminal-states'}})]
 elif len(workers)==1:p['operations']=[add(1,{'create':{'template':'child-terminal-states'}})]
 elif len(workers)==2:p['operations']=[add(2,None)]
 else:
  p['accept']=[{'task':w['id'],'rationale':'Reviewed.'} for w in workers];p['complete']=True
  p['result']={'title':'Done','markdown':'Done.','source_tasks':[w['id'] for w in workers]}
 out.write_text(json.dumps(p))
else:
 out.write_text('The answer is 42.')
`)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "supervision-rounds", ProjectID: req.ProjectID, Goal: "Supervised workers"})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(60 * time.Second)
	for time.Now().Before(until) {
		g, _ = e.Store.Graph(g.ID)
		if g.Status == "accepted" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if g.Status != "accepted" {
		t.Fatal("goal did not complete")
	}
	s := e.Store
	s.mu.RLock()
	defer s.mu.RUnlock()
	templates := 0
	for _, w := range s.watches {
		if w.Template == "child-terminal-states" {
			templates++
			if len(w.Membership()) != 3 {
				t.Fatalf("default watch members: %v", w.Membership())
			}
		}
	}
	if templates != 1 {
		t.Fatalf("template watches: %d", templates)
	}
	for _, m := range g.Members {
		if m.OrchestratorFor == "" && m.Spec.Kind == "application" && m.SupervisionWatch != g.SupervisionWatch {
			t.Fatalf("member not on the default watch: %+v", m.Spec.Brief.Title)
		}
	}
}
