package studio

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/watch"
)

// A running agent attempt under budget evaluation, with supervision wired so
// budget events reach a watch (#126).
func budgetedAttempt(t *testing.T) (*Engine, *Store, WorkGraph, string) {
	t.Helper()
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	s := e.Store
	agent := &AgentBinding{Runtime: "codex", Selection: &ExecutionSelection{Harness: "codex", HarnessName: "codex", Traits: TaskTraits{Nature: "implement", Size: "small", Intelligence: "low"}}}
	run := createRun(t, s)
	s.mu.Lock()
	current := s.runs[run.ID]
	current.TaskID = "worker"
	current.ProjectID = g.ProjectID
	current.GraphAttempt = &GraphAttempt{GraphID: g.ID}
	current.Status = "running"
	s.runs[run.ID] = current
	clone := graphClone(g)
	clone.Members["worker"] = &GraphMember{Spec: GraphSpec{ID: "worker", Parent: g.RootTask, Kind: "application", Required: true, Acceptance: "operator", Criteria: "Report 42", Brief: Brief{Title: "Produce answer"}}, Executor: &GraphExecutor{Application: Application{Agent: agent}}, Revision: 1, FactRevision: 1, State: "running", CurrentRunID: run.ID, Attempts: []string{run.ID}}
	s.mu.Unlock()
	clone = commitSupervision(t, s, clone, []string{"worker"}, nil)
	return e, s, clone, run.ID
}

// armBudget journals a budget state for the attempt with short windows.
func armBudget(t *testing.T, s *Store, run string, mutate func(*AttemptBudget)) {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := &AttemptBudget{SoftMinutes: 1, ProgressMinutes: 1, MaxExtensions: 2, GraceSeconds: 1, StartedAt: time.Now().UTC().Add(-10 * time.Minute), LastProgress: time.Now().UTC(), Conditions: map[string]AttemptCondition{}}
	if mutate != nil {
		mutate(state)
	}
	if err := s.commit(run, "effect", "attempt_budget_started", change{AttemptBudget: &attemptBudgetChange{Budget: state, Summary: "armed"}}); err != nil {
		t.Fatal(err)
	}
}

func budgetOf(t *testing.T, s *Store, run string) *AttemptBudget {
	t.Helper()
	r, err := s.Get(run)
	if err != nil {
		t.Fatal(err)
	}
	return r.AttemptBudget
}

func runEventsOf(t *testing.T, s *Store, run string) []string {
	t.Helper()
	r, err := s.Get(run)
	if err != nil {
		t.Fatal(err)
	}
	kinds := []string{}
	for _, e := range r.Events {
		kinds = append(kinds, e.Kind)
	}
	return kinds
}

func hasEvent(t *testing.T, s *Store, run, kind string) bool {
	t.Helper()
	for _, k := range runEventsOf(t, s, run) {
		if k == kind {
			return true
		}
	}
	return false
}

