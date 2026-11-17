package studio

import (
	"fmt"
	"slices"
	"strings"

	"mitos/agents"
	"mitos/internal/agentcli"
)

// ExecutionPolicyVersion is the only accepted execution policy contract.
// Role-only preset policies (v1) are refused, not translated.
const ExecutionPolicyVersion = "agent-execution/v2"

// Task trait buckets. Coarse buckets are the honest precision of an estimate.
var (
	TaskSizes         = []string{"small", "medium", "large"}
	TaskIntelligences = []string{"low", "medium", "high"}
)

// TaskTraits are declared by whoever requests a task: the orchestrator in an
// add operation, or a worker in a proposal. They are requirements on the
// executor, never a choice of harness or model; the execution policy maps them.
//
//   - nature: what kind of work (defaults from the role's definition)
//   - size: volume of work; sets the attempt budget
//   - intelligence: reasoning capability required; selects the model tier
type TaskTraits struct {
	Nature       string `json:"nature"`
	Size         string `json:"size"`
	Intelligence string `json:"intelligence"`
}

// ModelTier is a model and reasoning effort within one harness.
type ModelTier struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// ExecutionHarness is one native harness (Codex CLI or Claude Code) with its
// environment profile and a model tier per intelligence bucket.
type ExecutionHarness struct {
	ID        string               `json:"id"`
	Name      string               `json:"name"`
	Runtime   string               `json:"runtime"`
	ProfileID string               `json:"profile_id,omitempty"`
	Tiers     map[string]ModelTier `json:"tiers"`
}

// HarnessMatch constrains role and/or nature. Empty fields match anything.
type HarnessMatch struct {
	Role   string `json:"role,omitempty"`
	Nature string `json:"nature,omitempty"`
}
type HarnessRule struct {
	Match   HarnessMatch `json:"match"`
	Harness string       `json:"harness"`
}
type OrchestratorExecution struct {
	Harness string `json:"harness"`
	Tier    string `json:"tier"`
}

// SizeBudget is the attempt budget for a size bucket, capped by the agent
// definition's limits and the graph's allowances.
type SizeBudget struct {
	Minutes  int `json:"minutes"`
	MaxTurns int `json:"max_turns"`
}

// AgentExecutionPolicy separates the harness decision (driven by role and
// nature, filtered by hard capability constraints) from the model decision
// (driven by intelligence required, with a context floor from size).
type AgentExecutionPolicy struct {
	Version          string                `json:"version"`
	Harnesses        []ExecutionHarness    `json:"harnesses"`
	HarnessRules     []HarnessRule         `json:"harness_rules"`
	DefaultHarness   string                `json:"default_harness"`
	AllowedHarnesses []string              `json:"allowed_harnesses"`
	Orchestrator     OrchestratorExecution `json:"orchestrator"`
	SizeBudgets      map[string]SizeBudget `json:"size_budgets"`
	// ContextFloor optionally raises the tier for a size bucket (for a larger
	// context window). It never lowers the tier intelligence selects.
	ContextFloor map[string]string `json:"context_floor,omitempty"`
}

// ExecutionSelection records each execution decision and what made it.
type ExecutionSelection struct {
	Harness       string     `json:"harness"`
	HarnessName   string     `json:"harness_name"`
	HarnessSource string     `json:"harness_source"`
	Tier          string     `json:"tier"`
	TierSource    string     `json:"tier_source"`
	Traits        TaskTraits `json:"traits"`
	Budget        SizeBudget `json:"budget"`
}

var runtimeEfforts = map[string][]string{
	"codex":  {"minimal", "low", "medium", "high", "xhigh"},
	"claude": {"low", "medium", "high", "xhigh", "max"},
}

func defaultSizeBudgets() map[string]SizeBudget {
	return map[string]SizeBudget{"small": {Minutes: 15, MaxTurns: 40}, "medium": {Minutes: 30, MaxTurns: 80}, "large": {Minutes: 60, MaxTurns: 160}}
}

// defaultExecution is the policy for a project without one: a single harness
// whose tiers share the configured model and differ in reasoning effort.
func defaultExecution(runtime, model string) *AgentExecutionPolicy {
	if runtime == "" {
		runtime = "codex"
	}
	tiers := map[string]ModelTier{}
	for _, t := range TaskIntelligences {
		tiers[t] = ModelTier{Model: model, Effort: t}
	}
	return &AgentExecutionPolicy{Version: ExecutionPolicyVersion, Harnesses: []ExecutionHarness{{ID: "default", Name: "Project default", Runtime: runtime, Tiers: tiers}}, HarnessRules: []HarnessRule{}, DefaultHarness: "default", AllowedHarnesses: []string{"default"}, Orchestrator: OrchestratorExecution{Harness: "default", Tier: "high"}, SizeBudgets: defaultSizeBudgets()}
}

