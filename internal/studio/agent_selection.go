package studio

import (
	"fmt"
	"slices"
	"strings"

	"mitos/internal/agentcli"
)

// RuntimeCandidate records one runtime considered for a binding.
type RuntimeCandidate struct {
	Runtime    string `json:"runtime"`
	Available  bool   `json:"available"`
	Executable string `json:"executable,omitempty"`
	// Profile names the environment profile that supplied the executable when
	// the runtime is installed through a profile override or profile PATH.
	Profile    string `json:"profile,omitempty"`
	Compatible bool   `json:"compatible"`
	Reason     string `json:"reason,omitempty"`
}

// RuntimeSelection is the frozen selection receipt for one agent binding: the
// candidates considered, each compatibility check, and the rationale for the
// applied runtime. It is part of the binding digest, so replay preserves the
// exact selection without model calls.
type RuntimeSelection struct {
	Runtime    string             `json:"runtime"`
	Candidates []RuntimeCandidate `json:"candidates"`
	Rationale  string             `json:"rationale"`
}

// prepareRuntime finds a launchable installation for one runtime, preferring
// the binding's own environment profile and then any registered profile, so
// runtimes installed through a profile executable or profile PATH are not
// misread as unavailable.
func (e *Engine) prepareRuntime(runtime, profileID string) (agentcli.Launch, error) {
	attempts := []agentcli.Request{{AgentID: runtime, ProfileID: profileID}}
	if profileID != "" {
		attempts = append(attempts, agentcli.Request{AgentID: runtime})
	}
	for _, req := range attempts {
		if launch, err := e.Desk.Agents.Prepare(req); err == nil {
			return launch, nil
		}
	}
	for _, p := range e.Desk.Agents.AgentProfiles(runtime) {
		if p.ID == profileID {
			continue
		}
		if launch, err := e.Desk.Agents.Prepare(agentcli.Request{AgentID: runtime, ProfileID: p.ID}); err == nil {
			return launch, nil
		}
	}
	return agentcli.Launch{}, fmt.Errorf("%s is not installed", runtime)
}

// selectAgentRuntime validates the definition's runtime requirements against
// every installed runtime and freezes the selection receipt into the binding.
// Preferences only order compatible candidates; unsupported hard requirements
// never silently disappear: an incompatible selected runtime is refused, and
// the default worker runtime falls back to a compatible installed runtime only
// when the preset imposed no explicit choice.
func (e *Engine) selectAgentRuntime(b *AgentBinding) error {
	if e.Desk == nil || e.Desk.Agents == nil {
		return &agentPrerequisiteError{summary: "Coding agent discovery is unavailable; cannot select a runtime for " + b.Definition.ID + "."}
	}
	m := b.Definition.Manifest
	candidates := []RuntimeCandidate{}
	for _, runtime := range []string{"codex", "claude"} {
		c := RuntimeCandidate{Runtime: runtime}
		// The binding's own profile is preferred for its runtime; other runtimes
		// are discovered through any registered profile.
		discoverProfile := ""
		if runtime == b.Runtime {
			discoverProfile = b.ProfileID
		}
		if launch, err := e.prepareRuntime(runtime, discoverProfile); err == nil {
			c.Available = true
			c.Executable = launch.Executable
			c.Profile = launch.ProfileID
		} else {
			c.Reason = "not installed"
		}
		profile, known := nativeRuntimeProfile(runtime)
		if !known {
			c.Reason = "no registered capability profile"
		} else {
			var missing []string
			for _, feature := range m.Runtime.Requires {
				if !slices.Contains(profile.Features, feature) {
					missing = append(missing, feature)
				}
			}
			if len(missing) > 0 {
				c.Reason = "missing features: " + strings.Join(missing, ", ")
			} else {
				c.Compatible = true
			}
		}
		candidates = append(candidates, c)
	}
	// Preferred runtimes surface first among compatible candidates.
	slices.SortStableFunc(candidates, func(x, y RuntimeCandidate) int {
		px, py := slices.Contains(m.Runtime.Preferences, x.Runtime), slices.Contains(m.Runtime.Preferences, y.Runtime)
		switch {
		case px == py:
			return 0
		case px:
			return -1
		default:
			return 1
		}
	})
	selection := RuntimeSelection{Candidates: candidates}
	bound := slices.IndexFunc(candidates, func(c RuntimeCandidate) bool { return c.Runtime == b.Runtime })
	if bound >= 0 && candidates[bound].Available && candidates[bound].Compatible {
		selection.Runtime = b.Runtime
		selection.Rationale = b.Runtime + " satisfies runtime.requires"
		if b.SelectionSource != "" {
			selection.Rationale += " (" + b.SelectionSource + ")"
		}
		// Profile-backed discovery fills the executable for bindings that have
		// not been prepared by an execution preset yet.
		if b.Executable == "" {
			b.Executable = candidates[bound].Executable
		}
		b.Selection = &selection
		return nil
	}
	fallback := slices.IndexFunc(candidates, func(c RuntimeCandidate) bool { return c.Available && c.Compatible })
	if fallback >= 0 && !explicitRuntimePin(b.SelectionSource) {
		chosen := candidates[fallback]
		launch, err := e.prepareRuntime(chosen.Runtime, chosen.Profile)
		if err != nil {
			return &agentPrerequisiteError{summary: fmt.Sprintf("Runtime %s was selected for %s but is no longer available.", chosen.Runtime, b.Definition.ID)}
		}
		b.Runtime, b.Executable, b.Preset, b.Model = chosen.Runtime, launch.Executable, nil, ""
		b.ProfileID = launch.ProfileID
		profile, err := e.executionProfile(chosen.Runtime, launch.ProfileID)
		if err != nil {
			return &agentPrerequisiteError{summary: fmt.Sprintf("Runtime %s was selected for %s but its environment profile is unavailable.", chosen.Runtime, b.Definition.ID)}
		}
		b.Profile = profile
		selection.Runtime = chosen.Runtime
		selection.Rationale = "default runtime was incompatible; fell back to " + chosen.Runtime
		b.Selection = &selection
		return nil
	}
	if bound >= 0 && !candidates[bound].Compatible {
		return &agentPrerequisiteError{summary: fmt.Sprintf("%s cannot run on %s: %s. Considered: %s.", b.Definition.ID, b.Runtime, candidates[bound].Reason, summarizeCandidates(candidates))}
	}
	if fallback >= 0 {
		return &agentPrerequisiteError{summary: fmt.Sprintf("%s is not bound to a compatible runtime. Considered: %s.", b.Definition.ID, summarizeCandidates(candidates))}
	}
	return &agentPrerequisiteError{summary: fmt.Sprintf("No installed runtime provides the features %s requires (%s). Install a compatible coding agent.", b.Definition.ID, strings.Join(m.Runtime.Requires, ", "))}
}

// explicitRuntimePin reports whether the runtime was chosen by an explicit
// preset, role rule or task selection; implicit defaults may fall back to a
// compatible installed runtime instead of refusing.
func explicitRuntimePin(source string) bool {
	return source == "role rule" || source == "task selection" || source == "orchestrator setting"
}

func summarizeCandidates(candidates []RuntimeCandidate) string {
	parts := []string{}
	for _, c := range candidates {
		state := "compatible"
		switch {
		case !c.Available:
			state = "unavailable"
		case !c.Compatible:
			state = c.Reason
		}
		parts = append(parts, c.Runtime+" "+state)
	}
	return strings.Join(parts, "; ")
}
