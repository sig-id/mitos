package studio

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Calibration must count completed model actions once per action with the
// harness limit's semantics, and recognize budget exhaustion from structured
// evidence rather than adapter summary text.
func TestExecutionCalibrationCountsActionsAndExhaustion(t *testing.T) {
	if n := countCompletedActions([]byte(strings.Join([]string{
		`{"mitos_activity":1,"kind":"tool","id":"a","status":"running"}`,
		`{"mitos_activity":1,"kind":"tool","id":"a","status":"completed"}`,
		`{"mitos_activity":1,"kind":"command","id":"b","command":"ls","exit_code":0,"status":"completed"}`,
		`{"mitos_activity":1,"kind":"file","id":"c","name":"docs/a.md","status":"completed"}`,
		`{"mitos_activity":1,"kind":"message","id":"d","text":"thinking out loud"}`,
		`{"mitos_activity":1,"kind":"message","id":"e","text":"done"}`,
		`not json`,
	}, "\n"))); n != 3 {
		t.Fatalf("completed actions: got %d, want 3", n)
	}
	if !budgetExhausted(&Run{Events: []Event{{Kind: "execution_budget_exhausted"}}}) {
		t.Fatal("structured exhaustion marker ignored")
	}
	for _, legacy := range []string{
		"The time limit was reached. Review the output and try again with a longer limit.",
		"agent reached its limit of 160 model steps",
		"agent model-step limit reached",
	} {
		if !budgetExhausted(&Run{Summary: legacy}) {
			t.Fatalf("legacy exhaustion summary unrecognized: %q", legacy)
		}
	}
	if budgetExhausted(&Run{Summary: "codex stopped: EOF"}) {
		t.Fatal("ordinary failure counted as exhaustion")
	}

	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	s := e.Store
	selection := &ExecutionSelection{Harness: "codex", HarnessName: "codex", Tier: "high", Traits: TaskTraits{Nature: "implement", Size: "large", Intelligence: "high"}, Budget: SizeBudget{Minutes: 60, MaxTurns: 160}}
	agent := &AgentBinding{Runtime: "codex", Selection: selection}
	s.mu.Lock()
	gr := s.graphs[g.ID]
	gr.Members["worker"] = &GraphMember{Spec: GraphSpec{ID: "worker", Parent: g.RootTask, Kind: "application", Required: true, Acceptance: "application"}, Executor: &GraphExecutor{Application: Application{Agent: agent}}, Requester: "orchestrator", Revision: 1, FactRevision: 1, State: "done"}
	done := Run{ID: "run-done", TaskID: "worker", ProjectID: req.ProjectID, Status: "done", GraphAttempt: &GraphAttempt{GraphID: g.ID, Executor: GraphExecutor{Application: Application{Agent: agent}}}, CreatedAt: time.Now().UTC().Add(-time.Minute), UpdatedAt: time.Now().UTC()}
	exhausted := Run{ID: "run-limit", TaskID: "worker", ProjectID: req.ProjectID, Status: "failed", Summary: "agent reached its limit of 160 model steps", Events: []Event{{Kind: "execution_budget_exhausted"}}, GraphAttempt: &GraphAttempt{GraphID: g.ID, Executor: GraphExecutor{Application: Application{Agent: agent}}}, CreatedAt: time.Now().UTC().Add(-time.Minute), UpdatedAt: time.Now().UTC()}
	gr.Members["worker"].Attempts = []string{done.ID, exhausted.ID}
	s.runs[done.ID] = &done
	s.runs[exhausted.ID] = &exhausted
	s.mu.Unlock()
	activity := []byte(`{"kind":"tool","id":"a","status":"running"}` + "\n" + `{"kind":"tool","id":"a","status":"completed"}` + "\n" + `{"kind":"command","id":"b","status":"completed"}` + "\n" + `{"kind":"file","id":"c","name":"docs/a.md","status":"completed"}` + "\n")
	sum := sha256.Sum256(activity)
	s.mu.Lock()
	s.runs[done.ID].Artifacts = append(s.runs[done.ID].Artifacts, Artifact{Name: "activity.jsonl", SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(activity))})
	s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(s.dir, "artifacts", done.ID), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.dir, "artifacts", done.ID, "activity.jsonl"), activity, 0o600); err != nil {
		t.Fatal(err)
	}
	groups, err := s.ExecutionCalibration(req.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("groups: %+v", groups)
	}
	c := groups[0]
	if c.Requester != "orchestrator" || c.Nature != "implement" || c.Size != "large" || c.Intelligence != "high" || c.BudgetTurns != 160 {
		t.Fatalf("group keyed wrong: %+v", c)
	}
	if c.Attempts != 2 || c.Finished != 1 || c.BudgetExhausted != 1 {
		t.Fatalf("counts wrong: %+v", c)
	}
	// One finished attempt: three completed actions plus the final answer. The
	// running tool record and the messages never count.
	if c.MedianTurns != 4 {
		t.Fatalf("median turns: got %v, want 4", c.MedianTurns)
	}
}
