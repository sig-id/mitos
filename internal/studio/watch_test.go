package studio

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"mitos/internal/watch"
)

func runWatch(runID string, kinds []string, level watch.Level, wait int64) watch.Spec {
	return scopeWatch(watch.Scope{Run: runID}, kinds, level, wait)
}

func scopeWatch(scope watch.Scope, kinds []string, level watch.Level, wait int64) watch.Spec {
	return watch.Spec{
		Version: watch.Version, Owner: watch.Owner{Kind: "operator"}, Description: "test watch",
		Condition:    watch.Condition{Scope: scope, Kinds: kinds},
		Lease:        watch.Lease{ExpiresAt: time.Now().Add(time.Hour), MaxRenewals: 2},
		Grouping:     watch.Grouping{WaitMS: wait, IntervalMS: 1000, MaxEvents: 64, RepeatIntervalMS: 60000},
		DefaultLevel: level,
	}
}

func mustWatch(t *testing.T, s *Store, spec watch.Spec) Watch {
	t.Helper()
	w, err := s.CreateWatch(spec)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func evidence(t *testing.T, s *Store, run, kind string) {
	t.Helper()
	if err := s.RecordEvidence(run, kind, "observed "+kind, map[string]string{"kind": kind}); err != nil {
		t.Fatal(err)
	}
}

func deliveries(t *testing.T, s *Store, id string) []WatchDelivery {
	t.Helper()
	out, err := s.WatchDeliveries(id, 100)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWatchGroupsAndDeliversEachEventOnce(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	w := mustWatch(t, s, runWatch(r.ID, []string{"note"}, watch.Notify, 0))
	evidence(t, s, r.ID, "note")
	evidence(t, s, r.ID, "unrelated")
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, w.ID)
	if len(got) != 1 || got[0].Count != 2 || got[0].Counts["note"] != 2 || got[0].Level != watch.Notify {
		t.Fatalf("deliveries %+v", got)
	}
	if v, _ := s.Watch(w.ID); v.Pending != 0 || v.Cursor != s.seq {
		t.Fatalf("cursor did not advance past delivered events: %+v", v)
	}
}

func TestWatchRestartBetweenMatchAndReceiptLosesAndDuplicatesNothing(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := createRun(t, s)
	w := mustWatch(t, s, runWatch(r.ID, []string{"note"}, watch.Notify, 0))
	evidence(t, s, r.ID, "note")
	evidence(t, s, r.ID, "note")
	_ = s.Close()

	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := s.Watch(w.ID); v.Pending != 2 {
		t.Fatalf("replay lost admitted events: %+v", v)
	}
	if err = s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	before := deliveries(t, s, w.ID)
	if len(before) != 1 || before[0].Count != 2 {
		t.Fatalf("deliveries %+v", before)
	}
	_ = s.Close()

	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	after := deliveries(t, s, w.ID)
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatalf("replay changed deliveries:\n%s\n%s", a, b)
	}
}

func TestWatchDeliversCrashedRunOutsideItsCondition(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	w := mustWatch(t, s, runWatch(r.ID, []string{"verification_failed"}, watch.Notify, 0))
	if err := s.Transition(r.ID, "run_failed", "failed", "Failed", "The worker crashed."); err != nil {
		t.Fatal(err)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, w.ID)
	if len(got) != 1 || !reflect.DeepEqual(got[0].Reasons, []string{"run_failed"}) {
		t.Fatalf("a crash was silent: %+v", got)
	}
}

func TestOverlappingWatchesDeliverOnceAtTheHighestLevel(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	low := mustWatch(t, s, runWatch(r.ID, nil, watch.Notify, 0))
	high := mustWatch(t, s, runWatch(r.ID, []string{"note"}, watch.Escalate, 0))
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	all := append(deliveries(t, s, low.ID), deliveries(t, s, high.ID)...)
	if len(all) != 1 || all[0].Level != watch.Escalate || len(all[0].Watches) != 2 {
		t.Fatalf("owner received an event more than once or at the wrong level: %+v", all)
	}
	if items := s.OperatorWatchItems(); len(items) != 1 {
		t.Fatalf("escalation did not open one operator item: %+v", items)
	}
}