// Steady progress past the soft budget with no provider extends until the
// allowance is spent, then wraps up, then stops after the grace period
// (#128).
func TestBudgetOverrunExtendsThenWrapsUpThenStops(t *testing.T) {
	e, s, g, run := budgetedAttempt(t)
	_ = g
	armBudget(t, s, run, nil)
	// First overrun with recent progress: extend (default, provider down).
	if err := e.evaluateAttemptBudgets(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !hasEvent(t, s, run, "attempt_overrun") || !hasEvent(t, s, run, "attempt_extended") {
		t.Fatalf("first judgment wrong: %v", runEventsOf(t, s, run))
	}
	if b := budgetOf(t, s, run); b.Extensions != 1 || b.ExtendedMinutes != 1 {
		t.Fatalf("extension not granted: %+v", b)
	}
	// Second overrun after the extension: extend again.
	armBudget(t, s, run, func(b *AttemptBudget) { b.StartedAt = time.Now().UTC().Add(-5 * time.Minute) })
	s.mu.Lock()
	b := s.runs[run].AttemptBudget
	b.Extensions, b.ExtendedMinutes = 1, 1
	s.mu.Unlock()
	if err := e.evaluateAttemptBudgets(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if b := budgetOf(t, s, run); b.Extensions != 2 {
		t.Fatalf("second extension missing: %+v", b)
	}
	// Third overrun: the allowance is spent, so wrap up.
	s.mu.Lock()
	s.runs[run].AttemptBudget.Extensions, s.runs[run].AttemptBudget.ExtendedMinutes = 2, 2
	s.runs[run].AttemptBudget.StartedAt = time.Now().UTC().Add(-5 * time.Minute)
	s.mu.Unlock()
	if err := e.evaluateAttemptBudgets(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	budget := budgetOf(t, s, run)
	if !hasEvent(t, s, run, "attempt_wrap_up") || budget.WrapUpAt == nil {
		t.Fatalf("wrap-up missing: %+v events=%v", budget, runEventsOf(t, s, run))
	}
	// Grace expiry stops the attempt; evidence and the journal stay.
	time.Sleep(1100 * time.Millisecond)
	if err := e.evaluateAttemptBudgets(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	finished, _ := s.Get(run)
	if !finished.Terminal() {
		t.Fatalf("wrapped attempt not stopped: %s", finished.Status)
	}
}

// No progress for the progress deadline raises attempt_stalled and wraps up
// by default; the event reaches the supervising watch at wake (#126, #128).
func TestBudgetStallWrapsUpAndReachesWatch(t *testing.T) {
	e, s, g, run := budgetedAttempt(t)
	armBudget(t, s, run, func(b *AttemptBudget) { b.LastProgress = time.Now().UTC().Add(-10 * time.Minute) })
	if err := e.evaluateAttemptBudgets(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !hasEvent(t, s, run, "attempt_stalled") || !hasEvent(t, s, run, "attempt_wrap_up") {
		t.Fatalf("stall judgment wrong: %v", runEventsOf(t, s, run))
	}
	if b := budgetOf(t, s, run); b.JudgmentPending != "" || b.WrapUpAt == nil {
		t.Fatalf("judgment not settled: %+v", b)
	}
	if err := s.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	watchID := g.Members["worker"].SupervisionWatch
	got := deliveries(t, s, watchID)
	found := false
	for _, d := range got {
		if containsReason(d.Reasons, "attempt_stalled") && d.Level == watch.Wake {
			found = true
		}
	}
	if !found {
		t.Fatalf("supervising watch missed the stall: %+v", got)
	}
}

// A confident stop answer is accepted; a below-threshold stop falls back to
// the default wrap_up (#110 asymmetry, #128).
func TestBudgetDecisionAcceptanceIsAsymmetric(t *testing.T) {
	for _, test := range []struct {
		name       string
		confidence float64
		choice     string
		want       string
		source     string
	}{
		{"confident stop wins", 1, "stop", "stop", "decision"},
		{"unconvincing answer falls back", 0.2, "stop", "wrap_up", "fallback:"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				probs := map[string]float64{"extend": 0, "wrap_up": 0, "stop": 0}
				for choice := range probs {
					if choice == test.choice {
						probs[choice] = test.confidence
					} else {
						probs[choice] = (1 - test.confidence) / 2
					}
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "answers": map[string]any{"action": map[string]any{"type": "choice", "choice": test.choice, "confidence": test.confidence, "probabilities": probs}}})
			}))
			t.Cleanup(server.Close)
			e, s, g, run := budgetedAttempt(t)
			if _, err := e.SaveSystemConfiguration(SystemConfiguration{DecisionProvider: modelConfig(server.URL, "before_segment")}); err != nil {
				t.Fatal(err)
			}
			policy := s.AgentPolicy(g.ProjectID)
			policy.DecisionProvider = modelConfig(server.URL, "before_segment")
			if _, err := e.SaveAgentPolicy(g.ProjectID, policy); err != nil {
				t.Fatal(err)
			}
			armBudget(t, s, run, func(b *AttemptBudget) { b.LastProgress = time.Now().UTC().Add(-10 * time.Minute) })
			if err := e.evaluateAttemptBudgets(time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if !hasEvent(t, s, run, "attempt_stalled") {
				t.Fatalf("stall missing: %v", runEventsOf(t, s, run))
			}
			settled := false
			for _, ev := range mustRun(t, s, run).Events {
				if ev.Kind != "attempt_judgment_settled" && ev.Kind != "attempt_wrap_up" {
					continue
				}
				var c change
				if json.Unmarshal(ev.Data, &c) != nil || c.AttemptBudget == nil || c.AttemptBudget.Evidence == nil {
					continue
				}
				var outcome struct {
					Outcome string `json:"outcome"`
					Source  string `json:"source"`
				}
				_ = json.Unmarshal(c.AttemptBudget.Evidence, &outcome)
				if outcome.Outcome != test.want || !containsSubstring(outcome.Source, test.source) {
					t.Fatalf("outcome %s via %s, want %s via %s", outcome.Outcome, outcome.Source, test.want, test.source)
				}
				settled = true
			}
			if !settled {
				t.Fatalf("no settled judgment: %v", runEventsOf(t, s, run))
			}
		})
	}
}

func mustRun(t *testing.T, s *Store, run string) Run {
	t.Helper()
	r, err := s.Get(run)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func containsSubstring(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle || len(needle) == 0 || indexOf(haystack, needle) >= 0)
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}

// The graph's elapsed ceiling stops the attempt whatever the provider says
// (#128).
func TestBudgetGraphCeilingStopsRegardless(t *testing.T) {
	e, s, g, run := budgetedAttempt(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "answers": map[string]any{"action": map[string]any{"type": "choice", "choice": "extend", "confidence": 1, "probabilities": map[string]float64{"extend": 1}}}})
	}))
	t.Cleanup(server.Close)
	if _, err := e.SaveSystemConfiguration(SystemConfiguration{DecisionProvider: modelConfig(server.URL, "before_segment")}); err != nil {
		t.Fatal(err)
	}
	policy := s.AgentPolicy(g.ProjectID)
	policy.DecisionProvider = modelConfig(server.URL, "before_segment")
	if _, err := e.SaveAgentPolicy(g.ProjectID, policy); err != nil {
		t.Fatal(err)
	}
	armBudget(t, s, run, nil)
	// The graph's deadline is behind us: the ceiling applies.
	s.mu.Lock()
	expired := graphClone(g)
	expired.Limits.Deadline = time.Now().UTC().Add(-time.Minute)
	gc := graphChange{Graph: expired}
	s.projectGraph(&gc, s.graphs[g.ID])
	if err := s.commit("", "effect", "graph_test_expired", change{Graph: &gc}); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()
	if err := e.runBudgetJudgment(run, g.ID, "attempt_overrun", "extend", budgetJudgmentEvidence{Event: "attempt_overrun"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range mustRun(t, s, run).Events {
		if ev.Kind != "attempt_judgment_settled" {
			continue
		}
		var c change
		if json.Unmarshal(ev.Data, &c) != nil || c.AttemptBudget == nil {
			continue
		}
		var outcome struct {
			Outcome string `json:"outcome"`
			Source  string `json:"source"`
		}
		_ = json.Unmarshal(c.AttemptBudget.Evidence, &outcome)
		if outcome.Outcome != "stop" || outcome.Source != "ceiling" {
			t.Fatalf("ceiling not applied: %+v", outcome)
		}
		found = true
	}
	if !found {
		t.Fatalf("no ceiling judgment: %v", runEventsOf(t, s, run))
	}
}

// A judgment interrupted by a restart settles with its default and
// outcome_unknown consumption, never a second dispatch (#128).
func TestBudgetJudgmentRestartAppliesDefault(t *testing.T) {
	e, s, _, run := budgetedAttempt(t)
	armBudget(t, s, run, func(b *AttemptBudget) { b.JudgmentPending = "judgment_interrupted" })
	if err := e.evaluateAttemptBudgets(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if !hasEvent(t, s, run, "attempt_wrap_up") {
		t.Fatalf("interrupted judgment not settled: %v", runEventsOf(t, s, run))
	}
	for _, ev := range mustRun(t, s, run).Events {
		if ev.Kind != "attempt_wrap_up" {
			continue
		}
		var c change
		if json.Unmarshal(ev.Data, &c) != nil || c.AttemptBudget == nil {
			continue
		}
		var outcome struct {
			Source string `json:"source"`
		}
		_ = json.Unmarshal(c.AttemptBudget.Evidence, &outcome)
		if outcome.Source != "fallback:outcome_unknown" {
			t.Fatalf("restart outcome wrong: %+v", outcome)
		}
	}
}

// Replaying the journal rebuilds the same conditions, extensions and vector
// (#128).
func TestBudgetStateSurvivesReplay(t *testing.T) {
	_, s, _, run := budgetedAttempt(t)
	dir := s.Dir()
	armBudget(t, s, run, nil)
	if err := s.recordAttemptProgress(run, ProgressVector{ToolCalls: 3, OutputBytes: 100, WorkspaceFingerprint: "abc"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.settleAttemptJudgmentLockedForTest(run, "extend", "fallback:provider_unconfigured", "test", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	before := budgetOf(t, s, run)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	after := budgetOf(t, s, run)
	if after == nil || after.Extensions != before.Extensions || after.ExtendedMinutes != before.ExtendedMinutes || after.Vector != before.Vector || after.LastProgress.IsZero() {
		t.Fatalf("budget state changed on replay: %+v then %+v", before, after)
	}
}

// settleAttemptJudgmentLockedForTest exposes the settling path for tests.
func (s *Store) settleAttemptJudgmentLockedForTest(id, outcome, source, detail string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.settleAttemptJudgmentLocked(id, outcome, source, detail, at)
}

// Progress is journalled only when the vector advances, and the heartbeat
// carries it (#128).
func TestProgressVectorJournalledOnChange(t *testing.T) {
	_, s, _, run := budgetedAttempt(t)
	armBudget(t, s, run, nil)
	first := ProgressVector{ToolCalls: 1, OutputBytes: 10, WorkspaceFingerprint: "a"}
	if err := s.recordAttemptProgress(run, first, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := s.recordAttemptProgress(run, first, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, k := range runEventsOf(t, s, run) {
		if k == "attempt_progress" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("unchanged vector journalled: %d progress events", count)
	}
	session, token, err := s.CreateBoundAgentSession("codex", "Progress", t.TempDir(), "proj", "", run, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	second := ProgressVector{ToolCalls: 2, OutputBytes: 20, WorkspaceFingerprint: "b"}
	if err := s.AgentHeartbeatProgress(session.ID, token, &second); err != nil {
		t.Fatal(err)
	}
	s.mu.RLock()
	recorded := s.agentSessions[session.ID].Progress
	s.mu.RUnlock()
	if recorded == nil || *recorded != second {
		t.Fatalf("heartbeat did not carry the vector: %+v", recorded)
	}
}

// The extension allowance is enforced at settlement: a confident provider
// extend answer beyond the cap wraps up instead (#128 review).
func TestExtensionAllowanceEnforcedAtSettlement(t *testing.T) {
	_, s, _, run := budgetedAttempt(t)
	armBudget(t, s, run, func(b *AttemptBudget) { b.Extensions, b.ExtendedMinutes = 2, 2 })
	if err := s.settleAttemptJudgmentLockedForTest(run, "extend", "decision", "confident but exhausted", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	b := budgetOf(t, s, run)
	if b.Extensions != 2 || b.WrapUpAt == nil || !hasEvent(t, s, run, "attempt_wrap_up") {
		t.Fatalf("exhausted allowance extended anyway: %+v events=%v", b, runEventsOf(t, s, run))
	}
}

// A native agent attempt's execution deadline starts from the graph ceiling,
// not the soft budget, so extensions are not cancelled mid-flight; scheduling
// deadlines still cap the run, and non-agent runs keep the brief timeout
// (#128 review).
func TestAgentDeadlineStartsFromGraphCeiling(t *testing.T) {
	root := t.TempDir()
	app := executionApp(t, `sleep 30 & wait`)
	e, s := testEngine(t, root, []Application{app})
	project, err := e.Desk.CreateProject("Deadline test", root)
	if err != nil {
		t.Fatal(err)
	}
	g := createGraphTest(t, e, CreateGraph{Profile: GraphProfile, Key: "k", ProjectID: project.ID, GoalRevision: 1, Activation: "manual", Policy: GraphPolicy{Mode: "adaptive", Applications: []string{"change-app"}}, Limits: GraphLimits{Tasks: 8, Amendments: 16, Attempts: 8, Retries: 2, Expansions: 4, OrchestratorCalls: 8, Depth: 8, Fanout: 8, Deadline: time.Now().UTC().Add(6 * time.Hour)}, Tasks: []GraphSpec{{ID: "goal", Kind: "scope", Required: true, Criteria: "Do it", Acceptance: "operator", Brief: Brief{Title: "Goal"}}}})
	agent := &AgentBinding{Runtime: "codex", Selection: &ExecutionSelection{Harness: "codex", HarnessName: "codex", Traits: TaskTraits{Size: "small"}}}
	r, err := s.Create(Brief{Title: "Agent", Workspace: root, AppID: "change-app", TimeoutMinutes: 5}, "agent", "", false)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	current := s.runs[r.ID]
	current.GraphAttempt = &GraphAttempt{GraphID: g.ID, Executor: GraphExecutor{Application: Application{Agent: agent}}}
	s.runs[r.ID] = current
	s.mu.Unlock()
	fetched, _ := s.Get(r.ID)
	if d := e.executionDeadline(fetched); d.Sub(time.Now()) < 5*time.Hour {
		t.Fatalf("agent deadline still bound to the soft budget: %v", d)
	}
	// A scheduling deadline still caps the run.
	earlier := time.Now().UTC().Add(30 * time.Minute)
	fetched.Brief.Scheduling.Deadline = &earlier
	if d := e.executionDeadline(fetched); d.After(earlier.Add(time.Minute)) {
		t.Fatalf("scheduling deadline ignored: %v", d)
	}
	// Non-agent runs keep the brief timeout.
	plain, err := s.Create(Brief{Title: "Plain", Workspace: root, AppID: "change-app", TimeoutMinutes: 5}, app.Name, "", false)
	if err != nil {
		t.Fatal(err)
	}
	plainRun, _ := s.Get(plain.ID)
	if d := e.executionDeadline(plainRun); d.Sub(time.Now()) > 6*time.Minute {
		t.Fatalf("plain run lost its brief timeout: %v", d)
	}
}
