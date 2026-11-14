package studio

import (
	"strings"
	"testing"
)

func TestFinalAnswerRequiresAcceptedSources(t *testing.T) {
	g := WorkGraph{Members: map[string]*GraphMember{
		"worker":       {Spec: GraphSpec{Kind: "application"}, CurrentRunID: "run-new", Accepted: &GraphAcceptance{RunID: "run-reviewed"}},
		"unreviewed":   {Spec: GraphSpec{Kind: "application"}},
		"replaced":     {Spec: GraphSpec{Kind: "application"}, Replacement: "worker", Accepted: &GraphAcceptance{RunID: "old"}},
		"orchestrator": {Spec: GraphSpec{Kind: "application"}, OrchestratorFor: "initial", Accepted: &GraphAcceptance{RunID: "control"}},
	}}
	answer := FinalAnswer{Title: "Gap report", Markdown: "- [ ] A demonstrated gap.", SourceTasks: []string{"worker"}}
	result, err := bindGraphResult(&g, answer, "orchestrator:test")
	if err != nil || result.Sources[0].Run != "run-reviewed" {
		t.Fatalf("must freeze reviewed run: %+v %v", result, err)
	}
	for _, id := range []string{"missing", "unreviewed", "replaced", "orchestrator"} {
		bad := answer
		bad.SourceTasks = []string{id}
		if _, err := bindGraphResult(&g, bad, "orchestrator:test"); err == nil {
			t.Fatalf("accepted invalid source %s", id)
		}
	}
	for _, bad := range []FinalAnswer{{}, {Title: "Review accepted", SourceTasks: []string{"worker"}}, {Title: "Gap report", Markdown: "Findings", SourceTasks: []string{"worker", "worker"}}, {Title: "Gap report", Markdown: strings.Repeat("x", (128<<10)+1), SourceTasks: []string{"worker"}}} {
		if _, err := bindGraphResult(&g, bad, "orchestrator:test"); err == nil {
			t.Fatal("accepted missing or unbounded final answer")
		}
	}
}

func TestFinalAnswerPresentationItems(t *testing.T) {
	g := WorkGraph{Members: map[string]*GraphMember{"worker": {Spec: GraphSpec{Kind: "application"}, Accepted: &GraphAcceptance{RunID: "run"}}}}
	base := FinalAnswer{Title: "Implementation", Markdown: "Updated the implementation.", SourceTasks: []string{"worker"}}
	for _, item := range []ResultItem{{Kind: "file", Title: "Report", Task: "worker", Path: "report.md"}, {Kind: "changes", Title: "Changes", Task: "worker", Path: "changes.patch"}, {Kind: "link", Title: "PR 1", URL: "https://example.com/pull/1"}} {
		base.Items = []ResultItem{item}
		if _, err := bindGraphResult(&g, base, "orchestrator:test"); err != nil {
			t.Fatal(err)
		}
	}
	for _, item := range []ResultItem{{Kind: "html", Title: "Unsafe"}, {Kind: "link", Title: "Unsafe", URL: "javascript:alert(1)"}, {Kind: "file", Title: "Outside", Task: "worker", Path: "../secret"}, {Kind: "changes", Title: "Wrong source", Task: "missing", Path: "changes.patch"}, {Kind: "link", Title: "Credentials", URL: "https://user:secret@example.com"}} {
		base.Items = []ResultItem{item}
		if _, err := bindGraphResult(&g, base, "orchestrator:test"); err == nil {
			t.Fatalf("accepted invalid item %+v", item)
		}
	}
}
