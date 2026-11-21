package studio

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"mitos/agents"
	"mitos/internal/watch"
)

// Outcomes, marks and the watch report (spec 09 §5, gate W4).

func TestDeliveryOutcomesAndMarks(t *testing.T) {
	s := testStore(t)
	r := createRun(t, s)
	w := mustWatch(t, s, runWatch(r.ID, []string{"note"}, watch.Notify, 0))
	evidence(t, s, r.ID, "note")
	evaluate(t, s, time.Now().Add(time.Second))
	ds := deliveries(t, s, w.ID)
	if len(ds) != 1 {
		t.Fatalf("deliveries %+v", ds)
	}
	id := ds[0].ID

	// Exactly one of outcome and mark.
	if _, err := s.RecordDeliveryMark(id, "", ""); err == nil {
		t.Fatal("empty outcome and mark accepted")
	}
	if _, err := s.RecordDeliveryMark(id, "acted", "needless_wake"); err == nil {
		t.Fatal("outcome and mark together accepted")
	}
	if _, err := s.RecordDeliveryMark(id, "shrugged", ""); err == nil {
		t.Fatal("unknown outcome accepted")
	}
	if _, err := s.RecordDeliveryMark(id, "", "too_loud"); err == nil {
		t.Fatal("unknown mark accepted")
	}
	if _, err := s.RecordDeliveryMark("delivery_none", "acted", ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing delivery: %v", err)
	}

	// An owner outcome settles the delivery; the same one repeats, a different
	// one is refused. The outcome and the operator's calibration mark are
	// independent records (spec 09 §5): both can sit on one delivery, written
	// in either order.
	got, err := s.RecordDeliveryMark(id, "acted", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != "acted" || got.Mark != "" {
		t.Fatalf("delivery view %+v", got)
	}
	if _, err = s.RecordDeliveryMark(id, "acted", ""); err != nil {
		t.Fatalf("idempotent outcome: %v", err)
	}
	if _, err = s.RecordDeliveryMark(id, "dismissed", ""); err == nil {
		t.Fatal("second outcome accepted")
	}
	if _, err = s.RecordDeliveryMark(id, "", "needless_wake"); err != nil {
		t.Fatalf("mark beside an outcome refused: %v", err)
	}
	if _, err = s.RecordDeliveryMark(id, "", "should_have_woken"); err == nil {
		t.Fatal("second mark accepted")
	}
	if got, err = s.RecordDeliveryMark(id, "acted", ""); err != nil || got.Outcome != "acted" || got.Mark != "needless_wake" {
		t.Fatalf("idempotent outcome after mark: %v %+v", err, got)
	}

	// A second delivery takes the mark first, then its outcome.
	evidence(t, s, r.ID, "note")
	evaluate(t, s, time.Now().Add(2*time.Second))
	ds = deliveries(t, s, w.ID)
	if len(ds) != 2 {
		t.Fatalf("deliveries %+v", ds)
	}
	if _, err = s.RecordDeliveryMark(ds[0].ID, "", "should_have_woken"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.RecordDeliveryMark(ds[0].ID, "deferred", ""); err != nil {
		t.Fatalf("outcome beside a mark refused: %v", err)
	}
	if view, _ := s.WatchDeliveries(w.ID, 10); view[0].Mark != "should_have_woken" || view[0].Outcome != "deferred" || view[1].Outcome != "acted" || view[1].Mark != "needless_wake" {
		t.Fatalf("joined views %+v", view)
	}
}

func TestOperatorOutcomeRefusedForOwnedDeliveries(t *testing.T) {
	e, g, _, helper := ownerGraph(t)
	s := e.Store
	w := mustWatch(t, s, ownedWatch(responsibility(g), g.ID, []string{"note"}, watch.Notify))
	evidence(t, s, helper, "note")
	evaluate(t, s, time.Now().Add(time.Second))
	ds := deliveries(t, s, w.ID)
	if len(ds) != 1 {
		t.Fatalf("deliveries %+v", ds)
	}
	// Over the API the caller is the operator: only operator-owned deliveries
	// take an owner outcome. The responsibility settles its own through the
	// granted tool, fenced on the generation.
	if _, err := s.RecordDeliveryMark(ds[0].ID, "acted", ""); err == nil {
		t.Fatal("operator outcome accepted for a responsibility delivery")
	}
	if _, err := s.RecordDeliveryMark(ds[0].ID, "", "needless_wake"); err != nil {
		t.Fatalf("calibration mark refused: %v", err)
	}
	if _, err := s.RecordDeliveryOutcome(responsibility(g), g.OrchestratorGeneration, ds[0].ID, "acted"); err != nil {
		t.Fatalf("tool outcome: %v", err)
	}
	if view, _ := s.WatchDeliveries(w.ID, 10); view[0].Outcome != "acted" || view[0].Mark != "needless_wake" {
		t.Fatalf("joined view %+v", view[0])
	}
}

func TestWatchReportSurvivesReplay(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := createRun(t, s)
	// A wake-level watch, so the replayed mark also rebuilds the derived
	// wake-precision counts.
	w := mustWatch(t, s, runWatch(r.ID, []string{"note"}, watch.Wake, 0))
	evidence(t, s, r.ID, "note")
	_ = s.EvaluateWatches(time.Now().Add(time.Second))
	ds := deliveries(t, s, w.ID)
	if len(ds) != 1 {
		t.Fatalf("deliveries %+v", ds)
	}
	if _, err = s.RecordDeliveryMark(ds[0].ID, "dismissed", ""); err != nil {
		t.Fatal(err)
	}
	// The independent mark rides the same delivery through a separate record.
	if _, err = s.RecordDeliveryMark(ds[0].ID, "", "needless_wake"); err != nil {
		t.Fatal(err)
	}
	before, err := s.WatchReport(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = s.Close()

	s, err = OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after, err := s.WatchReport(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	if string(a) != string(b) {
		t.Fatalf("replay changed the report:\n%s\n%s", a, b)
	}
	if after.Outcomes["dismissed"] != 1 || after.Marks["needless_wake"] != 1 || after.NeedlessWake != 1 {
		t.Fatalf("replay lost the outcome or mark: %+v", after)
	}
}

// TestDefaultTemplatesCreateWatches turns every shipped template into a real
// responsibility watch on a live graph: the same input the watch_create tool
// takes, clamped to the creator's graph.
func TestDefaultTemplatesCreateWatches(t *testing.T) {
	e, g, _, _ := ownerGraph(t)
	s := e.Store
	templates, err := agents.WatchTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) < 3 {
		t.Fatalf("templates %+v", templates)
	}
	for _, tpl := range templates {
		in := tpl.Create
		spec := watch.Spec{
			Version: watch.Version, Owner: watch.Owner{Kind: watch.OwnerResponsibility, ID: g.ID},
			Description: in.Description, Condition: in.Condition,
			Lease:    watch.Lease{ExpiresAt: time.Now().Add(time.Duration(in.LeaseMS) * time.Millisecond), MaxRenewals: in.MaxRenewals},
			Grouping: in.Grouping, MaxSilenceMS: in.MaxSilenceMS, DefaultLevel: in.DefaultLevel, Triage: in.Triage,
		}
		w, err := s.CreateWatch(spec)
		if err != nil {
			t.Fatalf("template %s: %v", tpl.Template, err)
		}
		if w.Spec.Condition.Scope != (watch.Scope{Project: g.ProjectID, Graph: g.ID}) {
			t.Fatalf("template %s not clamped: %+v", tpl.Template, w.Spec.Condition.Scope)
		}
	}
}

// TestWatchReportLiveRun is the W4 evidence run: one responsibility watch, a
// real decision provider over HTTP, five groups delivered through the engine
// loop, two operator calibration marks and one owner outcome. The report it
// prints is the wake precision and fallback rate evidence the gate asks for;
// the assertions pin its arithmetic.
func TestWatchReportLiveRun(t *testing.T) {
	e, g, helper, provider := triageGraph(t, 8)
	s := e.Store
	// No kind filter: each step feeds one distinct kind, so every group is
	// new and carries exactly one delivery.
	w := mustWatch(t, s, triagedWatch(g, nil))
	now := time.Now()

	step := func(kind string) WatchDelivery {
		t.Helper()
		evidence(t, s, helper, kind)
		now = now.Add(2 * time.Second)
		triage(t, e, now)
		return onlyDelivery(t, s, w.ID, kind)
	}

	// Two accepted raises, one refusal, one answer below both thresholds.
	provider.answer(watch.Wake, .9, .9)
	first := step("raise_one")
	provider.answer(watch.Wake, .9, .9)
	second := step("raise_two")
	provider.refusing()
	third := step("refusal")
	provider.answer(watch.Digest, .5, .5)
	fourth := step("below")
	// A must-deliver rule event closes the run: no triage call.
	evidence(t, s, helper, "blocked_on_human")
	triage(t, e, now.Add(2*time.Second))
	rule := onlyDelivery(t, s, w.ID, "blocked_on_human")

	// The operator calibrates: one needless wake, one justified wake. The
	// owner settles one delivery.
	if _, err := s.RecordDeliveryMark(first.ID, "", "needless_wake"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordDeliveryMark(second.ID, "", "should_have_woken"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordDeliveryOutcome(responsibility(g), g.OrchestratorGeneration, fourth.ID, "deferred"); err != nil {
		t.Fatal(err)
	}

	report, err := s.WatchReport(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := json.MarshalIndent(report, "", "  ")
	t.Logf("live run watch report:\n%s", out)

	if report.Deliveries != 5 || report.Levels["wake"] != 3 || report.Levels["notify"] != 2 {
		t.Fatalf("levels %+v", report)
	}
	if report.Sources[watch.SourceTriage] != 2 || report.Sources[watch.SourceFallback] != 2 || report.Sources[watch.SourceRule] != 1 {
		t.Fatalf("sources %+v", report)
	}
	if report.Fallbacks[watch.FallbackRefused] != 1 || report.Fallbacks[watch.FallbackBelowThreshold] != 1 {
		t.Fatalf("fallbacks %+v", report)
	}
	if report.TriagedGroups != 4 || report.FallbackGroups != 2 {
		t.Fatalf("triage groups %+v", report)
	}
	if report.FallbackRate == nil || *report.FallbackRate != .5 {
		t.Fatalf("fallback rate %+v", report.FallbackRate)
	}
	if report.WakeDeliveries != 3 || report.MarkedWake != 2 || report.NeedlessWake != 1 {
		t.Fatalf("wake counts %+v", report)
	}
	if report.WakePrecision == nil || *report.WakePrecision != .5 {
		t.Fatalf("wake precision %+v", report.WakePrecision)
	}
	if report.Outcomes["deferred"] != 1 || report.Marks["needless_wake"] != 1 || report.Marks["should_have_woken"] != 1 {
		t.Fatalf("outcomes and marks %+v", report)
	}
	if report.Triage.Requests != 4 || report.Triage.Settled != 4 || report.Triage.Pending != 0 {
		t.Fatalf("triage usage %+v", report.Triage)
	}
	if provider.calls.Load() != 4 {
		t.Fatalf("provider calls %d", provider.calls.Load())
	}
	if rule.Source != watch.SourceRule || rule.TriageID != "" {
		t.Fatalf("rule delivery triaged: %+v", rule)
	}
	if third.Source != watch.SourceFallback || third.Fallback != watch.FallbackRefused {
		t.Fatalf("refused delivery: %+v", third)
	}
}
