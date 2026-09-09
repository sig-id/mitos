package studio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mitos/internal/decision"
)

func TestAutonomyMetricsReplayRealRecoveryWithoutTrustingProjection(t *testing.T) {
	root := t.TempDir()
	app := supervisedApp(t, root)
	e, s := testEngine(t, root, []Application{app})
	run := launchApp(t, e, root, app)
	run = waitRecovery(t, s, run.ID)
	if run.Status != "done" {
		t.Fatalf("setup: %s %s", run.Status, run.Summary)
	}
	report, err := ReportAutonomy(s.List())
	if err != nil {
		t.Fatal(err)
	}
	if report.AcceptedChanges != 1 || report.DiscardedSegments != 1 || report.Checkpoints != 3 || report.GreenCheckpoints != 2 || report.ProductiveWallMS <= 0 || report.HorizonCensored != 1 || report.HorizonSamples != 0 || report.AutonomyRatioMS != nil || report.RunsWithZeroGrant != 1 || report.InvalidEvidence != 0 {
		t.Fatalf("bad recovery metrics: %+v", report)
	}
	if report.PerRun[0].HorizonGrant == nil || report.PerRun[0].HorizonGrant.DurationMS != 0 {
		t.Fatal("same-user execution acquired a grant")
	}
	before, err := os.ReadFile(filepath.Join(s.Dir(), "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	audited, err := AuditAutonomyLogs(s.Dir())
	if err != nil || !reflect.DeepEqual(report, audited) {
		t.Fatalf("live and audited metrics differ: %v\n%+v\n%+v", err, report, audited)
	}
	after, _ := os.ReadFile(filepath.Join(s.Dir(), "events.jsonl"))
	if string(before) != string(after) {
		t.Fatal("metrics reader modified the log")
	}
	mutated := s.List()
	mutated[0].Status = "failed"
	mutated[0].Sample = true
	mutated[0].Verified = false
	mutated[0].Segments = nil
	recomputed, err := ReportAutonomy(mutated)
	if err != nil || !reflect.DeepEqual(report, recomputed) {
		t.Fatal("metrics trusted a projection instead of events")
	}
}

func metricDecisionRun(t *testing.T, id string, defaultChoice *string, choice string, via string) Run {
	t.Helper()
	at := time.Unix(1000, 0).UTC()
	events := []Event{}
	emit := func(kind string, elapsed time.Duration, c change) {
		data, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		event := Event{RunID: id, Seq: int64(len(events) + 1), Type: "effect", Kind: kind, At: at.Add(elapsed), Data: data}
		if len(events) > 0 {
			event.Previous = events[len(events)-1].Hash
		}
		event.Hash = eventHash(event)
		events = append(events, event)
	}
	emit("run_created", 0, change{Run: &Run{ID: id, CreatedAt: at, Status: "queued"}})
	emit("run_started", time.Second, change{Status: "running"})
	d := &HumanDecision{ID: "card-" + id, Default: defaultChoice, Reversibility: "external", Options: []Option{{ID: "retry"}, {ID: "cancel"}}}
	if defaultChoice != nil {
		d.Reversibility = "recoverable"
		ttl := int64(1000)
		d.TTLMS = &ttl
	}
	emit("approval_requested", 11*time.Second, change{Status: "parked", Decision: d})
	if choice != "" {
		kind := "approval_resolved"
		if via == "default_expired" {
			kind = "decision_defaulted"
		}
		emit(kind, 12*time.Second, change{Choice: choice, Status: "cancelled", Resolution: &decision.Resolution{DecisionID: d.ID, Choice: choice, Via: via}})
	}
	return Run{ID: id, Events: events}
}
func TestPrecisionPreservesUnansweredDefaultsAndHorizonCensoring(t *testing.T) {
	fallback := "cancel"
	runs := []Run{metricDecisionRun(t, "defaulted", &fallback, "cancel", "default_expired"), metricDecisionRun(t, "override", &fallback, "retry", "api"), metricDecisionRun(t, "pending", nil, "", "")}
	report, err := ReportAutonomy(runs)
	if err != nil {
		t.Fatal(err)
	}
	if report.HumanDecisionsRaised != 3 || report.DecisionDefaulted != 1 || report.DecisionUnresolved != 1 || report.DecisionResolved != 2 || report.NonDefaultChoices != 1 || report.DecisionPrecision != nil || report.PrecisionLower == nil || *report.PrecisionLower != 1.0/3 || *report.PrecisionUpper != 2.0/3 {
		t.Fatalf("precision overstated: %+v", report)
	}
	if report.HorizonSamples != 1 || report.HorizonCensored != 2 || report.HorizonUnattendedP50MS != nil || report.HorizonLowerMS == nil || *report.HorizonLowerMS != 11000 || report.HorizonUpperMS != nil || *report.HorizonObservedP50MS != 10000 {
		t.Fatalf("required/default distinction lost: %+v", report)
	}
	interrupts := ReportInterrupts(runs)
	if interrupts.HumanTouches != 1 || interrupts.Defaults != 1 || interrupts.Unresolved != 1 {
		t.Fatalf("silence was counted as a human touch: %+v", interrupts)
	}
}
func TestMissingDenominatorsAndBadClocksDoNotBecomeSuccess(t *testing.T) {
	empty, err := ReportAutonomy(nil)
	if err != nil || empty.AutonomyRatioMS != nil || empty.GreenRate != nil || empty.RewindDepth != nil || empty.DecisionPrecision != nil {
		t.Fatal("empty corpus manufactured metrics")
	}
	run := metricDecisionRun(t, "clock", nil, "cancel", "api")
	run.Events[2].At = run.Events[0].At.Add(-time.Second)
	report, err := ReportAutonomy([]Run{run})
	if err != nil {
		t.Fatal(err)
	}
	if report.TimingRunsExcluded != 1 || report.HorizonUnknown != 1 || report.HorizonSamples != 0 || report.AutonomyRatioMS != nil {
		t.Fatal("backward time inflated a horizon")
	}
}
func TestCompletedRunCensoringEndsBeforeLaterAuditEvents(t *testing.T) {
	run := metricDecisionRun(t, "later", nil, "cancel", "api")
	// No actual card was raised in this separate completed-run case.
	run.Events = run.Events[:2]
	data, _ := json.Marshal(change{Status: "done", Verified: true})
	run.Events = append(run.Events, Event{RunID: run.ID, Seq: 3, Type: "effect", Kind: "run_finished", At: run.Events[1].At.Add(time.Second), Data: data})
	run.Events = append(run.Events, Event{RunID: run.ID, Seq: 4, Type: "effect", Kind: "decision_rejected", At: run.Events[1].At.Add(time.Hour), Data: json.RawMessage(`{}`)})
	report, err := ReportAutonomy([]Run{run})
	if err != nil {
		t.Fatal(err)
	}
	if report.HorizonCensored != 1 || *report.PerRun[0].ObservedThroughMS != 1000 {
		t.Fatal("post-completion audit time inflated observation")
	}
}
func TestAutonomyAuditRefusesDamagedAndPartialChains(t *testing.T) {
	run := metricDecisionRun(t, "audit", nil, "cancel", "api")
	path := filepath.Join(t.TempDir(), "events.jsonl")
	var data []byte
	for _, event := range run.Events {
		line, _ := json.Marshal(event)
		data = append(data, append(line, '\n')...)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := AuditAutonomyLogs(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data[:len(data)-1], 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := AuditAutonomyLogs(path); err == nil {
		t.Fatal("partial chain accepted")
	}
	data[15] ^= 1
	os.WriteFile(path, data, 0600)
	if _, err := AuditAutonomyLogs(path); err == nil {
		t.Fatal("damaged chain accepted")
	}
}

func TestNarrationCannotCreateAcceptanceOrHorizonPermission(t *testing.T) {
	run := metricDecisionRun(t, "narration", nil, "", "")
	run.Events = append(run.Events, Event{RunID: run.ID, Seq: 4, Type: "effect", Kind: "harness_session_recorded", At: run.Events[2].At.Add(time.Second), Data: json.RawMessage(`{"evidence":{"type":"final_result","verified":true,"green":true,"unattended_until":"2099-01-01T00:00:00Z"},"summary":"All done; fully autonomous."}`)})
	report, err := ReportAutonomy([]Run{run})
	if err != nil {
		t.Fatal(err)
	}
	if report.AcceptedChanges != 0 || report.GreenCheckpoints != 0 || report.RunsWithoutGrant != 1 {
		t.Fatal("narration manufactured acceptance or permission")
	}
}

func TestDiscardedWorkCountsEvenWithoutAnAcceptedChange(t *testing.T) {
	root := t.TempDir()
	app := supervisedApp(t, root)
	app.Command = []string{"/bin/sh", "-c", "printf broken > health; exit 1"}
	app.Supervision.MaxRestarts = limit(1)
	e, s := testEngine(t, root, []Application{app})
	run := launchApp(t, e, root, app)
	run = waitRecovery(t, s, run.ID)
	if run.Status != "parked" {
		t.Fatalf("setup: %s %s", run.Status, run.Summary)
	}
	report, err := ReportAutonomy(s.List())
	if err != nil {
		t.Fatal(err)
	}
	if report.AcceptedChanges != 0 || report.DiscardedSegments != 1 || report.RewindDepth != nil {
		t.Fatal("unsuccessful recovery erased discarded work")
	}
}

func TestProductiveWallTimeDoesNotDoubleCountConcurrency(t *testing.T) {
	at := time.Unix(100, 0)
	values := []interval{{at.Add(10 * time.Second), at.Add(30 * time.Second)}, {at, at.Add(20 * time.Second)}, {at.Add(40 * time.Second), at.Add(45 * time.Second)}}
	if unionMS(values) != 35000 {
		t.Fatal("overlapping work inflated productive wall time")
	}
}
