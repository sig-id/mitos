package workpool

import (
	"testing"
	"time"
)

func TestSourcePriorityAdjustmentUsesPoolPolicyAndAging(t *testing.T) {
	now := time.Now()
	earlier := now.Add(time.Hour)
	later := earlier.Add(time.Hour)
	candidates := []Candidate{{ID: "low", PriorityAdjustment: -100, ReadyAt: now.Add(-time.Second), Request: Request{Deadline: &earlier}}, {ID: "critical", PriorityAdjustment: 200, ReadyAt: now, Request: Request{Deadline: &later}}}
	for discipline, want := range map[string]string{"priority": "critical", "fifo": "low", "lifo": "critical", "deadline_edf": "low"} {
		got := Order(Pool{Discipline: discipline}, candidates, now)
		if got[0].ID != want {
			t.Fatal(discipline, got)
		}
	}
	candidates[0].ReadyAt = now.Add(-301 * time.Minute)
	if Order(Pool{Discipline: "priority"}, candidates, now)[0].ID != "low" {
		t.Fatal("source priority defeated aging")
	}
	candidates[0].ReadyAt = now
	candidates[0].Request.Priority = 301
	if Order(Pool{Discipline: "priority"}, candidates, now)[0].ID != "low" {
		t.Fatal("source priority replaced explicit Task priority")
	}
}
