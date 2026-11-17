package studio

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"mitos/agents"
	"mitos/internal/compute"
)

func libraryAgent(t *testing.T, id string) agents.Definition {
	t.Helper()
	library, err := agents.Library()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range library {
		if d.ID == id {
			return d
		}
	}
	t.Fatal("missing", id)
	return agents.Definition{}
}

// Harness choice comes from role and nature among eligible harnesses; the
// model tier comes from intelligence within the chosen harness.
func TestHarnessAndTierSelection(t *testing.T) {
	p := mixedExecution()
	p.Harnesses[0].Tiers = map[string]ModelTier{"low": {Model: "codex-mini", Effort: "low"}, "medium": {Model: "codex-std", Effort: "medium"}, "high": {Model: "codex-max", Effort: "xhigh"}}
	p.HarnessRules = []HarnessRule{
		{Match: HarnessMatch{Role: "mitos.architect"}, Harness: "design"},
		{Match: HarnessMatch{Nature: "design"}, Harness: "design"},
		{Match: HarnessMatch{Role: "mitos.architect", Nature: "review"}, Harness: "code"},
	}
	if err := p.validate(); err != nil {
		t.Fatal(err)
	}
	engineer, architect, generalist := libraryAgent(t, "mitos.engineer"), libraryAgent(t, "mitos.architect"), libraryAgent(t, "mitos.generalist")
	for _, tc := range []struct {
		d              agents.Definition
		nature, want   string
		source, policy string
	}{
		{engineer, "implement", "code", "default harness", EnforcementAdvisory},
		{architect, "design", "design", "rule role=mitos.architect", EnforcementAdvisory}, // tie resolved by validation: same harness
		{architect, "review", "code", "rule role=mitos.architect nature=review", EnforcementAdvisory},
		{generalist, "design", "design", "rule nature=design", EnforcementAdvisory},
	} {
		h, source, err := selectHarness(p, tc.d, tc.nature, "", tc.policy)
		if err != nil || h.ID != tc.want || !strings.HasPrefix(source, strings.Split(tc.source, " ")[0]) {
			t.Errorf("%s %s %s: got %s (%s) %v, want %s", tc.d.ID, tc.nature, tc.policy, h.ID, source, err, tc.want)
		}
	}
	// Hard constraints filter before rules: a definition that requires
	// enforced writes cannot run on Claude (advisory locally) under
	// strict_required, so its design work falls back to Codex.
	writer := generalist
	writer.Manifest.Capabilities.Required = []string{"workspace.write"}
	writer.Manifest.Capabilities.Optional = []string{}
	if h, source, err := selectHarness(p, writer, "design", "", EnforcementStrictRequired); err != nil || h.ID != "code" || source != "default harness" {
		t.Fatal("ineligible harness not filtered", h.ID, source, err)
	}
	if _, _, err := selectHarness(p, writer, "design", "design", EnforcementStrictRequired); err == nil {
		t.Fatal("override onto an ineligible harness accepted")
	}
	p.DefaultHarness, p.AllowedHarnesses = "design", []string{"design"}
	p.HarnessRules = nil
	if _, _, err := selectHarness(p, writer, "design", "", EnforcementStrictRequired); err == nil || !strings.Contains(err.Error(), "no permitted harness") {
		t.Fatal("no eligible harness was not refused", err)
	}

	// Size and intelligence are independent axes.
	p.ContextFloor = map[string]string{"large": "medium"}
	for _, tc := range []struct{ size, intelligence, tier string }{{"large", "low", "medium"}, {"small", "high", "high"}, {"small", "low", "low"}, {"large", "high", "high"}} {
		if tier, _ := selectTier(p, TaskTraits{Size: tc.size, Intelligence: tc.intelligence}); tier != tc.tier {
			t.Errorf("%s/%s: tier %s, want %s", tc.size, tc.intelligence, tier, tc.tier)
		}
	}
}

func TestAmbiguousHarnessRulesAreRejected(t *testing.T) {
	p := mixedExecution()
	p.HarnessRules = []HarnessRule{{Match: HarnessMatch{Role: "mitos.architect"}, Harness: "design"}, {Match: HarnessMatch{Nature: "review"}, Harness: "code"}}
	if err := p.validate(); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatal("overlapping equal-specificity rules accepted", err)
	}
	p.HarnessRules = append(p.HarnessRules, HarnessRule{Match: HarnessMatch{Role: "mitos.architect", Nature: "review"}, Harness: "design"})
	if err := p.validate(); err != nil {
		t.Fatal("covered overlap rejected", err)
	}
	p.HarnessRules = append(p.HarnessRules, HarnessRule{Match: HarnessMatch{Role: "mitos.architect", Nature: "review"}, Harness: "code"})
	if err := p.validate(); err == nil {
		t.Fatal("duplicate rule accepted")
	}
}

