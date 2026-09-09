package autonomy

import (
	"testing"
	"time"

	"mitos/internal/acceptance"
)

func integer(value int) *int { return &value }
func TestIntensityBoundsRunAndStepAcrossSignals(t *testing.T) {
	now := time.Unix(1000, 0)
	policy := Supervision{MaxRestarts: integer(3), MaxSeconds: 10, Strategy: "rewind_to_green"}
	restarts := []Rewind{{StepID: "a", ReasonSignal: "command_failed", RequestedAt: now.Add(-5 * time.Second)}, {StepID: "b", ReasonSignal: "predicate_failed", RequestedAt: now.Add(-4 * time.Second)}, {StepID: "a", ReasonSignal: "predicate_stall", RequestedAt: now.Add(-3 * time.Second)}, {StepID: "a", RequestedAt: now.Add(-11 * time.Second)}}
	run, step, exhausted := Intensity(policy, restarts, "a", now)
	if run != 3 || step != 2 || !exhausted {
		t.Fatalf("rotated signals bypassed intensity: %d %d %v", run, step, exhausted)
	}
	if _, _, exhausted = Intensity(policy, restarts, "b", now.Add(20*time.Second)); exhausted {
		t.Fatal("old restart window did not expire")
	}
	if _, _, exhausted = Intensity(Supervision{MaxRestarts: integer(0)}, nil, "a", now); !exhausted {
		t.Fatal("zero limit allowed a restart")
	}
	if _, _, exhausted = Intensity(policy, restarts, "a", now.Add(-time.Minute)); !exhausted {
		t.Fatal("a backward clock jump reset the restart budget")
	}
}
func TestPredicateStallCountsAbandonedSegmentsButNotNarration(t *testing.T) {
	checkpoints := []Checkpoint{{StepID: "main", SegmentNumber: 0, Predicate: acceptance.Verdict{Delta: []float64{0}}}}
	for i := 1; i <= 3; i++ {
		checkpoints = append(checkpoints, Checkpoint{StepID: "main", SegmentNumber: i, Invalidated: i < 3, Predicate: acceptance.Verdict{Verdict: "fail", Delta: []float64{0, 0}}})
	}
	if !PredicateStall(checkpoints, "main") {
		t.Fatal("discarding failed segments erased stall evidence")
	}
	checkpoints[2].Predicate.Delta = []float64{0, 1}
	if PredicateStall(checkpoints, "main") {
		t.Fatal("measured progress classified as stall")
	}
	checkpoints[2].Predicate.Delta = nil
	if PredicateStall(checkpoints, "main") {
		t.Fatal("missing measurement treated as zero")
	}
}
func TestSupervisionDefaultsAndValidation(t *testing.T) {
	policy := Supervision{Strategy: "retry_step"}
	if policy.Validate() != nil || policy.Limit() != 3 || policy.Window() != time.Hour {
		t.Fatal("wrong defaults")
	}
	for _, policy := range []Supervision{{Strategy: "unknown"}, {Strategy: "escalate", MaxRestarts: integer(-1)}, {Strategy: "rewind_tail", MaxSeconds: -1}} {
		if policy.Validate() == nil {
			t.Fatal("invalid policy accepted")
		}
	}
	if (Rewind{Scope: "step", ReasonSignal: "failure"}).Validate() == nil {
		t.Fatal("memo-free rewind accepted")
	}
}

func TestPredicateStallWithoutOptionalNumericMeasure(t *testing.T) {
	var checkpoints []Checkpoint
	for number := 1; number <= 3; number++ {
		checkpoints = append(checkpoints, Checkpoint{StepID: "main", SegmentNumber: number, Predicate: acceptance.Verdict{Verdict: "fail"}})
	}
	if !PredicateStall(checkpoints, "main") {
		t.Fatal("binary predicate plateau was ignored")
	}
	checkpoints[1].Predicate.Verdict = "pass"
	if PredicateStall(checkpoints, "main") {
		t.Fatal("changed predicate verdict was ignored")
	}
	checkpoints[1].Predicate.Verdict = "unknown"
	if PredicateStall(checkpoints, "main") {
		t.Fatal("unknown verdict was treated as failure evidence")
	}
}
