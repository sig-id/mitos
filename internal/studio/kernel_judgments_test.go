package studio

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/fleet"
)

// judgmentFixture prepares a graph worker attempt whose task carries a
// review-shaped nature. decisionApp, when non-nil, is registered as the graph's
// decision application.
func judgmentFixture(t *testing.T, decisionApp *Application) (*Engine, Run, AgentSession, string, WorkGraph) {
	t.Helper()
	e, req := graphEngine(t, false)
	if decisionApp != nil {
		e.Apps = append(e.Apps, *decisionApp)
		req.Policy.DecisionApplication = decisionApp.ID
	}
	g := createGraphTest(t, e, req)
	spec := graphWorker("worker", g.RootTask)
	spec.Acceptance = "operator"
	g, _ = amendGraphTest(t, e, g, "worker", GraphOperation{Kind: "add", Spec: &spec}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "fixture"})
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var r Run
	for _, m := range g.Members {
		if m.Spec.Kind == "application" {
			r, _ = e.Store.Get(m.CurrentRunID)
		}
	}
	if r.ID == "" {
		t.Fatal("no run")
	}
	if err := e.Store.Transition(r.ID, "fixture_started", "running", "Running", "fixture"); err != nil {
		t.Fatal(err)
	}
	grantProposals(t, e, r.ID, true)
	setNature := func(nature string) {
		t.Helper()
		e.Store.mu.Lock()
		c := graphClone(*e.Store.graphs[r.GraphAttempt.GraphID])
		c.Members[r.TaskID].Spec.Traits = &TaskTraits{Nature: nature, Size: "small", Intelligence: "low"}
		err := e.Store.commit("", "effect", "fixture_nature", change{Graph: &graphChange{Graph: c}})
		e.Store.mu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
	}
	setNature("review")
	a, token, err := e.Store.CreateBoundAgentSession("codex", "Handoff", e.Root, req.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	return e, r, a, token, g
}

func blockingSubmission(key string) ResultSubmission {
	return submission(key, ResultProposal{ID: "dig", Rationale: "Follow-up needs a reviewer", Blocking: true, Tasks: []GraphSpec{proposedTask("dig")}, Artifacts: []string{}})
}

func submit(t *testing.T, e *Engine, a AgentSession, token string, in ResultSubmission) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(in)
	out, err := e.HandoffCall(a.ID, token, "result_submit", raw)
	mapped, _ := out.(map[string]any)
	return mapped, err
}

func TestJudgmentHoldsBlockingSubmissionForOperator(t *testing.T) {
	e, r, a, token, _ := judgmentFixture(t, nil)
	out, err := submit(t, e, a, token, blockingSubmission("yield"))
	if err != nil {
		t.Fatal(err)
	}
	if out["held"] != "kernel_judgment" || out["code"] != "proposal_blocking_not_permitted" {
		t.Fatal("submission not held:", out)
	}
	run, _ := e.Store.Get(r.ID)
	if run.Status != "parked" || run.Decision == nil || run.Result != nil {
		t.Fatal("attempt did not park with a decision:", run.Status, run.Decision != nil)
	}
	d := run.Decision
	if !d.HasOption("apply") || !d.HasOption("refuse") {
		t.Fatal("card candidates:", d.Options)
	}
	if run.HeldJudgment == nil || run.HeldJudgment.State != "pending" || run.HeldJudgment.Mode != "operator" || run.HeldJudgment.Submission == nil {
		t.Fatal("held judgment:", run.HeldJudgment)
	}
	// An identical resubmission stays held; nothing is double-recorded.
	if _, err = submit(t, e, a, token, blockingSubmission("yield")); err != nil {
		t.Fatal(err)
	}
	// A different submission conflicts while the judgment is pending.
	other := blockingSubmission("yield")
	other.Summary = "different"
	if _, err = submit(t, e, a, token, other); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	// The held result is visible to its producer as held, with the reason.
	page := callHandoff(t, e, a, token, "result_get", ResultRead{RunID: r.ID}).(ResultPage)
	if page.State != "held" || page.Summary == "" {
		t.Fatal("held result not visible:", page.State, page.Summary)
	}
}

