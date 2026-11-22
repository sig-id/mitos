package studio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mitos/internal/decision"
)

func TestRestartRecoversGraphAttemptsThroughGraph(t *testing.T) {
	e, g, planner, worker := ownerGraph(t)
	dir := e.Store.Dir()
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restarted, err := NewEngine(s, e.Apps, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Close() // Drive each subsequent reconciliation explicitly.
	for _, id := range []string{planner, worker} {
		r, err := s.Get(id)
		if err != nil || r.Status != "parked" || r.Decision == nil || r.Decision.ReasonSignal != "graph_attempt_interrupted" {
			t.Fatalf("restart did not offer graph recovery: %+v, %v", r.Decision, err)
		}
		if _, err := restarted.Retry(id); !errors.Is(err, ErrConflict) {
			t.Fatalf("plain retry: %v", err)
		}
		if _, err := restarted.Launch(r.Brief, id); !errors.Is(err, ErrConflict) {
			t.Fatalf("plain launch: %v", err)
		}
		for range 2 {
			if _, err := restarted.Resolve(id, r.Decision.ID, "retry"); err != nil {
				t.Fatal(err)
			}
		}
		old, _ := s.Get(id)
		if old.Status != "cancelled" || old.Decision.AppliedAt == nil {
			t.Fatal("graph recovery was not applied", old.Decision)
		}
	}
	if err := restarted.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	newGraph, _ := s.Graph(g.ID)
	plan := newGraph.Expansions["initial"]
	if plan.Error != "" || plan.OrchestratorTask == g.Expansions["initial"].OrchestratorTask || newGraph.OrchestratorGeneration != g.OrchestratorGeneration+1 || newGraph.Attempts != g.Attempts+2 {
		t.Fatal("graph did not replace the interrupted planner exactly once", plan)
	}
	w, _ := s.Get(worker)
	for _, task := range []string{plan.OrchestratorTask, w.TaskID} {
		r, err := s.Get(newGraph.Members[task].CurrentRunID)
		if err != nil || r.GraphAttempt == nil || r.GraphAttempt.GraphID != g.ID || r.GraphAttempt.Generation != newGraph.OrchestratorGeneration || r.TaskID != task || r.ID == worker || r.ID == planner {
			t.Fatal("invalid replacement", r.ID, err)
		}
	}
}

func TestRecoverPreviouslyRecordedGraphRetry(t *testing.T) {
	e, g, planner, worker := ownerGraph(t)
	s := e.Store
	if err := s.Park(planner, recoveryDecision()); err != nil {
		t.Fatal(err)
	}
	r, _ := s.Get(planner)
	if err := e.graphOrchestratorError(g.ID, "initial", r.TaskID, "orchestrator_execution_needs_review"); err != nil {
		t.Fatal(err)
	}
	// Model the two records left by an older server's failed retry.
	accepted := decision.Resolution{DecisionID: r.Decision.ID, Choice: "retry", Resolver: "local-operator", Via: "api", ResolvedAt: time.Now().UTC()}
	s.mu.Lock()
	err := s.commit(planner, "effect", "approval_resolved", change{Resolution: &accepted, Choice: "retry", Status: "parked"})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.failDecisionApplication(planner, "acceptance.cmd requires an absolute executable path", false); err != nil {
		t.Fatal(err)
	}
	e.reconcileDecisions()
	r, _ = s.Get(planner)
	after, _ := s.Graph(g.ID)
	if r.Decision.AppliedAt == nil || r.Decision.ApplicationError != "" || after.Expansions["initial"].Error != "" || after.OrchestratorGeneration != g.OrchestratorGeneration+1 {
		t.Fatal("previously accepted retry remains stuck", r.Decision)
	}
	w, _ := s.Get(worker)
	if err := s.interruptExecution(w); err != nil {
		t.Fatal(err)
	}
	w, _ = s.Get(worker)
	if _, err := e.Resolve(worker, w.Decision.ID, "cancel"); err != nil {
		t.Fatal(err)
	}
	after, _ = s.Graph(g.ID)
	if !after.Members[w.TaskID].CancelRequested {
		t.Fatal("cancel left the member eligible for another attempt")
	}
}

func TestInterruptedNativePlannerRetainsClaimUntilInspection(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "pi"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, req := graphEngine(t, false)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "interrupted-pi", ProjectID: req.ProjectID, Goal: "Review recovery", Execution: defaultExecution("pi", "fixture/planner")})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	id := g.Members[g.Expansions["initial"].OrchestratorTask].CurrentRunID
	ok, err := e.Store.AcquireClaims(id)
	if !ok || err != nil {
		t.Fatal("claim", ok, err)
	}
	// Start with the durable state a crashed Pi attempt left behind.
	restarted, err := NewEngine(e.Store, e.Apps, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Close()
	r, _ := e.Store.Get(id)
	if r.Decision == nil || r.Claim == nil || r.Claim.ReleasedAt != nil {
		t.Fatal("missing interruption or claim", r.Status)
	}
	if _, err := restarted.Resolve(id, r.Decision.ID, "retry"); !errors.Is(err, ErrConflict) {
		t.Fatal("retry bypassed claim inspection", err)
	}
	r, _ = e.Store.Get(id)
	if r.Decision.Resolution != nil {
		t.Fatal("refused retry persisted an unappliable resolution")
	}
	if err := restarted.ReleaseInspectedClaims(id); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Resolve(id, r.Decision.ID, "retry"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	replacement, err := e.Store.Get(g.Members[g.Expansions["initial"].OrchestratorTask].CurrentRunID)
	if err != nil || replacement.ID == id || replacement.GraphAttempt == nil || replacement.GraphAttempt.Generation != r.GraphAttempt.Generation+1 || replacement.GraphAttempt.Executor.Application.Agent.Runtime != "pi" {
		t.Fatal("lost Pi orchestrator binding", replacement.ID, err)
	}
}