func (p *AgentExecutionPolicy) harness(id string) (ExecutionHarness, bool) {
	for _, h := range p.Harnesses {
		if h.ID == id {
			return h, true
		}
	}
	return ExecutionHarness{}, false
}

func (m HarnessMatch) specificity() int {
	n := 0
	if m.Role != "" {
		n++
	}
	if m.Nature != "" {
		n++
	}
	return n
}
func (m HarnessMatch) matches(role, nature string) bool {
	return (m.Role == "" || m.Role == role) && (m.Nature == "" || m.Nature == nature)
}

// intersect returns the narrowest match covering both, if they can overlap.
func (m HarnessMatch) intersect(o HarnessMatch) (HarnessMatch, bool) {
	out := m
	for _, pair := range [][3]*string{{&m.Role, &o.Role, &out.Role}, {&m.Nature, &o.Nature, &out.Nature}} {
		a, b := *pair[0], *pair[1]
		switch {
		case a == b, b == "":
		case a == "":
			*pair[2] = b
		default:
			return out, false
		}
	}
	return out, true
}

func (p *AgentExecutionPolicy) validate() error {
	if p == nil {
		return nil
	}
	if p.Version != ExecutionPolicyVersion {
		return graphError("execution policy must be %s; role-only preset policies are no longer accepted, so redefine harnesses, tiers and rules", ExecutionPolicyVersion)
	}
	if len(p.Harnesses) < 1 || len(p.Harnesses) > 16 || len(p.HarnessRules) > 128 || len(p.AllowedHarnesses) < 1 || len(p.AllowedHarnesses) > 16 {
		return graphError("provide 1–16 harnesses, 1–16 permitted worker harnesses and at most 128 rules")
	}
	ids := map[string]bool{}
	for _, h := range p.Harnesses {
		if !validGraphKey(h.ID) || ids[h.ID] || len(strings.TrimSpace(h.Name)) < 1 || len(h.Name) > 100 {
			return graphError("harnesses need unique IDs and names")
		}
		ids[h.ID] = true
		efforts, ok := runtimeEfforts[h.Runtime]
		if !ok {
			return graphError("coordinated work supports Codex and Claude Code; %s has no coordinated adapter", h.Runtime)
		}
		if len(h.ProfileID) > 128 || strings.ContainsAny(h.ProfileID, "\x00\r\n") {
			return graphError("invalid environment profile in harness %s", h.ID)
		}
		if len(h.Tiers) != len(TaskIntelligences) {
			return graphError("harness %s needs exactly the tiers %s", h.ID, strings.Join(TaskIntelligences, ", "))
		}
		for _, name := range TaskIntelligences {
			t, ok := h.Tiers[name]
			if !ok {
				return graphError("harness %s needs exactly the tiers %s", h.ID, strings.Join(TaskIntelligences, ", "))
			}
			if len(t.Model) > 200 || strings.ContainsAny(t.Model, "\x00\r\n\"") || strings.HasPrefix(t.Model, "-") {
				return graphError("invalid model in harness %s tier %s", h.ID, name)
			}
			if t.Effort != "" && !slices.Contains(efforts, t.Effort) {
				return graphError("harness %s tier %s: %s cannot honour effort %q (supported: %s)", h.ID, name, h.Runtime, t.Effort, strings.Join(efforts, ", "))
			}
		}
	}
	for i, id := range p.AllowedHarnesses {
		if !ids[id] || slices.Contains(p.AllowedHarnesses[:i], id) {
			return graphError("unknown or duplicate permitted worker harness %s", id)
		}
	}
	if !slices.Contains(p.AllowedHarnesses, p.DefaultHarness) {
		return graphError("the default worker harness must be permitted for workers")
	}
	if !ids[p.Orchestrator.Harness] || !slices.Contains(TaskIntelligences, p.Orchestrator.Tier) {
		return graphError("select an orchestrator harness and tier")
	}
	if len(p.SizeBudgets) != len(TaskSizes) {
		return graphError("provide a budget for each size: %s", strings.Join(TaskSizes, ", "))
	}
	prev := SizeBudget{}
	for _, size := range TaskSizes {
		b, ok := p.SizeBudgets[size]
		if !ok || b.Minutes < 1 || b.Minutes > 120 || b.MaxTurns < 1 || b.MaxTurns > 1000 {
			return graphError("size %s needs a budget of 1–120 minutes and 1–1000 turns", size)
		}
		if b.Minutes < prev.Minutes || b.MaxTurns < prev.MaxTurns {
			return graphError("size budgets must not shrink as size grows")
		}
		prev = b
	}
	for size, tier := range p.ContextFloor {
		if !slices.Contains(TaskSizes, size) || !slices.Contains(TaskIntelligences, tier) {
			return graphError("context floors map a size to a tier")
		}
	}
	seen := map[HarnessMatch]bool{}
	for _, r := range p.HarnessRules {
		if r.Match.specificity() == 0 || !validGraphKey(r.Match.Role) && r.Match.Role != "" || r.Match.Nature != "" && !slices.Contains(agents.Natures, r.Match.Nature) {
			return graphError("harness rules must match a role and/or a known nature")
		}
		if seen[r.Match] {
			return graphError("duplicate harness rule for %+v", r.Match)
		}
		seen[r.Match] = true
		if !slices.Contains(p.AllowedHarnesses, r.Harness) {
			return graphError("harness rule %+v must select a permitted worker harness", r.Match)
		}
	}
	// Equal-specificity rules that can match the same task are ambiguous unless
	// a more specific rule covers exactly their overlap. Rule order never decides.
	for i, a := range p.HarnessRules {
		for _, b := range p.HarnessRules[i+1:] {
			if a.Match.specificity() != b.Match.specificity() || a.Harness == b.Harness {
				continue
			}
			if overlap, ok := a.Match.intersect(b.Match); ok && !seen[overlap] {
				return graphError("harness rules %+v and %+v are ambiguous; add a rule for %+v", a.Match, b.Match, overlap)
			}
		}
	}
	return nil
}

