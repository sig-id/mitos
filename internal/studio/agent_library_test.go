package studio

import (
	"errors"
	"testing"
)

func TestAgentLibraryRevisionsAndReplay(t *testing.T) {
	e, _ := graphEngine(t, false)
	initial, err := e.AgentLibrary()
	if err != nil || len(initial) != 8 {
		t.Fatalf("library: %v %d", err, len(initial))
	}
	request := AgentEdit{Key: "new-specialist", Name: "Accessibility reviewer", Description: "Review keyboard navigation", Instructions: "Inspect focus order and report evidence."}
	first, err := e.SaveAgent(request)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := e.SaveAgent(request)
	if err != nil || replay.ID != first.ID {
		t.Fatal("save replay failed", err)
	}
	request.ID = first.ID
	request.Revision = first.Revision
	request.Key = "edit-specialist"
	request.Instructions = "Check focus order and accessible names."
	second, err := e.SaveAgent(request)
	if err != nil || second.Revision != 2 {
		t.Fatal("revision failed", err)
	}
	request.Key = "stale-edit"
	if _, err = e.SaveAgent(request); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale edit: %v", err)
	}
	library, err := e.AgentLibrary()
	if err != nil || len(library) != 9 {
		t.Fatal("library persisted", err)
	}
	saved, err := e.readLibrary()
	if err != nil || len(saved.Revisions[first.ID]) != 2 {
		t.Fatal("history not preserved", err)
	}
	if first.Instructions == second.Instructions || first.Digest == second.Digest {
		t.Fatal("definition identity did not change")
	}
	for _, d := range initial {
		if isOrchestratorKind(d.Kind) {
			request.ID = d.ID
			request.Revision = d.Revision
			request.Key = "edit-orchestrator"
			if _, err = e.SaveAgent(request); err == nil {
				t.Fatal("orchestrator authority editable")
			}
		}
	}
}
