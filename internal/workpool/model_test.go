package workpool

import (
	"testing"
	"time"
)

func TestDependenciesGuardsJoinsAndGraph(t *testing.T) {
	states := map[string]State{"a": {ID: "a", Status: "done", RunID: "ra", Hash: "ha"}, "b": {ID: "b", Status: "failed", RunID: "rb", Hash: "hb"}, "example": {ID: "example", Status: "done", Sample: true}}
	r := Request{Deps: []Dependency{{From: "a", Guard: "done"}, {From: "b", Guard: "done"}}}
	r.Normalize()
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	all := Evaluate(r, states)
	if all.Ready || len(all.Waiting) != 1 || all.Waiting[0] != "b" {
		t.Fatal("hard success guard ignored")
	}
	r.Join.Rule = "any"
	any := Evaluate(r, states)
	if !any.Ready || len(any.Matched) != 1 || any.Matched[0].Hash != "ha" {
		t.Fatal("any join not bound to result")
	}
	r.Join = Join{Rule: "k_of_n", K: 2}
	r.Deps[1].Guard = "failed"
	if !Evaluate(r, states).Ready {
		t.Fatal("failure branch/quorum failed")
	}
	r.Deps[1].Guard = ""
	if !Evaluate(r, states).Ready {
		t.Fatal("unconditional finish did not include failure")
	}
	r.Join = Join{Rule: "all"}
	r.Deps[1].Strength = "soft"
	r.Deps[1].From = "missing"
	if !Evaluate(r, states).Ready {
		t.Fatal("soft hint blocked")
	}
	r.Deps[0].From = "example"
	if Evaluate(r, states).Ready {
		t.Fatal("example satisfied real work")
	}
	a := Request{Deps: []Dependency{{From: "b"}}}
	b := Request{Deps: []Dependency{{From: "a"}}}
	if Graph(map[string]Request{"a": a, "b": b}) == nil {
		t.Fatal("cycle accepted")
	}
	if Graph(map[string]Request{"a": a}) == nil {
		t.Fatal("missing source accepted")
	}
	for _, field := range []string{"SS", "FF", "SF"} {
		bad := Request{Deps: []Dependency{{From: "a", Relation: field}}}
		if bad.Validate() == nil {
			t.Fatal("unimplemented relation silently accepted")
		}
	}
}
func TestDisciplinesAgingAndStableTies(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	later := now.Add(time.Hour)
	earlier := now.Add(time.Minute)
	candidates := []Candidate{{ID: "old", ReadyAt: now.Add(-10 * time.Minute), Request: Request{Priority: 0, Deadline: &later}}, {ID: "new", ReadyAt: now, Request: Request{Priority: 3, Deadline: &earlier}}}
	for discipline, want := range map[string]string{"fifo": "old", "lifo": "new", "priority": "old", "deadline_edf": "new"} {
		got := Order(Pool{Discipline: discipline}, candidates, now)
		if got[0].ID != want {
			t.Errorf("%s got %s", discipline, got[0].ID)
		}
	}
	if candidates[0].ID != "old" {
		t.Fatal("selector mutated caller")
	}
	candidates[0].ReadyAt = now
	candidates[1].Request.Priority = 0
	candidates[0].Request.Deadline = nil
	candidates[1].Request.Deadline = nil
	if Order(Pool{Discipline: "fifo"}, candidates, now)[0].ID != "new" {
		t.Fatal("ties not stable by ID")
	}
}
func TestReservationsAndConcurrency(t *testing.T) {
	pools := Defaults()
	p := pools["interactive"]
	p.Reserved = 1
	pools[p.ID] = p
	if !Available("batch", pools, map[string]int{}) {
		t.Fatal("idle batch slot unavailable")
	}
	if Available("batch", pools, map[string]int{"batch": 1}) {
		t.Fatal("reserved interactive slot consumed")
	}
	if !Available("interactive", pools, map[string]int{"batch": 1}) {
		t.Fatal("reserved capacity blocked")
	}
	if Available("interactive", pools, map[string]int{"batch": 1, "interactive": 1}) {
		t.Fatal("global limit exceeded")
	}
	p = pools["batch"]
	p.Concurrency = 1
	pools[p.ID] = p
	if Available("batch", pools, map[string]int{"batch": 1}) {
		t.Fatal("pool concurrency ignored")
	}
}

func TestRaceAndQuorumBindEarliestSatisfiedEvents(t *testing.T) {
	states := map[string]State{"a": {Status: "done", Seq: 20}, "b": {Status: "done", Seq: 10}, "c": {Status: "done", Seq: 15}}
	r := Request{Join: Join{Rule: "any"}, Deps: []Dependency{{From: "a"}, {From: "b"}, {From: "c"}}}
	any := Evaluate(r, states)
	if len(any.Matched) != 1 || any.Matched[0].TaskID != "b" {
		t.Fatal("race did not select earliest terminal evidence")
	}
	r.Join = Join{Rule: "k_of_n", K: 2}
	quorum := Evaluate(r, states)
	if len(quorum.Matched) != 2 || quorum.Matched[0].TaskID != "b" || quorum.Matched[1].TaskID != "c" {
		t.Fatal("quorum witness was not deterministic")
	}
}

func TestExplicitDataSelectionValidation(t *testing.T) {
	var deps []Dependency
	if err := AddInput(&deps, "task_a:one.json"); err != nil {
		t.Fatal(err)
	}
	if err := AddInput(&deps, "task_a:two.bin"); err != nil {
		t.Fatal(err)
	}
	request := Request{Deps: deps}
	if err := request.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || len(deps[0].Artifacts) != 2 {
		t.Fatal("artifact selections not grouped")
	}
	for _, name := range []string{"../secret", "/absolute", "..", "a/b", "bad\nname"} {
		bad := Request{Deps: []Dependency{{From: "a", Payload: "data", Artifacts: []string{name}}}}
		if bad.Validate() == nil {
			t.Fatalf("unsafe name %q admitted", name)
		}
	}
	deps[0].Artifacts = append(deps[0].Artifacts, "one.json")
	if (Request{Deps: deps}).Validate() == nil {
		t.Fatal("duplicate artifact accepted")
	}
}