func (e *Engine) validateExecution(p *AgentExecutionPolicy) error {
	if p == nil {
		return graphError("execution policy is required")
	}
	if err := p.validate(); err != nil {
		return err
	}
	if e.Desk == nil || e.Desk.Agents == nil {
		return graphError("coding agent discovery is unavailable")
	}
	library, err := e.AgentLibrary()
	if err != nil {
		return err
	}
	for _, r := range p.HarnessRules {
		if r.Match.Role == "" {
			continue
		}
		found := false
		for _, d := range library {
			if d.ID == r.Match.Role && !isOrchestratorKind(d.Kind) {
				found = true
				if r.Match.Nature != "" && !d.Manifest.Natures.Supports(r.Match.Nature) {
					return graphError("role %s never takes on nature %s", r.Match.Role, r.Match.Nature)
				}
			}
		}
		if !found {
			return graphError("unknown worker role %s", r.Match.Role)
		}
	}
	for _, h := range p.Harnesses {
		if _, err := e.Desk.Agents.Prepare(agentcli.Request{AgentID: h.Runtime, ProfileID: h.ProfileID}); err != nil {
			return graphError("harness %s: %v", h.Name, err)
		}
	}
	return nil
}

// resolveTraits checks a task's declared traits against its role and fills the
// default nature. Native agent tasks must state size and intelligence.
func resolveTraits(d agents.Definition, in *TaskTraits) (TaskTraits, error) {
	if in == nil {
		return TaskTraits{}, graphError("native agent tasks must declare traits: size (%s) and intelligence (%s); nature defaults from the role", strings.Join(TaskSizes, "|"), strings.Join(TaskIntelligences, "|"))
	}
	t := *in
	if !slices.Contains(TaskSizes, t.Size) || !slices.Contains(TaskIntelligences, t.Intelligence) {
		return t, graphError("traits need size (%s) and intelligence (%s)", strings.Join(TaskSizes, "|"), strings.Join(TaskIntelligences, "|"))
	}
	if t.Nature == "" {
		t.Nature = d.Manifest.Natures.Default
	}
	if !slices.Contains(agents.Natures, t.Nature) {
		return t, graphError("unknown nature %q", t.Nature)
	}
	if !d.Manifest.Natures.Supports(t.Nature) {
		return t, graphError("role %s does not take on %s work (supported: %s)", d.ID, t.Nature, strings.Join(d.Manifest.Natures.Supported, ", "))
	}
	return t, nil
}

func graphEnforcement(g *WorkGraph) string {
	if g.Setup != nil && g.Setup.Enforcement != "" {
		return g.Setup.Enforcement
	}
	return EnforcementAdvisory
}