func TestJudgmentRefuseKeepsCeilingWithFeedback(t *testing.T) {
	e, r, a, token, g := judgmentFixture(t, nil)
	if _, err := submit(t, e, a, token, blockingSubmission("yield")); err != nil {
		t.Fatal(err)
	}
	run, _ := e.Store.Get(r.ID)
	if err := e.Store.Resolve(r.ID, run.Decision.ID, "refuse"); err != nil {
		t.Fatal(err)
	}
	run, _ = e.Store.Get(r.ID)
	if run.Status != "cancelled" || run.Result != nil || run.Decision.Choice != "refuse" || run.Decision.AppliedAt == nil {
		t.Fatal("refusal state:", run.Status, run.Decision.Choice, run.Decision.AppliedAt != nil)
	}
	if len(run.KernelJudgments) != 1 || run.KernelJudgments[0].State != "refused" || run.KernelJudgments[0].Submission != nil {
		t.Fatal("judgment receipt:", run.KernelJudgments)
	}
	graph, _ := e.Store.Graph(g.ID)
	m := graph.Members[r.TaskID]
	if m.State != "queued" || m.ContinueFrom != r.ID || m.Continuation == nil || m.Continuation.Reason != "judgment_refused" || m.Continuation.Refusal == nil || m.Continuation.Refusal.Code != "proposal_blocking_not_permitted" {
		t.Fatal("member continuation:", m.State, m.Continuation)
	}
	if m.Proposed != 1 {
		t.Fatal("refused proposals were not spent:", m.Proposed)
	}
	// Replaying the refused digest keeps the refusal without a new decision.
	if _, err := submit(t, e, a, token, blockingSubmission("yield")); handoffCode(err) != "proposal_blocking_not_permitted" {
		t.Fatal("refused digest resubmitted:", err)
	}
}

func TestJudgmentApplyRecordsYield(t *testing.T) {
	e, r, a, token, g := judgmentFixture(t, nil)
	if _, err := submit(t, e, a, token, blockingSubmission("yield")); err != nil {
		t.Fatal(err)
	}
	run, _ := e.Store.Get(r.ID)
	if err := e.Store.Resolve(r.ID, run.Decision.ID, "apply"); err != nil {
		t.Fatal(err)
	}
	run, _ = e.Store.Get(r.ID)
	if run.Status != "done" || run.Result == nil {
		t.Fatal("applied judgment did not finish the run:", run.Status, run.Result != nil)
	}
	if len(run.Result.ProposalIDs) != 1 || !run.Result.Submission.Proposals[0].Blocking {
		t.Fatal("held submission not recorded:", run.Result)
	}
	if len(run.KernelJudgments) != 1 || run.KernelJudgments[0].State != "applied" || run.KernelJudgments[0].Resolver != "local-operator" {
		t.Fatal("judgment receipt:", run.KernelJudgments)
	}
	graph, _ := e.Store.Graph(g.ID)
	m := graph.Members[r.TaskID]
	if m.Yield == nil || len(m.Yield.Proposals) != 1 || m.State != "yielded" {
		t.Fatal("yield not recorded:", m.State, m.Yield)
	}
	if rec := graph.Proposals[m.Yield.Proposals[0]]; rec == nil || !rec.Blocking {
		t.Fatal("blocking proposal not journaled:", graph.Proposals)
	}
}

func judgmentModelServer(t *testing.T, choice string, confidence float64) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		other := "refuse"
		if choice == "refuse" {
			other = "apply"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "fixture", "answers": map[string]any{"action": map[string]any{"type": "choice", "choice": choice, "confidence": confidence, "probabilities": map[string]float64{choice: 0.95, other: 0.05}}}})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func decisionApplication(t *testing.T, server *httptest.Server) *Application {
	t.Helper()
	app := executionApp(t, "true")
	app.ID = "judgment-model"
	app.DecisionModel = modelConfig(server.URL, "before_segment")
	return &app
}

