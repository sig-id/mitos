package studio

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

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
		_, prop := s["properties"].(map[string]any)["proposals"]
		required := slices.Contains(s["required"].([]any), any("proposals"))
		if prop != required {
			t.Fatal("schema property and required list disagree")
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
