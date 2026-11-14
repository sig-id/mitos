package workpool

import "testing"

func TestTaskAndInboxSourcesShareJoins(t *testing.T) {
	r := Request{Deps: []Dependency{
		{From: "a", Guard: "done"},
		{From: "b", Guard: "failed"},
		{From: "release", Guard: "inbox_received", Payload: "data", Inbox: &Inbox{Project: "p", Mailbox: "g", Kind: "event", Type: "workflow.signal"}},
	}}
	states := map[string]State{
		"a":       {Status: "done", Seq: 2},
		"b":       {Status: "running", Seq: 1},
		"release": {Status: "inbox_received", Seq: 3, Inbox: &InboxWitness{Key: "peer/message"}},
	}
	for _, tc := range []struct {
		join      Join
		ready     bool
		witnesses int
	}{
		{Join{Rule: "all"}, false, 2}, {Join{Rule: "any"}, true, 1},
		{Join{Rule: "k_of_n", K: 2}, true, 2}, {Join{Rule: "k_of_n", K: 3}, false, 2},
	} {
		r.Join = tc.join
		if err := Graph(map[string]Request{"a": {}, "b": {}, "consumer": r}); err != nil {
			t.Fatal(err)
		}
		got := Evaluate(r, states)
		if got.Ready != tc.ready || len(got.Matched) != tc.witnesses {
			t.Fatalf("%+v: %+v", tc.join, got)
		}
		if tc.witnesses == 2 && got.Matched[1].Inbox == nil {
			t.Fatal("inbox evidence missing")
		}
	}
	r.Join = Join{Rule: "all"}
	states["b"] = State{Status: "failed", Seq: 4}
	if !Evaluate(r, states).Ready {
		t.Fatal("failed guard did not join with successful task and inbox")
	}
	delete(states, "release")
	r.Deps[2].Strength = "soft"
	if !Evaluate(r, states).Ready {
		t.Fatal("optional inbox blocked")
	}
}