func TestJudgmentDecisionApplicationApplies(t *testing.T) {
	server, calls := judgmentModelServer(t, "apply", 0.99)
	app := decisionApplication(t, server)
	e, r, a, token, g := judgmentFixture(t, app)
	out, err := submit(t, e, a, token, blockingSubmission("yield"))
	if err != nil {
		t.Fatal(err)
	}
	if out["held"] != nil {
		t.Fatal("decision application did not decide inline:", out)
	}
	if calls.Load() != 1 {
		t.Fatal("inference calls:", calls.Load())
	}
	run, _ := e.Store.Get(r.ID)
	if run.Status != "running" || run.Result == nil || len(run.KernelJudgments) != 1 || run.KernelJudgments[0].State != "applied" || run.KernelJudgments[0].Mode != "decision_application" || run.KernelJudgments[0].Answer == nil {
		t.Fatal("inline apply state:", run.Status, run.Result != nil, run.KernelJudgments)
	}
	graph, _ := e.Store.Graph(g.ID)
	if graph.OrchestratorCalls == 0 {
		t.Fatal("judgment not counted against the decision allowance")
	}
	if err = e.Store.Complete(r.ID, run.Result.Submission.Summary, false); err != nil {
		t.Fatal(err)
	}
	graph, _ = e.Store.Graph(g.ID)
	if m := graph.Members[r.TaskID]; m.Yield == nil {
		t.Fatal("yield not recorded after completion")
	}
}

func TestJudgmentDecisionApplicationRefuses(t *testing.T) {
	server, _ := judgmentModelServer(t, "refuse", 0.99)
	app := decisionApplication(t, server)
	e, r, a, token, g := judgmentFixture(t, app)
	_, err := submit(t, e, a, token, blockingSubmission("yield"))
	if handoffCode(err) != "proposal_blocking_not_permitted" {
		t.Fatal("expected refusal:", err)
	}
	var failure *HandoffFailure
	if !errors.As(err, &failure) || failure.Detail == "" || !strings.Contains(failure.Detail, "blocking=false") {
		t.Fatal("refusal lacks feedback:", err)
	}
	run, _ := e.Store.Get(r.ID)
	if run.Result != nil || run.Status != "running" || len(run.KernelJudgments) != 1 || run.KernelJudgments[0].State != "refused" {
		t.Fatal("inline refuse state:", run.Status, run.Result != nil, run.KernelJudgments)
	}
	// The worker can immediately resubmit non-blocking follow-up work.
	if _, err = submit(t, e, a, token, submission("follow-up", ResultProposal{ID: "dig", Rationale: "r", Tasks: []GraphSpec{proposedTask("dig")}, Artifacts: []string{}})); err != nil {
		t.Fatal(err)
	}
	run, _ = e.Store.Get(r.ID)
	if run.Result == nil {
		t.Fatal("non-blocking resubmission not recorded")
	}
	_ = g
}

func TestJudgmentEscalatesUncertainApplicationToOperator(t *testing.T) {
	server, calls := judgmentModelServer(t, "apply", 0.1)
	app := decisionApplication(t, server)
	e, r, a, token, _ := judgmentFixture(t, app)
	out, err := submit(t, e, a, token, blockingSubmission("yield"))
	if err != nil {
		t.Fatal(err)
	}
	if out["held"] != "kernel_judgment" {
		t.Fatal("uncertain decision did not escalate:", out)
	}
	if calls.Load() != 1 {
		t.Fatal("inference calls:", calls.Load())
	}
	run, _ := e.Store.Get(r.ID)
	if run.Status != "parked" || run.Decision == nil || run.HeldJudgment == nil || run.HeldJudgment.Mode != "operator" {
		t.Fatal("escalation state:", run.Status, run.HeldJudgment)
	}
	if err = e.Store.Resolve(r.ID, run.Decision.ID, "apply"); err != nil {
		t.Fatal(err)
	}
	run, _ = e.Store.Get(r.ID)
	if run.Status != "done" || run.Result == nil || len(run.KernelJudgments) != 1 || run.KernelJudgments[0].State != "applied" || run.KernelJudgments[0].Reason != "confidence_threshold" {
		t.Fatal("operator fallback apply:", run.Status, run.KernelJudgments)
	}
}

func TestJudgmentAllowanceExhaustedFailsClosed(t *testing.T) {
	server, calls := judgmentModelServer(t, "apply", 0.99)
	app := decisionApplication(t, server)
	e, r, a, token, g := judgmentFixture(t, app)
	e.Store.mu.Lock()
	c := graphClone(*e.Store.graphs[g.ID])
	c.Limits.OrchestratorCalls = c.OrchestratorCalls
	if err := e.Store.commit("", "effect", "fixture_allowance", change{Graph: &graphChange{Graph: c}}); err != nil {
		e.Store.mu.Unlock()
		t.Fatal(err)
	}
	e.Store.mu.Unlock()
	_, err := submit(t, e, a, token, blockingSubmission("yield"))
	if handoffCode(err) != "proposal_blocking_not_permitted" {
		t.Fatal("expected conservative refusal:", err)
	}
	if calls.Load() != 0 {
		t.Fatal("inference was called without allowance")
	}
	run, _ := e.Store.Get(r.ID)
	if run.Status != "running" || run.Decision != nil || len(run.KernelJudgments) != 0 {
		t.Fatal("exhausted allowance must not park or journal:", run.Status, run.Decision != nil, run.KernelJudgments)
	}
}