// selectHarness filters harnesses by hard constraints (the definition and
// nature must be admissible on the runtime under the enforcement policy), then
// applies the most specific matching rule. It never downgrades: if nothing
// eligible matches, the task is refused with the reason.
func selectHarness(p *AgentExecutionPolicy, d agents.Definition, nature, override, enforcement string) (ExecutionHarness, string, error) {
	eligible := func(h ExecutionHarness) error {
		_, err := resolveAuthority(d, h.Runtime, enforcement, 0, nature)
		return err
	}
	if override != "" {
		h, ok := p.harness(override)
		if !ok || !slices.Contains(p.AllowedHarnesses, override) {
			return h, "", graphError("task harness %s is not a permitted worker harness", override)
		}
		if err := eligible(h); err != nil {
			return h, "", graphError("task harness %s cannot run this task: %v", override, err)
		}
		return h, "task override", nil
	}
	best, bestSpec, tied := HarnessRule{}, 0, false
	for _, r := range p.HarnessRules {
		if !r.Match.matches(d.ID, nature) {
			continue
		}
		h, _ := p.harness(r.Harness)
		if eligible(h) != nil {
			continue
		}
		switch s := r.Match.specificity(); {
		case s > bestSpec:
			best, bestSpec, tied = r, s, false
		case s == bestSpec && r.Harness != best.Harness:
			tied = true
		}
	}
	if tied {
		return ExecutionHarness{}, "", graphError("harness rules are ambiguous for role %s and nature %s once ineligible harnesses are excluded", d.ID, nature)
	}
	if bestSpec > 0 {
		h, _ := p.harness(best.Harness)
		source := "rule"
		if best.Match.Role != "" {
			source += " role=" + best.Match.Role
		}
		if best.Match.Nature != "" {
			source += " nature=" + best.Match.Nature
		}
		return h, source, nil
	}
	h, _ := p.harness(p.DefaultHarness)
	if err := eligible(h); err != nil {
		return h, "", graphError("no permitted harness can run %s %s work under the %s policy: %v", d.ID, nature, enforcement, err)
	}
	return h, "default harness", nil
}

// selectTier picks the model tier within a harness from intelligence required,
// raised (never lowered) to the size's context floor.
func selectTier(p *AgentExecutionPolicy, t TaskTraits) (string, string) {
	tier, source := t.Intelligence, "intelligence "+t.Intelligence
	if floor := p.ContextFloor[t.Size]; floor != "" && slices.Index(TaskIntelligences, floor) > slices.Index(TaskIntelligences, tier) {
		tier, source = floor, "context floor for "+t.Size+" size"
	}
	return tier, source
}

func (e *Engine) bindExecution(g *WorkGraph, spec GraphSpec, b *AgentBinding) error {
	p := graphExecution(g)
	if err := p.validate(); err != nil {
		return err
	}
	var sel ExecutionSelection
	var h ExecutionHarness
	if isOrchestratorKind(b.Definition.Kind) {
		if spec.Harness != "" || spec.Traits != nil {
			return graphError("orchestrator execution is fixed by the policy")
		}
		h, _ = p.harness(p.Orchestrator.Harness)
		sel = ExecutionSelection{Harness: h.ID, HarnessName: h.Name, HarnessSource: "orchestrator setting", Tier: p.Orchestrator.Tier, TierSource: "orchestrator setting"}
	} else {
		traits, err := resolveTraits(b.Definition, spec.Traits)
		if err != nil {
			return err
		}
		var source string
		h, source, err = selectHarness(p, b.Definition, traits.Nature, spec.Harness, graphEnforcement(g))
		if err != nil {
			return err
		}
		tier, tierSource := selectTier(p, traits)
		sel = ExecutionSelection{Harness: h.ID, HarnessName: h.Name, HarnessSource: source, Tier: tier, TierSource: tierSource, Traits: traits, Budget: p.SizeBudgets[traits.Size]}
	}
	return e.applySelection(b, h, sel)
}

// applySelection freezes a harness and tier into the binding.
func (e *Engine) applySelection(b *AgentBinding, h ExecutionHarness, sel ExecutionSelection) error {
	launch, err := e.Desk.Agents.Prepare(agentcli.Request{AgentID: h.Runtime, ProfileID: h.ProfileID})
	if err != nil {
		return fmt.Errorf("harness %s: %w", h.Name, err)
	}
	tier := h.Tiers[sel.Tier]
	b.Runtime, b.Model, b.Effort, b.Executable, b.ProfileID = h.Runtime, tier.Model, tier.Effort, launch.Executable, h.ProfileID
	profile, err := e.executionProfile(h.Runtime, h.ProfileID)
	if err != nil {
		return err
	}
	b.Profile = profile
	b.Selection = &sel
	return nil
}

func graphExecution(g *WorkGraph) *AgentExecutionPolicy {
	if g.Setup == nil {
		return defaultExecution("codex", "")
	}
	if g.Setup.Execution != nil {
		return g.Setup.Execution
	}
	return defaultExecution(g.Setup.Runtime, g.Setup.Model)
}

func (e *Engine) executionProfile(runtime, id string) (*agentcli.Profile, error) {
	if id == "" {
		return nil, nil
	}
	installation, err := e.Desk.Agents.Inspect(runtime, id, "")
	if err != nil {
		return nil, err
	}
	for _, p := range installation.Profiles {
		if p.ID == id {
			return &p, nil
		}
	}
	return nil, graphError("environment profile %s is unavailable for %s", id, runtime)
}
