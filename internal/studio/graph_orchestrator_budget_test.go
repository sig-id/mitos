package studio

import "testing"

// Issue #137 part 1: the orchestrator attempt budget must scale with the
// materialized evidence the round reads – the artifacts graph_read pages in –
// instead of a flat five minutes that late review rounds systematically
// exceed.

func budgetGraph(automatic bool, calls int, members ...*GraphMember) WorkGraph {
	g := WorkGraph{
		Policy:            GraphPolicy{Mode: "adaptive", Automatic: automatic},
		OrchestratorCalls: calls,
		Members:           map[string]*GraphMember{},
		Expansions:        map[string]*GraphExpansion{},
	}
	for _, m := range members {
		g.Members[m.Spec.ID] = m
	}
	return g
}

func TestOrchestratorAttemptMinutesScalesWithEvidence(t *testing.T) {
	s := testStore(t)
	if got := orchestratorAttemptMinutes(budgetGraph(false, 0), s); got != 5 {
		t.Fatalf("first planning round budget changed: %d", got)
	}
	if got := orchestratorAttemptMinutes(budgetGraph(true, 1), s); got != 10 {
		t.Fatalf("review round base budget not raised: %d", got)
	}
	// The fact journal is bounded by design (32 KiB serialized), so it is
	// not a scaling signal; the artifacts graph_read pages in are.
	journaled := budgetGraph(true, 1)
	journaled.FactJournal = make([]GraphFactEntry, 32)
	if got := orchestratorAttemptMinutes(journaled, s); got != 10 {
		t.Fatalf("journal bytes miscounted: %d", got)
	}
	// Completed workers' readable artifacts add one minute per 512 KiB.
	s.mu.Lock()
	s.runs["run-reports"] = &Run{ID: "run-reports", Artifacts: []Artifact{
		{Name: "output.txt", Bytes: 300 << 10},
		{Name: "activity.jsonl", Bytes: 220 << 10},
		{Name: "big-report.md", Bytes: 5 << 20}, // within the first eight, so counted
	}}
	s.runs["run-planner"] = &Run{ID: "run-planner", Artifacts: []Artifact{{Name: "proposal", Bytes: 64 << 30}}}
	s.mu.Unlock()
	worker := &GraphMember{Spec: GraphSpec{ID: "worker"}, State: "done", CurrentRunID: "run-reports"}
	// 300 KiB + 220 KiB + 5 MiB = 5740 KiB ≈ 11 minutes of evidence.
	if got := orchestratorAttemptMinutes(budgetGraph(true, 1, worker), s); got != 21 {
		t.Fatalf("artifact evidence miscounted: %d", got)
	}
	// The budget is capped so one heavyweight round cannot outlive the graph.
	heavy := &GraphMember{Spec: GraphSpec{ID: "heavy"}, State: "done", CurrentRunID: "run-planner"}
	if got := orchestratorAttemptMinutes(budgetGraph(true, 1, heavy), s); got != 30 {
		t.Fatalf("budget cap not applied: %d", got)
	}
	// Orchestrator-owned members are not evidence for their own round.
	planner := &GraphMember{OrchestratorFor: "initial", Spec: GraphSpec{ID: "planner"}, State: "done", CurrentRunID: "run-planner"}
	if got := orchestratorAttemptMinutes(budgetGraph(true, 1, planner), s); got != 10 {
		t.Fatalf("planner artifacts leaked into the budget: %d", got)
	}
}