func TestJudgmentReplayAfterRestart(t *testing.T) {
	e, r, a, token, _ := judgmentFixture(t, nil)
	if _, err := submit(t, e, a, token, blockingSubmission("yield")); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.recoverKernelJudgments(); err != nil {
		t.Fatal(err)
	}
	run, _ := e.Store.Get(r.ID)
	if run.HeldJudgment == nil || run.HeldJudgment.State != "pending" {
		t.Fatal("operator judgment must survive recovery:", run.HeldJudgment)
	}
	// The agent completion path meets a held attempt: no result, pending
	// judgment. It must route through Complete, which keeps the attempt
	// parked (releasing its claim) so the card stays resolvable.
	current, err := e.Store.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Result != nil || current.HeldJudgment == nil || current.HeldJudgment.State != "pending" {
		t.Fatal("fixture does not match the completion-path precondition")
	}
	if err = e.Store.Complete(r.ID, "Result held for a kernel judgment; evidence is preserved.", false); err != nil {
		t.Fatal(err)
	}
	run, _ = e.Store.Get(r.ID)
	if run.Status != "parked" || run.Result != nil {
		t.Fatal("held run completed while judgment pending:", run.Status)
	}
	if run.Claim != nil && run.Claim.ReleasedAt == nil {
		t.Fatal("held run kept its execution claim")
	}
	// The card remains resolvable after the attempt ended its execution.
	if err = e.Store.Resolve(r.ID, run.Decision.ID, "apply"); err != nil {
		t.Fatal(err)
	}
	run, _ = e.Store.Get(r.ID)
	if run.Status != "done" || run.Result == nil || len(run.KernelJudgments) != 1 || run.KernelJudgments[0].State != "applied" {
		t.Fatal("apply after completion-path park:", run.Status, run.KernelJudgments)
	}
}

func TestJudgmentApplyRefusesWhenStateChanged(t *testing.T) {
	e, r, a, token, g := judgmentFixture(t, nil)
	if _, err := submit(t, e, a, token, blockingSubmission("yield")); err != nil {
		t.Fatal(err)
	}
	// The task is cancelled while the judgment is pending; an apply can no
	// longer proceed, so the conservative refusal semantics apply.
	e.Store.mu.Lock()
	c := graphClone(*e.Store.graphs[g.ID])
	c.Members[r.TaskID].CancelRequested = true
	if err := e.Store.commit("", "effect", "fixture_cancel", change{Graph: &graphChange{Graph: c}}); err != nil {
		e.Store.mu.Unlock()
		t.Fatal(err)
	}
	e.Store.mu.Unlock()
	run, _ := e.Store.Get(r.ID)
	if err := e.Store.Resolve(r.ID, run.Decision.ID, "apply"); err != nil {
		t.Fatal(err)
	}
	run, _ = e.Store.Get(r.ID)
	if run.Status != "cancelled" || run.Result != nil || run.Decision.Choice != "apply" {
		t.Fatal("stale apply state:", run.Status, run.Decision.Choice)
	}
	if len(run.KernelJudgments) != 1 || run.KernelJudgments[0].State != "refused" || run.KernelJudgments[0].Reason == "" {
		t.Fatal("stale apply receipt:", run.KernelJudgments)
	}
}

func TestJudgmentSurvivesJournalReplay(t *testing.T) {
	e, r, a, token, _ := judgmentFixture(t, nil)
	if _, err := submit(t, e, a, token, blockingSubmission("yield")); err != nil {
		t.Fatal(err)
	}
	dir := e.Store.Dir()
	if err := e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	run, err := reopened.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "parked" || run.Decision == nil || run.HeldJudgment == nil || run.HeldJudgment.State != "pending" || run.HeldJudgment.Submission == nil {
		t.Fatal("held judgment did not replay:", run.Status, run.HeldJudgment)
	}
	if err = reopened.Resolve(r.ID, run.Decision.ID, "refuse"); err != nil {
		t.Fatal(err)
	}
	run, _ = reopened.Get(r.ID)
	if run.Status != "cancelled" || len(run.KernelJudgments) != 1 || run.KernelJudgments[0].State != "refused" {
		t.Fatal("refusal after replay:", run.Status, run.KernelJudgments)
	}
}

