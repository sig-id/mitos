package studio

import (
	"strings"
	"testing"
)

func TestFinalAnswerRequiresAcceptedSources(t *testing.T) {
	g := WorkGraph{Members: map[string]*GraphMember{
		"worker":     {Spec: GraphSpec{Kind: "application"}, CurrentRunID: "run-new", Accepted: &GraphAcceptance{RunID: "run-reviewed"}},
		"unreviewed": {Spec: GraphSpec{Kind: "application"}},
		"replaced":   {Spec: GraphSpec{Kind: "application"}, Replacement: "worker", Accepted: &GraphAcceptance{RunID: "old"}},
		"planner":    {Spec: GraphSpec{Kind: "application"}, ControllerFor: "initial", Accepted: &GraphAcceptance{RunID: "control"}},
	}}
	answer := FinalAnswer{Title: "Gap report", Markdown: "- [ ] A demonstrated gap.", SourceTasks: []string{"worker"}}
	result, err := bindGraphResult(&g, answer, "planner:test")
	if err != nil || result.Sources[0].Run != "run-reviewed" {
		t.Fatalf("must freeze reviewed run: %+v %v", result, err)
	}
	for _, id := range []string{"missing", "unreviewed", "replaced", "planner"} {
		bad := answer
		bad.SourceTasks = []string{id}
		if _, err := bindGraphResult(&g, bad, "planner:test"); err == nil {
			t.Fatalf("accepted invalid source %s", id)
		}
	}
	for _, bad := range []FinalAnswer{{}, {Title: "Review accepted", SourceTasks: []string{"worker"}}, {Title: "Gap report", Markdown: "Findings", SourceTasks: []string{"worker", "worker"}}, {Title: "Gap report", Markdown: strings.Repeat("x", (128<<10)+1), SourceTasks: []string{"worker"}}} {
		if _, err := bindGraphResult(&g, bad, "planner:test"); err == nil {
			t.Fatal("accepted missing or unbounded final answer")
		}
	}
}
