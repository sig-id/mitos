package studio

import (
	"fmt"
	"slices"
	"strings"

	"mitos/internal/agentcli"
)

// Execution presets select a native process independently of a portable role.
// The complete policy is frozen on the graph; attempts freeze their resolved preset.
type ExecutionPreset struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Runtime   string `json:"runtime"`
	Model     string `json:"model,omitempty"`
	ProfileID string `json:"profile_id,omitempty"`
}
type AgentExecutionPolicy struct {
	Presets        []ExecutionPreset `json:"presets"`
	Orchestrator   string            `json:"orchestrator"`
	Worker         string            `json:"worker"`
	Roles          map[string]string `json:"roles"`
	AllowedWorkers []string          `json:"allowed_workers"`
}

func defaultExecution(runtime, model string) *AgentExecutionPolicy {
	if runtime == "" {
		runtime = "codex"
	}
	return &AgentExecutionPolicy{Presets: []ExecutionPreset{{ID: "default", Name: "Default", Runtime: runtime, Model: model}}, Orchestrator: "default", Worker: "default", Roles: map[string]string{}, AllowedWorkers: []string{"default"}}
}
func (p *AgentExecutionPolicy) preset(id string) (ExecutionPreset, bool) {
	for _, v := range p.Presets {
		if v.ID == id {
			return v, true
		}
	}
	return ExecutionPreset{}, false
}
func (p *AgentExecutionPolicy) validate() error {
	if p == nil {
		return nil
	}
	if len(p.Presets) < 1 || len(p.Presets) > 32 || len(p.Roles) > 128 || len(p.AllowedWorkers) < 1 || len(p.AllowedWorkers) > 32 {
		return graphError("provide 1–32 execution presets and permitted worker presets")
	}
	ids := map[string]bool{}
	for _, v := range p.Presets {
		if !validGraphKey(v.ID) || ids[v.ID] || len(strings.TrimSpace(v.Name)) < 1 || len(v.Name) > 100 {
			return graphError("execution presets need unique IDs and names")
		}
		ids[v.ID] = true
		if v.Runtime != "codex" && v.Runtime != "claude" {
			return graphError("coordinated work supports Codex and Claude Code; %s has no coordinated adapter", v.Runtime)
		}
		if len(v.Model) > 200 || strings.ContainsAny(v.Model, "\x00\r\n") || strings.HasPrefix(v.Model, "-") || len(v.ProfileID) > 128 || strings.ContainsAny(v.ProfileID, "\x00\r\n") {
			return graphError("invalid model or environment profile in preset %s", v.ID)
		}
	}
	for i, id := range p.AllowedWorkers {
		if !ids[id] || slices.Contains(p.AllowedWorkers[:i], id) {
			return graphError("unknown or duplicate permitted worker preset %s", id)
		}
	}
	if !ids[p.Orchestrator] || !slices.Contains(p.AllowedWorkers, p.Worker) {
		return graphError("select an orchestrator preset and a permitted default worker preset")
	}
	for role, id := range p.Roles {
		if !validGraphKey(role) || !slices.Contains(p.AllowedWorkers, id) {
			return graphError("role %s must select a permitted worker preset", role)
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
	for role := range p.Roles {
		found := false
		for _, d := range library {
			if d.ID == role && !isOrchestratorKind(d.Kind) {
				found = true
			}
		}
		if !found {
			return graphError("unknown worker role %s", role)
		}
	}
	for _, v := range p.Presets {
		if _, err := e.Desk.Agents.Prepare(agentcli.Request{AgentID: v.Runtime, ProfileID: v.ProfileID}); err != nil {
			return graphError("preset %s: %v", v.Name, err)
		}
	}

	return nil
}
func (e *Engine) bindExecution(g *WorkGraph, spec GraphSpec, b *AgentBinding) error {
	p := graphExecution(g)
	if err := p.validate(); err != nil {
		return err
	}
	id, source := p.Worker, "worker default"
	if role := p.Roles[b.Definition.ID]; role != "" {
		id, source = role, "role rule"
	}
	if spec.ExecutionPreset != "" {
		if !slices.Contains(p.AllowedWorkers, spec.ExecutionPreset) {
			return graphError("task execution preset is outside the permitted worker presets")
		}
		id, source = spec.ExecutionPreset, "task selection"
	}
	if isOrchestratorKind(b.Definition.Kind) {
		if spec.ExecutionPreset != "" && spec.ExecutionPreset != p.Orchestrator {
			return graphError("task cannot override the orchestrator preset")
		}
		id, source = p.Orchestrator, "orchestrator setting"
	}
	preset, ok := p.preset(id)
	if !ok {
		return graphError("unknown execution preset %s", id)
	}
	launch, err := e.Desk.Agents.Prepare(agentcli.Request{AgentID: preset.Runtime, ProfileID: preset.ProfileID})
	if err != nil {
		return fmt.Errorf("preset %s: %w", preset.Name, err)
	}
	b.Runtime, b.Model, b.Executable, b.ProfileID = preset.Runtime, preset.Model, launch.Executable, preset.ProfileID
	profile, err := e.executionProfile(preset.Runtime, preset.ProfileID)
	if err != nil {
		return err
	}
	b.Profile = profile
	b.Preset = &preset
	b.SelectionSource = source
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