func TestJudgmentRecoveryRefusesEndedAttempts(t *testing.T) {
	e, r, a, token, _ := judgmentFixture(t, nil)
	if _, err := submit(t, e, a, token, blockingSubmission("yield")); err != nil {
		t.Fatal(err)
	}
	// The attempt is stopped while the operator card is still pending; the
	// held submission can never be applied, so recovery refuses it.
	if err := e.Store.Transition(r.ID, "run_cancelled", "cancelled", "Stopped", "Stopped before the judgment settled."); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.recoverKernelJudgments(); err != nil {
		t.Fatal(err)
	}
	run, _ := e.Store.Get(r.ID)
	if run.HeldJudgment != nil || len(run.KernelJudgments) != 1 || run.KernelJudgments[0].State != "refused" || run.KernelJudgments[0].Reason != "attempt_ended_before_decision" {
		t.Fatal("ended attempt judgment:", run.HeldJudgment, run.KernelJudgments)
	}
}

func TestJudgmentRefuseDefersUntilHarvestFinishes(t *testing.T) {
	e, r, a, token, g := judgmentFixture(t, nil)
	if _, err := submit(t, e, a, token, blockingSubmission("yield")); err != nil {
		t.Fatal(err)
	}
	// The operator refuses while the worker's execution is still open.
	e.Store.mu.Lock()
	claim := fleet.Claim{ID: newID("claim"), RunID: r.ID, Workspace: r.Brief.Workspace, AcquiredAt: time.Now().UTC()}
	if err := e.Store.commit(r.ID, "effect", "fixture_claim", change{Claim: &claim}); err != nil {
		e.Store.mu.Unlock()
		t.Fatal(err)
	}
	e.Store.mu.Unlock()
	run, _ := e.Store.Get(r.ID)
	if err := e.Store.Resolve(r.ID, run.Decision.ID, "refuse"); err != nil {
		t.Fatal(err)
	}
	// The decision is journaled but the attempt stays nonterminal so the
	// worker can finish saving its output and workspace.
	run, _ = e.Store.Get(r.ID)
	if run.Status != "parked" || run.Result != nil || len(run.KernelJudgments) != 1 || run.KernelJudgments[0].State != "refused" {
		t.Fatal("refused run left the parked state:", run.Status, run.KernelJudgments)
	}
	if err := e.Store.SaveArtifact(r.ID, "report.md", []byte("final report")); err != nil {
		t.Fatal("harvest rejected on a refused-but-open attempt:", err)
	}
	if err := e.Store.RecordWorkspace(r.ID, "workspace_harvested", "Saved agent output.", WorkspaceRecord{Mode: "copy", BaselineCommit: strings.Repeat("a", 40), Fingerprint: strings.Repeat("b", 64), ChangedFiles: 1}); err != nil {
		t.Fatal("workspace record rejected on a refused-but-open attempt:", err)
	}
	graph, _ := e.Store.Graph(g.ID)
	if m := graph.Members[r.TaskID]; m.CurrentRunID != r.ID {
		t.Fatal("member requeued before harvest finished")
	}
	// The completion path finalizes the refusal: cancelled attempt, released
	// claim, and a requeued member with the feedback continuation.
	if err := e.Store.Complete(r.ID, "Judgment refused", false); err != nil {
		t.Fatal(err)
	}
	run, _ = e.Store.Get(r.ID)
	if run.Status != "cancelled" || run.Claim == nil || run.Claim.ReleasedAt == nil {
		t.Fatal("refusal not finalized:", run.Status, run.Claim)
	}
	graph, _ = e.Store.Graph(g.ID)
	m := graph.Members[r.TaskID]
	if m.State != "queued" || m.ContinueFrom != r.ID || m.Continuation == nil || m.Continuation.Reason != "judgment_refused" || m.Proposed != 1 {
		t.Fatal("feedback continuation not queued:", m.State, m.Continuation, m.Proposed)
	}
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	graph, _ = e.Store.Graph(g.ID)
	if m = graph.Members[r.TaskID]; m.CurrentRunID == "" || m.CurrentRunID == r.ID {
		t.Fatal("continuation attempt not queued after finalization")
	}
}