func TestNatureNarrowsAuthority(t *testing.T) {
	engineer := libraryAgent(t, "mitos.engineer")
	r, err := resolveAuthority(engineer, "codex", EnforcementAdvisory, 0, "context", (&compute.Local{}).Capabilities())
	if err != nil {
		t.Fatal(err)
	}
	if r.Nature != "context" || r.Granted("workspace.write") || !r.Granted("workspace.read") {
		t.Fatalf("context work kept write access: %+v", r)
	}
	for _, c := range r.Capabilities {
		if c.Name == "workspace.write" && (c.Status != CapabilityEnforced || c.Required || !strings.Contains(c.Note, "context")) {
			t.Fatal("withheld capability not recorded as enforced by nature", c)
		}
	}
	if r, _ = resolveAuthority(engineer, "codex", EnforcementAdvisory, 0, "implement", (&compute.Local{}).Capabilities()); !r.Granted("workspace.write") {
		t.Fatal("nature removed a grant it does not withhold")
	}
	// Claude cannot block writes, so strict policy refuses withheld-by-nature work there.
	if _, err = resolveAuthority(engineer, "claude", EnforcementStrict, 0, "context", (&compute.Local{}).Capabilities()); err == nil {
		t.Fatal("strict policy accepted an unenforceable withholding")
	}
}

func TestTraitAdmissionAndSizeBudget(t *testing.T) {
	executionFixture(t)
	e, req := graphEngine(t, false)
	configureExecutionRegistry(t, e)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "traits", ProjectID: req.ProjectID, Goal: "Check trait admission", Execution: mixedExecution()})
	if err != nil {
		t.Fatal(err)
	}
	spec := graphWorker("w", g.RootTask)
	spec.Brief.AppID = "agent:mitos.engineer"
	spec.Brief.TimeoutMinutes = 99
	spec.Traits = &TaskTraits{Size: "large", Intelligence: "high"}
	out, executor, err := e.resolveGraphSpec(&g, spec)
	if err != nil {
		t.Fatal(err)
	}
	sel := executor.Application.Agent.Selection
	if out.Traits.Nature != "implement" || out.Brief.TimeoutMinutes != 60 || sel.Budget.MaxTurns != 160 || sel.Tier != "high" || executor.Application.Agent.Effort != "high" {
		t.Fatalf("traits not applied: %+v %+v", out.Traits, sel)
	}
	for name, mutate := range map[string]func(*GraphSpec){
		"missing traits":     func(s *GraphSpec) { s.Traits = nil },
		"unknown size":       func(s *GraphSpec) { s.Traits = &TaskTraits{Size: "huge", Intelligence: "low"} },
		"unsupported nature": func(s *GraphSpec) { s.Traits = &TaskTraits{Nature: "design", Size: "small", Intelligence: "low"} },
		"traits on app":      func(s *GraphSpec) { s.Brief.AppID = "workspace-inventory" },
	} {
		bad := spec
		mutate(&bad)
		if _, _, err = e.resolveGraphSpec(&g, bad); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestRetryEscalationStaysOnHarnessAndResumes(t *testing.T) {
	executionFixture(t)
	e, req := graphEngine(t, false)
	configureExecutionRegistry(t, e)
	policy := mixedExecution()
	policy.Harnesses[0].Tiers = map[string]ModelTier{"low": {Model: "codex-mini", Effort: "low"}, "medium": {Model: "codex-std", Effort: "medium"}, "high": {Model: "codex-max", Effort: "high"}}
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "escalate", ProjectID: req.ProjectID, Goal: "Escalate a retry", Execution: policy})
	if err != nil {
		t.Fatal(err)
	}
	spec := graphWorker("w", g.RootTask)
	spec.Brief.AppID = "agent:mitos.engineer"
	spec.Traits = &TaskTraits{Size: "small", Intelligence: "medium"}
	g, receipt := amendGraphTest(t, e, g, "add", GraphOperation{Kind: "add", Spec: &spec})
	id := receipt.IDs["w"]
	if err = e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	first := g.Members[id].CurrentRunID
	failRun(t, e, first)
	g, _ = e.Store.Graph(g.ID)
	before := *g.Members[id].Executor
	if g.Members[id].Requester != "operator" {
		t.Fatal("requester not recorded", g.Members[id].Requester)
	}
	if _, _, err = e.AmendGraph(proposal(g, "bad-axis", GraphOperation{Kind: "retry", Task: id, Escalate: "harness"})); err == nil {
		t.Fatal("unknown escalation axis accepted")
	}
	g, _ = amendGraphTest(t, e, g, "escalate", GraphOperation{Kind: "retry", Task: id, Escalate: "intelligence"})
	m := g.Members[id]
	b := m.Executor.Application.Agent
	if b.Model != "codex-max" || b.Selection.Tier != "high" || b.Selection.Harness != "code" || !strings.HasPrefix(b.Selection.TierSource, "escalated") || m.Spec.Traits.Intelligence != "high" {
		t.Fatalf("intelligence escalation: %+v", b.Selection)
	}
	if m.ContinueFrom != first || m.Continuation == nil || m.Continuation.Escalation == nil || len(m.Escalations) != 1 || m.Escalations[0].From != "medium" || m.Escalations[0].To != "high" {
		t.Fatalf("escalation not recorded for resume: %+v", m)
	}
	if executorContinuity(before) != executorContinuity(*m.Executor) || before.Digest == m.Executor.Digest {
		t.Fatal("tier change should keep conversation continuity but change the executor")
	}
	if err = e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	next, _ := e.Store.Get(g.Members[id].CurrentRunID)
	if next.GraphAttempt.ContinueFrom != first || next.GraphAttempt.Continuation.Escalation.Axis != "intelligence" {
		t.Fatal("escalated retry does not continue the attempt", next.GraphAttempt)
	}
	failRun(t, e, next.ID)
	g, _ = e.Store.Graph(g.ID)
	if _, _, err = e.AmendGraph(proposal(g, "too-high", GraphOperation{Kind: "retry", Task: id, Escalate: "intelligence"})); err == nil {
		t.Fatal("escalated past the highest tier")
	}
	g, _ = amendGraphTest(t, e, g, "bigger", GraphOperation{Kind: "retry", Task: id, Escalate: "size"})
	if m = g.Members[id]; m.Spec.Traits.Size != "medium" || m.Spec.Brief.TimeoutMinutes != 30 || m.Executor.Application.Agent.Selection.Budget.MaxTurns != 80 {
		t.Fatalf("size escalation: %+v", m.Executor.Application.Agent.Selection)
	}

	// A context-only harness change is never an escalation; continuity breaks.
	other := before
	other.Application.Agent = graphClone(before.Application.Agent)
	other.Application.Agent.Runtime = "claude"
	if executorContinuity(other) == executorContinuity(before) {
		t.Fatal("harness change kept conversation continuity")
	}
}