func TestEscalationsCoalesceIntoOneOperatorItem(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	w := mustWatch(t, s, runWatch(r.ID, []string{"note"}, watch.Escalate, 0))
	now := time.Now()
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	items := s.OperatorWatchItems()
	if len(items) != 1 || items[0].Count != 2 || len(items[0].Deliveries) != 2 {
		t.Fatalf("repeated escalation opened a second item: %+v", items)
	}
	// Both receipts stay separate records that name the one item.
	got := deliveries(t, s, w.ID)
	if len(got) != 2 || got[0].ItemID != items[0].ID || got[1].ItemID != items[0].ID || got[0].Route != "feed" || got[0].Count != 1 {
		t.Fatalf("receipts lost their identity in the item: %+v", got)
	}
	first := items[0].ID
	// After repeat_interval_ms a new item replaces the still-open one.
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	items = s.OperatorWatchItems()
	if len(items) != 1 || items[0].Supersedes != first || items[0].Count != 1 {
		t.Fatalf("repeat interval did not supersede the item: %+v", items)
	}
	if all, _ := s.WatchItems(w.ID); len(all) != 2 || all[1].ID != first || all[1].State != "superseded" {
		t.Fatalf("superseded item not kept: %+v", all)
	}
	if err := s.ResolveWatchItem(items[0].ID); err != nil {
		t.Fatal(err)
	}
	if len(s.OperatorWatchItems()) != 0 {
		t.Fatal("resolved item still open")
	}
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(now.Add(3 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	if items = s.OperatorWatchItems(); len(items) != 1 || items[0].Supersedes != "" {
		t.Fatalf("escalation after resolution did not open a fresh item: %+v", items)
	}
}

func TestWatchLeaseExpiryReportsWaitingCount(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	spec := runWatch(r.ID, []string{"note"}, watch.Notify, 60000)
	w := mustWatch(t, s, spec)
	evidence(t, s, r.ID, "note")
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(time.Now().Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	v, _ := s.Watch(w.ID)
	got := deliveries(t, s, w.ID)
	if v.State != "expired" || len(got) != 1 || got[0].Kind != "expired" || got[0].Count != 2 {
		t.Fatalf("expiry %+v deliveries %+v", v, got)
	}
	if _, err := s.RenewWatch(w.ID, time.Now().Add(time.Hour)); err != ErrConflict {
		t.Fatalf("expired watch renewed: %v", err)
	}
}

func TestWatchRenewalIsBounded(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	w := mustWatch(t, s, runWatch(r.ID, nil, watch.Notify, 0))
	for i := 0; i < 2; i++ {
		if _, err := s.RenewWatch(w.ID, time.Now().Add(2*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.RenewWatch(w.ID, time.Now().Add(2*time.Hour)); err != ErrConflict {
		t.Fatalf("renewal past max_renewals: %v", err)
	}
}

func TestWatchReportsSilenceOncePerQuietPeriod(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	spec := runWatch(r.ID, []string{"note"}, watch.Notify, 0)
	spec.MaxSilenceMS = 60000
	w := mustWatch(t, s, spec)
	later := time.Now().Add(61 * time.Second)
	for i := 0; i < 2; i++ {
		if err := s.EvaluateWatches(later); err != nil {
			t.Fatal(err)
		}
	}
	got := deliveries(t, s, w.ID)
	if len(got) != 1 || got[0].Kind != "silent" {
		t.Fatalf("silence %+v", got)
	}
}

func TestWatchRules(t *testing.T) {
	r := &Run{ID: "r"}
	block, _ := json.Marshal(map[string]string{"threshold": "block"})
	steer, _ := json.Marshal(map[string]string{"threshold": "steer"})
	cases := []struct {
		kind  string
		c     change
		run   *Run
		level watch.Level
	}{
		{"quiescent", change{Evidence: block}, r, watch.Wake},
		{"quiescent", change{Evidence: steer}, r, ""},
		{"observation_failed", change{}, r, watch.Escalate},
		{"judgment_held", change{Status: "parked", Judgment: &KernelJudgment{}}, r, watch.Notify},
		{"parked", change{Status: "parked"}, &Run{Decision: &HumanDecision{ReasonSignal: "model_call_limit"}}, watch.Wake},
		{"parked", change{Status: "parked"}, &Run{Decision: &HumanDecision{ReasonSignal: "dependency_inputs_changed"}}, watch.Notify},
		{"run_finished", change{Status: "done"}, r, watch.Notify},
	}
	for _, tc := range cases {
		if level, _ := watchRule(Event{Kind: tc.kind}, tc.run, tc.c); level != tc.level {
			t.Errorf("%s %+v: level %q, want %q", tc.kind, tc.c.Status, level, tc.level)
		}
	}
}

func TestWatchCreationRefusesUnsupportedOwnersAndInvalidTriage(t *testing.T) {
	s := testStore(t)
	spec := runWatch("run_x", nil, watch.Notify, 0)
	spec.Owner = watch.Owner{Kind: "responsibility", ID: "responsibility_1"}
	if _, err := s.CreateWatch(spec); err == nil {
		t.Fatal("a responsibility owner without an active graph was accepted")
	}
	spec = runWatch("run_x", nil, watch.Notify, 0)
	spec.Triage = &watch.Triage{}
	if _, err := s.CreateWatch(spec); err == nil {
		t.Fatal("triage without instructions accepted")
	}
}

func TestEngineLoopDeliversWithoutExplicitEvaluation(t *testing.T) {
	_, s := testEngine(t, t.TempDir(), nil)
	r := createRun(t, s)
	w := mustWatch(t, s, runWatch(r.ID, []string{"note"}, watch.Notify, 0))
	evidence(t, s, r.ID, "note")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(deliveries(t, s, w.ID)) == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("engine loop never delivered the group")
}

func TestWatchDeliverySnapshotsAreDetachedFromTheStore(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	w := mustWatch(t, s, runWatch(r.ID, []string{"note"}, watch.Escalate, 0))
	now := time.Now()
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	items, receipts := s.OperatorWatchItems(), deliveries(t, s, w.ID)
	if len(items) != 1 || len(receipts) != 1 || items[0].Deliveries[0] != receipts[0].ID {
		t.Fatalf("items %+v receipts %+v", items, receipts)
	}
	// A coalescing delivery mutates the stored item's Counts in place; the
	// snapshots already handed out must not follow it.
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(now.Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if items[0].Counts["note"] != 1 || items[0].Count != 1 || len(items[0].Deliveries) != 1 {
		t.Fatalf("item snapshot aliases live store state: %+v", items[0])
	}
	if receipts[0].Counts["note"] != 1 {
		t.Fatalf("receipt snapshot aliases live store state: %+v", receipts[0])
	}
	fresh := s.OperatorWatchItems()
	if len(fresh) != 1 || fresh[0].Count != 2 || len(fresh[0].Deliveries) != 2 || fresh[0].Counts["note"] != 2 {
		t.Fatalf("coalescing update did not land: %+v", fresh[0])
	}
}

func TestContinuousUpdatesDoNotPostponeDeliveryPastInterval(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	spec := runWatch(r.ID, []string{"observation_failed", "note"}, watch.Notify, 60000)
	spec.Grouping.IntervalMS = 1000
	w := mustWatch(t, s, spec)
	now := time.Now()
	// A must-deliver rule delivers the first group immediately, before wait_ms.
	evidence(t, s, r.ID, "observation_failed")
	if err := s.EvaluateWatches(now); err != nil {
		t.Fatal(err)
	}
	if got := deliveries(t, s, w.ID); len(got) != 1 {
		t.Fatalf("rule group was not delivered immediately: %+v", got)
	}
	// Updates that continue afterwards deliver at interval_ms, not once per
	// wait_ms, even though the newest entry is far younger than wait_ms.
	evidence(t, s, r.ID, "note")
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, w.ID)
	if len(got) != 2 || got[0].Counts["note"] != 2 {
		t.Fatalf("continuous updates were postponed past interval_ms: %+v", got)
	}
}

func TestKernelJudgmentHoldCreatesNoWatchItem(t *testing.T) {
	e, r, a, token, _ := judgmentFixture(t, nil)
	s := e.Store
	plain := mustWatch(t, s, runWatch(r.ID, nil, watch.Notify, 0))
	urgent := mustWatch(t, s, runWatch(r.ID, nil, watch.Escalate, 0))
	if _, err := submit(t, e, a, token, blockingSubmission("watch")); err != nil {
		t.Fatal(err)
	}
	// The judgment already has its operator card: the hold's notice reaches the
	// owner at notify and opens no second Notifications item, even for watches
	// whose default is escalate (spec 09 §3).
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := append(deliveries(t, s, urgent.ID), deliveries(t, s, plain.ID)...)
	if len(got) != 1 || got[0].Level != watch.Notify || !reflect.DeepEqual(got[0].Reasons, []string{"judgment_parked"}) {
		t.Fatalf("judgment hold was not delivered once at notify: %+v", got)
	}
	if items := s.OperatorWatchItems(); len(items) != 0 {
		t.Fatalf("judgment hold opened a second operator item: %+v", items)
	}
}

func TestProjectWatchMatchesRunsByBriefProject(t *testing.T) {
	s := testStore(t)
	brief := testBrief(".")
	brief.ProjectID = "project_brief"
	r, err := s.Create(brief, "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	w := mustWatch(t, s, scopeWatch(watch.Scope{Project: "project_brief"}, nil, watch.Notify, 0))
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, w.ID)
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("a run scoped only by its brief project was not delivered: %+v", got)
	}
}

func TestWatchObservesGraphEventsWithoutRuns(t *testing.T) {
	e, req := graphEngine(t, false)
	s := e.Store
	w := mustWatch(t, s, scopeWatch(watch.Scope{Project: req.ProjectID}, nil, watch.Notify, 0))
	createGraphTest(t, e, req)
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got := deliveries(t, s, w.ID)
	if len(got) != 1 || got[0].Counts["graph_created"] != 1 {
		t.Fatalf("a graph-level event was never observed: %+v", got)
	}
}

func TestOverlappingWatchesShareDeliveryTiming(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	a := mustWatch(t, s, runWatch(r.ID, []string{"note"}, watch.Notify, 0))
	b := mustWatch(t, s, runWatch(r.ID, []string{"note"}, watch.Notify, 0))
	now := time.Now()
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := len(deliveries(t, s, a.ID)) + len(deliveries(t, s, b.ID)); got != 1 {
		t.Fatalf("owner received %d deliveries, want 1", got)
	}
	// The undelivered copy of the next event must still honor interval_ms: a
	// delivery through one watch advances every contributing watch's clock.
	evidence(t, s, r.ID, "note")
	if err := s.EvaluateWatches(now.Add(600 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := len(deliveries(t, s, a.ID)) + len(deliveries(t, s, b.ID)); got != 1 {
		t.Fatalf("a second watch bypassed the grouping interval: %d deliveries", got)
	}
	if err := s.EvaluateWatches(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := len(deliveries(t, s, a.ID)) + len(deliveries(t, s, b.ID)); got != 2 {
		t.Fatalf("grouped event was never delivered after the interval: %d deliveries", got)
	}
}
