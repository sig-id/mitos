package studio

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/agents"
	"mitos/internal/agentcli"
)

func executionFixture(t *testing.T) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required")
	}
	script, err := os.ReadFile("../../tools/terminal-eval/fixtures/coordinated-agents.py")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, id := range []string{"codex", "claude"} {
		if err = os.WriteFile(filepath.Join(bin, id), append([]byte("#!"+python+"\n"), script...), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("MITOS_TEST_ACCOUNT", "selected-profile")
}
func sameTiers(model string) map[string]ModelTier {
	return map[string]ModelTier{"low": {Model: model, Effort: "low"}, "medium": {Model: model, Effort: "medium"}, "high": {Model: model, Effort: "high"}}
}
func mixedExecution() *AgentExecutionPolicy {
	p := defaultExecution("codex", "")
	p.Harnesses = []ExecutionHarness{{ID: "code", Name: "Implement", Runtime: "codex", Tiers: sameTiers("codex-test")}, {ID: "design", Name: "Design", Runtime: "claude", ProfileID: "claude-work", Tiers: sameTiers("claude-test")}}
	p.HarnessRules = []HarnessRule{{Match: HarnessMatch{Role: "mitos.architect"}, Harness: "design"}}
	p.DefaultHarness, p.AllowedHarnesses, p.Orchestrator = "code", []string{"code", "design"}, OrchestratorExecution{Harness: "code", Tier: "high"}
	return p
}
func traits(size, intelligence string) *TaskTraits {
	return &TaskTraits{Size: size, Intelligence: intelligence}
}
func configureExecutionRegistry(t *testing.T, e *Engine) {
	t.Helper()
	registry, err := agentcli.New(agentcli.Builtins(), agentcli.Config{Profiles: []agentcli.Profile{{ID: "claude-work", Name: "Claude account", AgentID: "claude", Env: map[string]string{"MITOS_FIXTURE_ACCOUNT": "MITOS_TEST_ACCOUNT"}}}}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	e.Desk.Agents = registry
}
func TestMixedCoordinatedExecution(t *testing.T) {
	for _, orchestrator := range []string{"code", "design"} {
		t.Run(orchestrator, func(t *testing.T) {
			executionFixture(t)
			e, req := graphEngine(t, true)
			configureExecutionRegistry(t, e)
			policy := mixedExecution()
			policy.Orchestrator.Harness = orchestrator
			request := AutomaticGoal{Key: "mixed", ProjectID: req.ProjectID, Goal: "Implement and design with the selected agents", Execution: policy}
			g, err := e.StartAutomaticGoal(request)
			if err != nil {
				t.Fatal(err)
			}
			again, err := e.StartAutomaticGoal(request)
			if err != nil || again.ID != g.ID {
				t.Fatalf("replay %v", err)
			}
			changed := request
			changed.Execution = graphClone(policy)
			changed.Execution.DefaultHarness = "design"
			if _, err = e.StartAutomaticGoal(changed); !errors.Is(err, ErrConflict) {
				t.Fatalf("changed request replay: %v", err)
			}
			deadline := time.Now().Add(30 * time.Second)
			for time.Now().Before(deadline) {
				g, err = e.Store.Graph(g.ID)
				if err != nil {
					t.Fatal(err)
				}
				if g.Status == "accepted" {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if g.Status != "accepted" {
				for _, m := range g.Members {
					if m.CurrentRunID != "" {
						r, _ := e.Store.Get(m.CurrentRunID)
						output, _ := e.Store.Artifact(r.ID, "output.txt")
						t.Log(r.Status, r.Summary, string(output))
					}
				}
				t.Fatalf("work not accepted: %+v", g.Expansions)
			}
			found := map[string]bool{}
			for _, m := range g.Members {
				if m.Executor == nil || m.Executor.Application.Agent == nil {
					continue
				}
				b := m.Executor.Application.Agent
				found[b.Runtime] = true
				expected := policy.DefaultHarness
				if b.Definition.ID == "mitos.architect" {
					expected = "design"
				}
				if isOrchestratorKind(b.Definition.Kind) {
					expected = policy.Orchestrator.Harness
				}
				if b.Selection == nil || b.Selection.Harness != expected || b.Model != b.Runtime+"-test" {
					t.Fatalf("wrong binding: %+v", b)
				}
				if b.Runtime == "claude" && (b.ProfileID != "claude-work" || b.Profile == nil) {
					t.Fatal("profile was not frozen")
				}
			}
			if !found["codex"] || !found["claude"] {
				t.Fatal(found)
			}
			// Follow-up inherits the original policy even after project defaults change.
			if _, err = e.SavePreferences(req.ProjectID, RuntimePreferences{Execution: defaultExecution("codex", "different")}); err != nil {
				t.Fatal(err)
			}
			follow, err := e.StartAutomaticGoal(AutomaticGoal{Key: "follow", ProjectID: req.ProjectID, Goal: "Continue the work", FromGraph: g.ID, FromRevision: g.Revision})
			if err != nil {
				t.Fatal(err)
			}
			if graphDigest(follow.Setup.Execution) != graphDigest(policy) {
				t.Fatal("follow-up lost execution policy")
			}
		})
	}
}
func TestExecutionRulesAndValidation(t *testing.T) {
	executionFixture(t)
	e, req := graphEngine(t, false)
	configureExecutionRegistry(t, e)
	policy := mixedExecution()
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "policy", ProjectID: req.ProjectID, Goal: "Check preset selection", Execution: policy})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		role, pin, want string
		traits          *TaskTraits
	}{{"mitos.engineer", "", "code", traits("small", "low")}, {"mitos.architect", "", "design", traits("small", "low")}, {"mitos.engineer", "design", "design", traits("small", "low")}, {"mitos.orchestrator", "", "code", nil}} {
		app, ok := e.agentApplication("agent:" + tc.role)
		if !ok {
			t.Fatal(tc.role)
		}
		if err = e.bindExecution(&g, GraphSpec{Harness: tc.pin, Traits: tc.traits}, app.Agent); err != nil {
			t.Fatal(err)
		}
		if app.Agent.Selection.Harness != tc.want {
			t.Fatal(app.Agent)
		}
	}
	for _, mutate := range []func(*AgentExecutionPolicy){
		func(p *AgentExecutionPolicy) { p.DefaultHarness = "missing" },
		func(p *AgentExecutionPolicy) {
			p.HarnessRules = append(p.HarnessRules, HarnessRule{Match: HarnessMatch{Role: "unknown.role"}, Harness: "code"})
		},
		func(p *AgentExecutionPolicy) { p.Harnesses[1].Runtime = "pi" },
		func(p *AgentExecutionPolicy) { p.Harnesses[1].ProfileID = "missing" },
		func(p *AgentExecutionPolicy) { p.AllowedHarnesses = []string{"code"} },
		func(p *AgentExecutionPolicy) { p.Version = "" },
		func(p *AgentExecutionPolicy) { delete(p.Harnesses[0].Tiers, "high") },
		func(p *AgentExecutionPolicy) { p.Harnesses[1].Tiers["low"] = ModelTier{Effort: "minimal"} },
		func(p *AgentExecutionPolicy) { p.SizeBudgets["large"] = SizeBudget{Minutes: 1, MaxTurns: 1} },
		func(p *AgentExecutionPolicy) {
			p.HarnessRules = append(p.HarnessRules, HarnessRule{Match: HarnessMatch{Role: "mitos.architect", Nature: "deploy"}, Harness: "code"})
		},
		func(p *AgentExecutionPolicy) {
			p.HarnessRules = append(p.HarnessRules, HarnessRule{Match: HarnessMatch{Nature: "context"}, Harness: "code"})
		},
	} {
		bad := graphClone(policy)
		mutate(bad)
		if err = e.validateExecution(bad); err == nil {
			t.Fatal("invalid policy accepted", bad)
		}
	}
	app, _ := e.agentApplication("agent:mitos.engineer")
	if err = e.bindExecution(&g, GraphSpec{Harness: "missing", Traits: traits("small", "low")}, app.Agent); err == nil {
		t.Fatal("unpermitted task harness accepted")
	}
	if err = e.bindExecution(&g, GraphSpec{}, app.Agent); err == nil {
		t.Fatal("native task without traits accepted")
	}
	document := "```mitos\n" + string(executionJSON(t, ExecutionPolicy{Execution: policy})) + "\n```\n"
	preferences, err := documentPolicy(document)
	if err != nil || graphDigest(preferences.Execution) != graphDigest(policy) {
		t.Fatal(preferences, err)
	}
	setup, err := e.ProjectSetup(req.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.SaveInstructions(req.ProjectID, InstructionsEdit{FileHash: setup.FileHash, Document: &document}); err != nil {
		t.Fatal(err)
	}
	frozen, err := e.freezeSetup(req.ProjectID)
	if err != nil || graphDigest(frozen.Execution) != graphDigest(policy) {
		t.Fatal(frozen, err)
	}
	if strings.Contains(string(executionJSON(t, g)), "selected-profile") {
		t.Fatal("credential value persisted")
	}
}

func executionJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestExecutionBindingsSurviveRestartAndDefaultChanges(t *testing.T) {
	executionFixture(t)
	e, req := graphEngine(t, false)
	configureExecutionRegistry(t, e)
	policy := mixedExecution()
	request := AutomaticGoal{Key: "durable-execution", ProjectID: req.ProjectID, Goal: "Retain selected execution", Execution: policy}
	g, err := e.StartAutomaticGoal(request)
	if err != nil {
		t.Fatal(err)
	}
	worker := graphWorker("implement", g.RootTask)
	worker.Brief.AppID = "agent:mitos.engineer"
	worker.Harness = "design"
	worker.Traits = traits("small", "low")
	g, _ = amendGraphTest(t, e, g, "pin-execution", GraphOperation{Kind: "add", Spec: &worker})
	var binding *AgentBinding
	for _, m := range g.Members {
		if m.Executor != nil {
			binding = m.Executor.Application.Agent
		}
	}
	if binding == nil || binding.Runtime != "claude" || binding.Selection.HarnessSource != "task override" {
		t.Fatal(binding)
	}
	before := graphDigest(binding)
	if _, err = e.SavePreferences(req.ProjectID, RuntimePreferences{Execution: defaultExecution("codex", "changed")}); err != nil {
		t.Fatal(err)
	}
	dir := e.Store.Dir()
	if err = e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	saved, err := reopened.Graph(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range saved.Members {
		if m.Executor != nil && graphDigest(m.Executor.Application.Agent) != before {
			t.Fatal("restart retargeted admitted work")
		}
	}
	replayEngine := &Engine{Store: reopened}
	replay, err := replayEngine.StartAutomaticGoal(request)
	if err != nil || replay.ID != g.ID {
		t.Fatal(replay.ID, err)
	}
	if graphDigest(saved.Setup.Execution) != graphDigest(policy) {
		t.Fatal("restart lost role rules")
	}
}

func TestClaudeAuthorityDoesNotClaimConfinement(t *testing.T) {
	library, err := agents.Library()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range library {
		receipt, err := resolveAuthority(d, "claude", EnforcementAdvisory, 60, "")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range receipt.Capabilities {
			if strings.HasPrefix(c.Name, "workspace.") || c.Name == "command.run" || c.Name == "network.read" {
				if c.Status != CapabilityAdvisory {
					t.Fatal(c)
				}
			}
		}
		if _, err = resolveAuthority(d, "claude", EnforcementStrict, 60, ""); err == nil {
			t.Fatalf("strict policy admitted %s on an advisory runtime", d.ID)
		}
	}
}

func TestClaudeExecutionDoesNotRequireCodex(t *testing.T) {
	executionFixture(t)
	e, req := graphEngine(t, false)
	var adapters []agentcli.Adapter
	for _, a := range agentcli.Builtins() {
		if a.Definition().ID == "claude" {
			adapters = append(adapters, a)
		}
	}
	registry, err := agentcli.New(adapters, agentcli.Config{}, os.Environ())
	if err != nil {
		t.Fatal(err)
	}
	e.Desk.Agents = registry
	if ready, _ := e.AutomaticAgents()["ready"].(bool); !ready {
		t.Fatal("Claude-only installation reported unavailable")
	}
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "claude-only", ProjectID: req.ProjectID, Goal: "Use the installed Claude", Execution: defaultExecution("claude", "claude-test")})
	if err != nil {
		t.Fatal(err)
	}
	worker := graphWorker("architect", g.RootTask)
	worker.Brief.AppID = "agent:mitos.architect"
	worker.Traits = traits("small", "low")
	_, executor, err := e.resolveGraphSpec(&g, worker)
	if err != nil {
		t.Fatal(err)
	}
	if executor.Application.Agent.Runtime != "claude" {
		t.Fatal("Codex fallback")
	}
}