func failRun(t *testing.T, e *Engine, run string) {
	t.Helper()
	if err := e.Store.Transition(run, "fixture_started", "running", "Running", "fixture"); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.Transition(run, "run_failed", "failed", "Needs a new attempt", "Checks failed."); err != nil {
		t.Fatal(err)
	}
}

func TestProposalTraitsAreCheckedAtSubmission(t *testing.T) {
	e, r, a, token := handoffFixture(t)
	grantProposals(t, e, r.ID, true)
	call := func(spec GraphSpec, blocking bool) string {
		raw, _ := json.Marshal(submission("p-"+spec.ID, ResultProposal{ID: "p", Rationale: "r", Blocking: blocking, Tasks: []GraphSpec{spec}, Artifacts: []string{}}))
		_, err := e.HandoffCall(a.ID, token, "result_submit", raw)
		return handoffCode(err)
	}
	native := proposedTask("native")
	native.Brief.AppID = "agent:mitos.writer"
	if code := call(native, false); code != "invalid_proposal_traits" {
		t.Fatal("native proposal without traits:", code)
	}
	native.Traits = &TaskTraits{Nature: "implement", Size: "small", Intelligence: "low"}
	if code := call(native, false); code != "invalid_proposal_traits" {
		t.Fatal("writer proposed for implementation:", code)
	}
	native.Traits.Nature = ""
	native.Harness = "nowhere"
	if code := call(native, false); code != "proposal_unroutable" {
		t.Fatal("unpermitted harness:", code)
	}
	inventory := proposedTask("inventory")
	inventory.Traits = &TaskTraits{Size: "small", Intelligence: "low"}
	if code := call(inventory, false); code != "invalid_proposal" {
		t.Fatal("traits on a registered application:", code)
	}
}

func TestContextWorkCannotYield(t *testing.T) {
	e, r, a, token := handoffFixture(t)
	grantProposals(t, e, r.ID, true)
	e.Store.mu.Lock()
	g := graphClone(*e.Store.graphs[r.GraphAttempt.GraphID])
	g.Members[r.TaskID].Spec.Traits = &TaskTraits{Nature: "context", Size: "small", Intelligence: "low"}
	err := e.Store.commit("", "effect", "fixture_context_nature", change{Graph: &graphChange{Graph: g}})
	e.Store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(submission("yield", ResultProposal{ID: "dig", Rationale: "r", Blocking: true, Tasks: []GraphSpec{proposedTask("dig")}, Artifacts: []string{}}))
	if _, err = e.HandoffCall(a.ID, token, "result_submit", raw); handoffCode(err) != "proposal_blocking_not_permitted" {
		t.Fatal("context work yielded", err)
	}
	callHandoff(t, e, a, token, "result_submit", submission("follow-up", ResultProposal{ID: "dig", Rationale: "r", Tasks: []GraphSpec{proposedTask("dig")}, Artifacts: []string{}}))
}

func TestV1ExecutionPolicyIsRefused(t *testing.T) {
	raw := []byte(`{"presets":[{"id":"default","name":"Default","runtime":"codex"}],"orchestrator":"default","worker":"default","roles":{},"allowed_workers":["default"]}`)
	var p AgentExecutionPolicy
	if err := DecodeGraphJSON(raw, &p); err == nil {
		if err = p.validate(); err == nil {
			t.Fatal("v1 policy accepted")
		}
	}
	document := "```mitos\n{\"execution\":" + string(raw) + "}\n```\n"
	if _, err := documentPolicy(document); err == nil {
		t.Fatal("v1 policy in project instructions accepted")
	}
	if err := defaultExecution("claude", "m").validate(); err != nil {
		t.Fatal(err)
	}
	var _ = errors.New
}
