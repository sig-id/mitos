package studio

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"mitos/internal/decision"
	"mitos/internal/workpool"
)

func grantProposals(t *testing.T, e *Engine, run string, granted bool) {
	t.Helper()
	receipt, _ := json.Marshal(AuthorityReceipt{Policy: EnforcementAdvisory, Runtime: "codex", Capabilities: []CapabilityReceipt{{Name: ProposalCapability, Granted: granted, Status: CapabilityEnforced}}})
	if err := e.Store.SaveArtifact(run, "authority.json", receipt); err != nil {
		t.Fatal(err)
	}
}

func proposedTask(id string) GraphSpec {
	return GraphSpec{ID: id, Kind: "application", Required: true, Criteria: "Dig deeper", Acceptance: "operator", Brief: Brief{Title: "Dig deeper", Intent: "Investigate the sub-question", AppID: "workspace-inventory", TimeoutMinutes: 1}}
}

func submission(key string, proposals ...ResultProposal) ResultSubmission {
	return ResultSubmission{Version: HandoffVersion, Key: key, Summary: "Partial findings", Proposals: proposals}
}

func handoffCode(err error) string {
	var failure *HandoffFailure
	if errors.As(err, &failure) {
		return failure.Code
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

func TestProposalSubmissionIsGatedTypedAndBounded(t *testing.T) {
	e, r, a, token := handoffFixture(t)
	valid := ResultProposal{ID: "dig", Rationale: "Needs a separate investigation", Blocking: true, Tasks: []GraphSpec{proposedTask("dig")}, Artifacts: []string{}}
	call := func(in any) error {
		raw, _ := json.Marshal(in)
		_, err := e.HandoffCall(a.ID, token, "result_submit", raw)
		return err
	}
	if code := handoffCode(call(submission("ungated", valid))); code != "capability_not_granted" {
		t.Fatal("proposal without capability:", code)
	}
	grantProposals(t, e, r.ID, true)

	scope := proposedTask("scope")
	scope.Kind = "scope"
	dangling := proposedTask("dangling")
	dangling.Brief.Scheduling.Deps = []workpool.Dependency{{From: "elsewhere"}}
	many := []GraphSpec{}
	for i := range HandoffTasksPerProposal + 1 {
		many = append(many, proposedTask("t"+string(rune('a'+i))))
	}
	budget := []ResultProposal{}
	for i := range HandoffProposalsPerTask {
		budget = append(budget, ResultProposal{ID: "p" + string(rune('a'+i)), Rationale: "split", Tasks: many[:HandoffTasksPerProposal], Artifacts: []string{}})
	}
	tooMany := []ResultProposal{}
	for i := range HandoffProposalsPerTask + 1 {
		tooMany = append(tooMany, ResultProposal{ID: "q" + string(rune('a'+i)), Rationale: "split", Tasks: []GraphSpec{proposedTask("x")}, Artifacts: []string{}})
	}
	for name, tc := range map[string]struct {
		in   []ResultProposal
		code string
	}{
		"no rationale":     {[]ResultProposal{{ID: "a", Tasks: []GraphSpec{proposedTask("x")}}}, "invalid_proposal"},
		"no tasks":         {[]ResultProposal{{ID: "a", Rationale: "r"}}, "invalid_proposal"},
		"scope task":       {[]ResultProposal{{ID: "a", Rationale: "r", Tasks: []GraphSpec{scope}}}, "invalid_proposal"},
		"dangling dep":     {[]ResultProposal{{ID: "a", Rationale: "r", Tasks: []GraphSpec{dangling}}}, "invalid_proposal"},
		"too many tasks":   {[]ResultProposal{{ID: "a", Rationale: "r", Tasks: many}}, "invalid_proposal"},
		"duplicate ids":    {[]ResultProposal{valid, valid}, "invalid_proposal"},
		"unknown artifact": {[]ResultProposal{{ID: "a", Rationale: "r", Tasks: []GraphSpec{proposedTask("x")}, Artifacts: []string{"missing"}}}, "artifact_missing"},
		"over budget":      {budget, "proposal_over_budget"},
		"proposal cap":     {tooMany, "proposal_limit_exceeded"},
	} {
		if code := handoffCode(call(submission("bad-"+strings.ReplaceAll(name, " ", "-"), tc.in...))); code != tc.code {
			t.Errorf("%s: got %q, want %q", name, code, tc.code)
		}
	}
	// Unknown fields are refused, not ignored.
	raw := []byte(`{"version":"mitos.handoff/v1","key":"unknown","summary":"s","artifacts":[],"checks":[],"limitations":[],"unresolved_requests":[],"proposals":[{"id":"a","rationale":"r","blocking":true,"tasks":[],"artifacts":[],"priority":1}]}`)
	if _, err := e.HandoffCall(a.ID, token, "result_submit", raw); err == nil {
		t.Fatal("unknown proposal field accepted")
	}

	out := callHandoff(t, e, a, token, "result_submit", submission("ok", valid))
	ids, _ := out.(map[string]any)["proposal_ids"].([]string)
	if len(ids) != 1 || !strings.HasPrefix(ids[0], "proposal") {
		t.Fatal("receipt lacks proposal ids", out)
	}
}

func TestProposalSchemaFollowsGrant(t *testing.T) {
	schema := func(granted bool) map[string]any {
		for _, tool := range grantedHandoffTools(AuthorityReceipt{Capabilities: []CapabilityReceipt{{Name: ProposalCapability, Granted: granted}}}) {
			if tool.Name == "result_submit" {
				var out map[string]any
				_ = json.Unmarshal(tool.InputSchema, &out)
				return out
			}
		}
		t.Fatal("result_submit missing")
		return nil
	}
	has := func(s map[string]any) bool {
		props, _ := s["properties"].(map[string]any)
		_, prop := props["proposals"]
		// An advertised field is optional in the required list exactly when
		// the wire contract marks it omitempty; proposals is such a field.
		if required := slices.Contains(s["required"].([]any), any("proposals")); required {
			t.Fatal("proposals must stay an optional list")
		}
		return prop
	}
	if !has(schema(true)) || has(schema(false)) {
		t.Fatal("proposals advertised regardless of grant")
	}
}

// yieldWithProposals submits a result with the given proposals, finishes the
// attempt and returns the graph with the proposal IDs in order.
func yieldWithProposals(t *testing.T, proposals ...ResultProposal) (*Engine, WorkGraph, Run, []string) {
	t.Helper()
	e, r, a, token := handoffFixture(t)
	grantProposals(t, e, r.ID, true)
	out := callHandoff(t, e, a, token, "result_submit", submission("yield", proposals...))
	ids := out.(map[string]any)["proposal_ids"].([]string)
	if err := e.Store.Complete(r.ID, "yielded", false); err != nil {
		t.Fatal(err)
	}
	g, _ := e.Store.Graph(r.GraphAttempt.GraphID)
	return e, g, r, ids
}

func triggerFor(t *testing.T, g WorkGraph, run string) int64 {
	t.Helper()
	for _, tr := range g.Triggers {
		if tr.RunID == run {
			return tr.Seq
		}
	}
	t.Fatal("no trigger for", run)
	return 0
}

func finishProposedRun(t *testing.T, e *Engine, run string) {
	t.Helper()
	if err := e.Store.Transition(run, "fixture_started", "running", "Running", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Complete(run, "finished", false); err != nil {
		t.Fatal(err)
	}
}

func TestYieldReviewAndContinuation(t *testing.T) {
	e, g, r, ids := yieldWithProposals(t, ResultProposal{ID: "dig", Rationale: "Needs a separate investigation", Blocking: true, Tasks: []GraphSpec{proposedTask("dig")}, Artifacts: []string{}})
	m := g.Members[r.TaskID]
	rec := g.Proposals[ids[0]]
	if m.State != "yielded" || m.Yield == nil || m.Yield.RunID != r.ID || rec == nil || rec.Trigger != triggerFor(t, g, r.ID) || rec.Local != "dig" || !rec.Blocking {
		t.Fatalf("yield not journaled with its trigger: %+v %+v", m, rec)
	}
	page, err := e.Store.readResult(r.ID, ResultRead{RunID: r.ID, Limit: 16})
	if err != nil || page.State != "yielded" || len(page.Entries) != 1 || page.Entries[0].Proposal == nil || page.Entries[0].Proposal.ID != ids[0] || page.Entries[0].Proposal.Outcome != "pending" {
		t.Fatalf("result_get does not expose the proposal: %+v %v", page, err)
	}
	if view, _ := e.Store.GraphView(g.ID, r.TaskID); view.Nodes[r.TaskID].State != "blocked" {
		t.Fatal("yielded task is not shown as waiting", view.Nodes[r.TaskID].State)
	}

	// A yielded attempt is not a candidate: no acceptance, no retry, and its
	// outcome cannot be classified without reviewing the proposal.
	accept := proposal(g, "accept-yielded", GraphOperation{Kind: "accept", Task: r.TaskID, Evidence: "e", Rationale: "r"})
	accept.Reads = map[string]GraphRead{r.TaskID: {m.Revision, m.FactRevision}}
	if _, _, err = e.AmendGraph(accept); err == nil || !strings.Contains(err.Error(), "yielded") {
		t.Fatal("accepted a yielded task", err)
	}
	if _, _, err = e.AmendGraph(proposal(g, "retry-yielded", GraphOperation{Kind: "retry", Task: r.TaskID})); err == nil {
		t.Fatal("retried a yielded task")
	}
	seq := triggerFor(t, g, r.ID)
	if _, _, err = e.AmendGraph(proposal(g, "classify-only", GraphOperation{Kind: "classify_trigger", Trigger: seq, Rationale: "seen"})); err == nil || !strings.Contains(err.Error(), "proposal_review_required") {
		t.Fatal("classified a yield without reviewing its proposal", err)
	}
	if _, _, err = e.AmendGraph(proposal(g, "no-deps", GraphOperation{Kind: "accept_proposal", Proposal: ids[0], Rationale: "ok"}, GraphOperation{Kind: "classify_trigger", Trigger: seq, Rationale: "seen"})); err == nil {
		t.Fatal("accepted a blocking proposal without naming its work")
	}

	spec := proposedTask("dig")
	spec.Parent = g.RootTask
	g, receipt := amendGraphTest(t, e, g, "review",
		GraphOperation{Kind: "add", Spec: &spec},
		GraphOperation{Kind: "accept_proposal", Proposal: ids[0], Dependencies: []workpool.Dependency{{From: "dig", Guard: "done"}}, Rationale: "Worth a dedicated task"},
		GraphOperation{Kind: "classify_trigger", Trigger: seq, Rationale: "Proposal reviewed"})
	dig := receipt.IDs["dig"]
	m = g.Members[r.TaskID]
	if g.Proposals[ids[0]].Outcome != "accepted" || !slices.Equal(g.Proposals[ids[0]].Tasks, []string{dig}) || !slices.ContainsFunc(m.Spec.Brief.Scheduling.Deps, func(d workpool.Dependency) bool { return d.From == dig }) {
		t.Fatal("accepted proposal did not amend the yielded task's dependencies", g.Proposals[ids[0]], m.Spec.Brief.Scheduling.Deps)
	}
	if _, _, err = e.AmendGraph(proposal(g, "again", GraphOperation{Kind: "reject_proposal", Proposal: ids[0], Rationale: "changed mind"})); err == nil {
		t.Fatal("re-reviewed a proposal")
	}

	// The proposer stays parked without a slot until the accepted work settles.
	if err = e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	if g.Members[r.TaskID].CurrentRunID != r.ID || g.Members[r.TaskID].Yield == nil || g.Members[dig].CurrentRunID == "" {
		t.Fatal("continuation started before the proposed work settled")
	}
	finishProposedRun(t, e, g.Members[dig].CurrentRunID)
	g, _ = e.Store.Graph(g.ID)
	accept = proposal(g, "accept-dig", GraphOperation{Kind: "accept", Task: dig, Evidence: "report", Rationale: "Good"})
	accept.Reads = map[string]GraphRead{dig: {g.Members[dig].Revision, g.Members[dig].FactRevision}}
	if _, _, err = e.AmendGraph(accept); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	m = g.Members[r.TaskID]
	next, err := e.Store.Get(m.CurrentRunID)
	if err != nil || m.Yield != nil || m.Yields != 1 || len(m.Attempts) != 2 || next.GraphAttempt.ContinueFrom != r.ID || next.ParentID != r.ID {
		t.Fatalf("no continuation of the yielded attempt: %+v %v", m, err)
	}
	c := next.GraphAttempt.Continuation
	if c == nil || c.Reason != "yield" || len(c.Proposals) != 1 || c.Proposals[0].Outcome != "accepted" || !slices.Equal(c.Proposals[0].Completed, []string{dig}) {
		t.Fatalf("continuation lacks proposal outcomes: %+v", c)
	}
	if len(m.Attempts)-m.Yields > 1 {
		t.Fatal("yield consumed a retry")
	}
	if task, _ := e.Store.Task(r.TaskID); !e.Store.graphReadiness(&task).Ready {
		t.Fatal("continuation is not ready on accepted work")
	}
}

func TestRejectedAndAbandonedProposalsContinueWithRationale(t *testing.T) {
	e, g, r, ids := yieldWithProposals(t,
		ResultProposal{ID: "keep", Rationale: "Parallel check", Blocking: true, Tasks: []GraphSpec{proposedTask("check")}, Artifacts: []string{}},
		ResultProposal{ID: "drop", Rationale: "Rewrite the module", Blocking: true, Tasks: []GraphSpec{proposedTask("rewrite")}, Artifacts: []string{}},
		ResultProposal{ID: "later", Rationale: "Docs could improve", Tasks: []GraphSpec{proposedTask("docs")}, Artifacts: []string{}})
	if y := g.Members[r.TaskID].Yield; y == nil || len(y.Proposals) != 2 {
		t.Fatal("non-blocking proposal joined the yield", y)
	}
	spec := proposedTask("check")
	spec.Parent = g.RootTask
	seq := triggerFor(t, g, r.ID)
	// The non-blocking proposal may stay unreviewed; blocking ones may not.
	g, receipt := amendGraphTest(t, e, g, "review",
		GraphOperation{Kind: "add", Spec: &spec},
		GraphOperation{Kind: "accept_proposal", Proposal: ids[0], Dependencies: []workpool.Dependency{{From: "check", Guard: "done"}}, Rationale: "Fine"},
		GraphOperation{Kind: "reject_proposal", Proposal: ids[1], Rationale: "Out of scope for this goal"},
		GraphOperation{Kind: "classify_trigger", Trigger: seq, Rationale: "Reviewed"})
	check := receipt.IDs["check"]
	g, _ = amendGraphTest(t, e, g, "cancel-check", GraphOperation{Kind: "cancel", Task: check, Rationale: "No longer needed"})
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	m := g.Members[r.TaskID]
	next, err := e.Store.Get(m.CurrentRunID)
	if err != nil || next.GraphAttempt.ContinueFrom != r.ID {
		t.Fatalf("abandoned work did not release the continuation: %+v %v", m, err)
	}
	c := next.GraphAttempt.Continuation
	if len(c.Proposals) != 2 || !slices.Equal(c.Proposals[0].Abandoned, []string{check}) || c.Proposals[1].Outcome != "rejected" || c.Proposals[1].Rationale != "Out of scope for this goal" {
		t.Fatalf("continuation outcomes: %+v", c.Proposals)
	}
	if slices.ContainsFunc(m.Spec.Brief.Scheduling.Deps, func(d workpool.Dependency) bool { return d.From == check }) {
		t.Fatal("dependency on abandoned work kept")
	}
	if g.Proposals[ids[2]].Outcome != "" {
		t.Fatal("non-blocking proposal changed without review")
	}
}

// A yielded attempt is not an available candidate: downstream candidate_available
// consumers must wait for the blocking proposals to be reviewed and integrated,
// while the same dependency on an ordinary settled worker stays available.
func TestYieldedMemberIsNotAnAvailableCandidate(t *testing.T) {
	e, g, r, _ := yieldWithProposals(t, ResultProposal{ID: "dig", Rationale: "Needs a separate investigation", Blocking: true, Tasks: []GraphSpec{proposedTask("dig")}, Artifacts: []string{}})
	consumer := graphWorker("consumer", g.RootTask)
	consumer.Brief.Scheduling.Deps = []workpool.Dependency{{From: r.TaskID, Guard: "candidate_available"}}
	plain := graphWorker("plain", g.RootTask)
	plain.Acceptance = "operator"
	watcher := graphWorker("watcher", g.RootTask)
	watcher.Brief.Scheduling.Deps = []workpool.Dependency{{From: "plain", Guard: "candidate_available"}}
	g, receipt := amendGraphTest(t, e, g, "consumers", GraphOperation{Kind: "add", Spec: &consumer}, GraphOperation{Kind: "add", Spec: &plain}, GraphOperation{Kind: "add", Spec: &watcher})
	plainID := receipt.IDs["plain"]
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	plainRun := g.Members[plainID].CurrentRunID
	if plainRun == "" {
		t.Fatal("plain worker not started")
	}
	if err := e.Store.Transition(plainRun, "fixture_started", "running", "Running", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Complete(plainRun, "plain finished", false); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	if e.Store.graphMemberReadiness(&g, g.Members[receipt.IDs["consumer"]]).Ready {
		t.Fatal("yielded attempt exposed as an available candidate")
	}
	if !e.Store.graphMemberReadiness(&g, g.Members[receipt.IDs["watcher"]]).Ready {
		t.Fatal("settled non-yielded candidate unavailable")
	}
}

// Accepted proposal results must reach the continuation's dependency inputs even
// when the original any join already matched an older source: workpool keeps
// only the first matched quorum, which can exclude the just-accepted helper.
func TestContinuationJoinsIncludeAcceptedProposalResults(t *testing.T) {
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	early := graphWorker("early", g.RootTask)
	early.Acceptance = "operator"
	proposer := graphWorker("proposer", g.RootTask)
	// The builtin inventory app cannot consume artifact inputs, so the original
	// any-join edge is control-only; the accepted proposal below appends the
	// helper's data dependency the continuation must integrate.
	proposer.Brief.Scheduling = workpool.Request{Join: workpool.Join{Rule: "any"}, Deps: []workpool.Dependency{{From: "early", Guard: "candidate_available", Payload: "control"}}}
	g, receipt := amendGraphTest(t, e, g, "workers", GraphOperation{Kind: "add", Spec: &early}, GraphOperation{Kind: "add", Spec: &proposer}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "fixture"})
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	earlyRun := g.Members[receipt.IDs["early"]].CurrentRunID
	if earlyRun == "" {
		t.Fatal("early worker not started")
	}
	if err := e.Store.Transition(earlyRun, "fixture_started", "running", "Running", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Complete(earlyRun, "early finished", false); err != nil {
		t.Fatal(err)
	}
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	r, _ := e.Store.Get(g.Members[receipt.IDs["proposer"]].CurrentRunID)
	if r.ID == "" {
		t.Fatal("proposer not started on the early candidate")
	}
	if err := e.Store.Transition(r.ID, "fixture_started", "running", "Running", "fixture"); err != nil {
		t.Fatal(err)
	}
	a, token, err := e.Store.CreateBoundAgentSession("codex", "Handoff", e.Root, req.ProjectID, "", r.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	grantProposals(t, e, r.ID, true)
	callHandoff(t, e, a, token, "result_submit", submission("yield", ResultProposal{ID: "dig", Rationale: "Needs the helper", Blocking: true, Tasks: []GraphSpec{proposedTask("helper")}, Artifacts: []string{}}))
	if err = e.Store.Complete(r.ID, "yielded", false); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var proposalID string
	for id := range g.Proposals {
		proposalID = id
	}
	if proposalID == "" {
		t.Fatal("proposal not journaled")
	}
	helper := proposedTask("helper")
	helper.Parent = g.RootTask
	g, receipt = amendGraphTest(t, e, g, "review",
		GraphOperation{Kind: "add", Spec: &helper},
		GraphOperation{Kind: "accept_proposal", Proposal: proposalID, Dependencies: []workpool.Dependency{{From: "helper", Guard: "done", Payload: "data", Artifacts: []string{"report.md"}}}, Rationale: "A helper answers"})
	helperID := receipt.IDs["helper"]
	if err = e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	helperRun := g.Members[helperID].CurrentRunID
	if helperRun == "" {
		t.Fatal("helper not started")
	}
	if err = e.Store.Transition(helperRun, "fixture_started", "running", "Running", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.SaveArtifact(helperRun, "report.md", []byte("helper report")); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.Complete(helperRun, "helper finished", false); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	accept := proposal(g, "accept-helper", GraphOperation{Kind: "accept", Task: helperID, Evidence: "report", Rationale: "Helper answered"})
	accept.Reads = map[string]GraphRead{helperID: {g.Members[helperID].Revision, g.Members[helperID].FactRevision}}
	if _, _, err = e.AmendGraph(accept); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	m := g.Members[r.TaskID]
	next, err := e.Store.Get(m.CurrentRunID)
	if err != nil || m.Yield != nil || next.GraphAttempt.ContinueFrom != r.ID || next.GraphAttempt.Continuation == nil || next.GraphAttempt.Continuation.Reason != "yield" {
		t.Fatalf("continuation not readied after the accepted proposal settled: %+v %+v", m, next.GraphAttempt)
	}
	ready := e.Store.graphMemberReadiness(&g, m)
	if !ready.Ready {
		t.Fatal("continuation not ready")
	}
	var helperWitness *workpool.Witness
	for i := range ready.Matched {
		if ready.Matched[i].TaskID == helperID {
			helperWitness = &ready.Matched[i]
		}
	}
	if helperWitness == nil {
		t.Fatalf("accepted proposal helper missing from continuation witnesses: %+v", ready.Matched)
	}
	task, err := e.Store.Task(r.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := e.Store.bindDependencyInputs(&task, ready.Matched)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range bound {
		if w.TaskID == helperID && len(w.Artifacts) == 1 && w.Artifacts[0].Name == "report.md" {
			return
		}
	}
	t.Fatalf("helper evidence not bound for the continuation: %+v", bound)
}

func TestYieldLimit(t *testing.T) {
	e, r, a, token := handoffFixture(t)
	grantProposals(t, e, r.ID, true)
	e.Store.mu.Lock()
	g := graphClone(*e.Store.graphs[r.GraphAttempt.GraphID])
	g.Members[r.TaskID].Yields = HandoffYieldsPerTask
	err := e.Store.commit("", "effect", "fixture_yields_used", change{Graph: &graphChange{Graph: g}})
	e.Store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(submission("yield", ResultProposal{ID: "dig", Rationale: "r", Blocking: true, Tasks: []GraphSpec{proposedTask("dig")}, Artifacts: []string{}}))
	if _, err = e.HandoffCall(a.ID, token, "result_submit", raw); handoffCode(err) != "yield_limit_exceeded" {
		t.Fatal("yield cap not enforced", err)
	}
	// Follow-up proposals remain available after the yield cap.
	callHandoff(t, e, a, token, "result_submit", submission("follow-up", ResultProposal{ID: "dig", Rationale: "r", Tasks: []GraphSpec{proposedTask("dig")}, Artifacts: []string{}}))
}

// The advertised result_submit schema must not require evidence the wire
// contract accepts as absent: a proposal without artifact IDs, or a result
// without checks, is valid and must not bounce at client-side validation.
func TestHandoffSchemaKeepsOptionalEvidenceOptional(t *testing.T) {
	for _, tool := range grantedHandoffTools(AuthorityReceipt{Capabilities: []CapabilityReceipt{{Name: ProposalCapability, Granted: true}}}) {
		if tool.Name != "result_submit" {
			continue
		}
		var schema struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Items struct {
					Required   []string `json:"required"`
					Properties map[string]struct {
						Items struct {
							Required []string `json:"required"`
						} `json:"items"`
					} `json:"properties"`
				} `json:"items"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
			t.Fatal(err)
		}
		for _, optional := range []string{"artifacts", "checks", "limitations", "unresolved_requests", "proposals"} {
			if _, advertised := schema.Properties[optional]; !advertised {
				t.Fatalf("result_submit no longer advertises %s", optional)
			}
			if slices.Contains(schema.Required, optional) {
				t.Fatalf("result_submit requires optional list %s", optional)
			}
		}
		proposal := schema.Properties["proposals"].Items
		if slices.Contains(proposal.Required, "artifacts") {
			t.Fatal("proposal evidence must stay optional")
		}
		check := schema.Properties["checks"].Items
		if slices.Contains(check.Properties["evidence"].Items.Required, "evidence") {
			t.Fatal("check evidence must stay optional")
		}
		if !slices.Contains(check.Required, "name") || !slices.Contains(proposal.Required, "tasks") {
			t.Fatal("semantically required fields must stay required")
		}
		return
	}
	t.Fatal("result_submit missing")
}

// A submission that omits every optional list is accepted and journaled.
func TestResultSubmissionWithoutOptionalLists(t *testing.T) {
	e, r, a, token := handoffFixture(t)
	grantProposals(t, e, r.ID, true)
	raw := json.RawMessage(`{"version":"` + HandoffVersion + `","key":"bare","summary":"Findings only"}`)
	out, err := e.HandoffCall(a.ID, token, "result_submit", raw)
	if err != nil {
		t.Fatal(err)
	}
	if id, _ := out.(map[string]any)["id"].(string); !strings.HasPrefix(id, "result_") {
		t.Fatal("receipt missing", out)
	}
	stored, err := e.Store.Get(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Result == nil || stored.Result.Submission.Summary != "Findings only" {
		t.Fatal("bare submission not stored")
	}
}
