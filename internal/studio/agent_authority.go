package studio

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"mitos/agents"
	"mitos/internal/agentcli"
	"mitos/internal/compute"
	"mitos/internal/harness"
)

// Enforcement policies decide which unenforceable permissions block an attempt.
// Missing runtime features and unsupported required capabilities always block.
const (
	EnforcementAdvisory       = "advisory"        // record what is and is not enforced
	EnforcementStrictRequired = "strict_required" // refuse advisory required capabilities
	EnforcementStrict         = "strict"          // refuse any advisory capability
)

func validEnforcement(v string) bool {
	return v == EnforcementAdvisory || v == EnforcementStrictRequired || v == EnforcementStrict
}

// Capability statuses in an authority receipt.
const (
	CapabilityEnforced = "enforced" // the runtime bounds the grant, or blocks the withheld capability
	CapabilityAdvisory = "advisory" // the runtime cannot bound or block it as declared
	CapabilityRefused  = "refused"  // requested but unsupported by the runtime
)

// runtimeProfile states what a native runtime can provide and what it can confine.
type runtimeProfile struct {
	Features []string
	// Capabilities the runtime can provide, with whether a grant stays within the
	// declared scope and whether withholding it is actually blocked.
	Capabilities map[string]capabilitySupport
}
type capabilitySupport struct {
	BoundedGrant  bool
	BlockedDenial bool
	Note          string
	// Withheld marks a capability the execution provider denies entirely, so a
	// request can never become a grant even though the runtime knows it.
	Withheld string
}

// Codex on the local process provider. Its sandbox confines writes and can disable
// network access; it does not confine reads or disable command execution.
var codexProfile = runtimeProfile{
	Features: []string{"native_output", "tool_use", "proposal_tools"},
	Capabilities: map[string]capabilitySupport{
		"workspace.read":   {BoundedGrant: false, BlockedDenial: false, Note: "The sandbox does not confine reads; any readable host file is accessible."},
		"workspace.write":  {BoundedGrant: true, BlockedDenial: true, Note: "Writes are confined to the workspace copy and temporary directories; withheld means a read-only sandbox."},
		"command.run":      {BoundedGrant: true, BlockedDenial: false, Note: "Commands run inside the sandbox; command execution cannot be disabled."},
		"network.read":     {BoundedGrant: false, BlockedDenial: true, Note: "Granted network access is not limited to reads; withheld network access is disabled by the sandbox."},
		"artifact.read":    {BoundedGrant: true, BlockedDenial: true, Note: "Mitos exposes the artifact read tools only when granted."},
		"artifact.write":   {BoundedGrant: true, BlockedDenial: true, Note: "Mitos exposes artifact registration only when granted."},
		"graph.read":       {BoundedGrant: true, BlockedDenial: true, Note: "Graph state is supplied by Mitos as context."},
		"graph.propose":    {BoundedGrant: true, BlockedDenial: true, Note: "Proposals are validated and committed only by the kernel."},
		"agent.specialize": {BoundedGrant: true, BlockedDenial: true, Note: "Specializations are validated against the base definition by the kernel."},
		ProposalCapability: {BoundedGrant: true, BlockedDenial: true, Note: "Workers propose work only on their terminal result; the orchestrator admits it through graph validation. Native subagents are disabled."},
		WatchCapability:    {BoundedGrant: true, BlockedDenial: true, Note: "Mitos exposes the watch tools only when granted; watch scope is clamped to the attempt's graph."},
	},
}

// RuntimeCapabilityProfiles publishes, per runtime, which capabilities a grant
// keeps within scope and which withheld capabilities are actually blocked. The
// effective profile is derived from the execution provider's capabilities.
func RuntimeCapabilityProfiles() map[string]any {
	return RuntimeCapabilityProfilesFor(compute.Capabilities{Isolation: "process", Egress: "unrestricted", NetModes: []string{"unrestricted"}})
}

func RuntimeCapabilityProfilesFor(provider compute.Capabilities) map[string]any {
	out := map[string]any{}
	for _, runtime := range append(agentcli.CoordinatedRuntimes(), "codex-headless") {
		base, _ := nativeRuntimeProfile(runtime)
		p := confinedProfile(base, provider)
		caps := map[string]any{}
		for name, c := range p.Capabilities {
			caps[name] = map[string]any{"bounded_grant": c.BoundedGrant, "blocked_denial": c.BlockedDenial, "note": c.Note}
		}
		out[runtime] = map[string]any{"features": p.Features, "capabilities": caps}
	}
	return out
}

// AgentCapabilityReport publishes the execution provider's enforced posture and
// the runtime profiles derived from it.
func (e *Engine) AgentCapabilityReport() map[string]any {
	caps := e.agentProviderCapabilities()
	return map[string]any{"provider": caps, "runtimes": RuntimeCapabilityProfilesFor(caps)}
}

// Headless structured work runs Codex read-only in an empty scratch directory
// without Mitos handoff tools: there is no workspace to read or write and no
// artifact or graph access.
var codexHeadlessProfile = runtimeProfile{
	Features: []string{"native_output", "tool_use"},
	Capabilities: map[string]capabilitySupport{
		"command.run": {BoundedGrant: true, BlockedDenial: false, Note: "Commands run inside a read-only sandbox in the scratch directory; command execution cannot be disabled."},
	},
}

func nativeRuntimeProfile(runtime string) (runtimeProfile, bool) {
	switch runtime {
	case "codex":
		return codexProfile, true
	case "codex-headless":
		return codexHeadlessProfile, true
	default:
		if runtime != "claude" && !agentcli.PrintRuntime(runtime) {
			return runtimeProfile{}, false
		}
		p := runtimeProfile{Features: slices.Clone(codexProfile.Features), Capabilities: map[string]capabilitySupport{}}
		for name, c := range codexProfile.Capabilities {
			if strings.HasPrefix(name, "workspace.") || name == "command.run" || name == "network.read" {
				c = capabilitySupport{Note: runtime + " native tool restrictions are advisory on the local process provider; they do not provide OS confinement."}
			}
			if name == ProposalCapability && agentcli.PrintRuntime(runtime) {
				c = capabilitySupport{Note: "Mitos validates graph proposals. Native CLI delegation is restricted by instruction only."}
			}
			p.Capabilities[name] = c
		}
		return p, true
	}
}

type CapabilityReceipt struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
	Granted  bool   `json:"granted"`
	Status   string `json:"status"`
	Note     string `json:"note,omitempty"`
}

// natureCeilings lists capabilities a task nature withholds whatever the role
// may be granted. A nature narrows authority; it never widens it.
var natureCeilings = map[string][]string{
	"context": {"workspace.write"},
	"review":  {"workspace.write"},
}

// NatureCeilings publishes which capabilities each task nature withholds.
func NatureCeilings() map[string][]string {
	out := map[string][]string{}
	for k, v := range natureCeilings {
		out[k] = slices.Clone(v)
	}
	return out
}

// AuthorityReceipt freezes the effective authority and limits of one attempt.
type AuthorityReceipt struct {
	Nature         string              `json:"nature,omitempty"`
	Policy         string              `json:"policy"`
	Runtime        string              `json:"runtime"`
	Definition     string              `json:"definition"`
	Digest         string              `json:"digest"`
	Features       []string            `json:"features"`
	Capabilities   []CapabilityReceipt `json:"capabilities"`
	GrantSource    string              `json:"grant_source"`
	TimeoutSeconds int                 `json:"timeout_seconds"`
	MaxTurns       int                 `json:"max_turns"`
	TurnCounting   string              `json:"turn_counting"`
}

func (r AuthorityReceipt) Granted(name string) bool {
	return r.capability(name).Granted
}

func (r AuthorityReceipt) capability(name string) CapabilityReceipt {
	for _, c := range r.Capabilities {
		if c.Name == name {
			return c
		}
	}
	return CapabilityReceipt{Name: name, Status: CapabilityRefused}
}

// confinedProfile derives the effective runtime profile from the execution
// provider instead of the fixed advisory table: OS confinement makes reads and
// withheld network enforceable, and receipts say only what the provider proves.
func confinedProfile(base runtimeProfile, caps compute.Capabilities) runtimeProfile {
	if !caps.WorkspaceConfined {
		return base
	}
	p := runtimeProfile{Features: slices.Clone(base.Features), Capabilities: map[string]capabilitySupport{}}
	for name, c := range base.Capabilities {
		p.Capabilities[name] = c
	}
	p.Capabilities["workspace.read"] = capabilitySupport{BoundedGrant: true, BlockedDenial: true, Note: "Reads are confined to the mounted workspace copy, dependency inputs, the runtime home and required toolchains; other host paths are not mounted."}
	p.Capabilities["workspace.write"] = capabilitySupport{BoundedGrant: true, Note: "Writes are confined to the mounted workspace copy, the runtime home and temporary directories."}
	p.Capabilities["command.run"] = capabilitySupport{BoundedGrant: true, Note: "Commands run inside the OS-confined sandbox; execution cannot be disabled."}
	if caps.ReadOnlyWorkspace {
		p.Capabilities["workspace.write"] = capabilitySupport{BoundedGrant: true, BlockedDenial: true, Note: "Writes are confined to the mounted workspace copy, the runtime home and temporary directories; withheld means the workspace copy is mounted read-only."}
	}
	if slices.Contains(caps.NetModes, compute.NetBroker) {
		const brokered = "General network access is withheld under this provider: the runtime reaches only its model API, through the per-attempt Mitos credential broker, and holds no credential itself."
		p.Capabilities["network.read"] = capabilitySupport{BlockedDenial: true, Withheld: brokered, Note: brokered}
	} else if !slices.Contains(caps.NetModes, compute.NetUnrestricted) || caps.Egress == compute.NetOff {
		p.Capabilities["network.read"] = capabilitySupport{BlockedDenial: true, Withheld: "Network access is withheld for every attempt under the confining provider; the runtime's own model transport is equally unreachable, so agents are admitted only with unrestricted egress.", Note: "Network access is withheld for every attempt under the confining provider; the runtime's own model transport is equally unreachable, so agents are admitted only with unrestricted egress."}
	}
	return p
}

// resolveAuthority intersects the definition's requests with what the runtime can
// provide and applies the enforcement policy. Without a project grant model the
// grant is the supported part of the definition's request.
func resolveAuthority(d agents.Definition, runtime, policy string, attemptSeconds int, nature string, caps compute.Capabilities) (AuthorityReceipt, error) {
	if !validEnforcement(policy) {
		return AuthorityReceipt{}, fmt.Errorf("unknown enforcement policy %q", policy)
	}
	base, ok := nativeRuntimeProfile(runtime)
	if !ok {
		return AuthorityReceipt{}, fmt.Errorf("runtime %s has no registered capability profile", runtime)
	}
	profile := confinedProfile(base, caps)
	m := d.Manifest
	r := AuthorityReceipt{Nature: nature, Policy: policy, Runtime: runtime, Definition: fmt.Sprintf("%s@%d", d.ID, d.Revision), Digest: d.Digest, Features: slices.Clone(m.Runtime.Requires), GrantSource: "definition request intersected with runtime support; no project capability grants are configured", MaxTurns: m.Limits.MaxTurns, TimeoutSeconds: m.Limits.TimeoutSeconds, TurnCounting: "model steps, counted as completed tool actions plus the final answer"}
	if agentcli.PrintRuntime(runtime) {
		r.TurnCounting = "Non-interactive CLI invocations, one Mitos tool request or final response per invocation. CLI-internal steps are not observable."
	}
	if attemptSeconds > 0 && attemptSeconds < r.TimeoutSeconds {
		r.TimeoutSeconds = attemptSeconds
	}
	var problems []string
	for _, f := range m.Runtime.Requires {
		if !slices.Contains(profile.Features, f) {
			problems = append(problems, "runtime feature "+f+" is unavailable")
		}
	}
	names := make([]string, 0, len(profile.Capabilities))
	for name := range profile.Capabilities {
		names = append(names, name)
	}
	for _, name := range append(slices.Clone(m.Capabilities.Required), m.Capabilities.Optional...) {
		if _, ok := profile.Capabilities[name]; !ok && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	for _, name := range names {
		required := slices.Contains(m.Capabilities.Required, name)
		requested := required || slices.Contains(m.Capabilities.Optional, name)
		withheld := slices.Contains(natureCeilings[nature], name)
		if withheld {
			required, requested = false, false
		}
		support, supported := profile.Capabilities[name]
		c := CapabilityReceipt{Name: name, Required: required, Granted: requested && supported, Note: support.Note}
		if support.Withheld != "" {
			// The provider denies this capability outright: no grant exists and
			// the denial is enforced, but a required request still blocks.
			c.Granted = false
			c.Status = CapabilityEnforced
			c.Note = support.Withheld
			if required {
				problems = append(problems, "required capability "+name+" is withheld by the execution provider")
			}
			r.Capabilities = append(r.Capabilities, c)
			continue
		}
		switch {
		case withheld && support.BlockedDenial:
			c.Status, c.Note = CapabilityEnforced, "Withheld by the task's "+nature+" nature."
		case withheld:
			c.Status, c.Note = CapabilityAdvisory, "Withheld by the task's "+nature+" nature; "+runtime+" cannot block it: "+support.Note
			if policy == EnforcementStrict {
				problems = append(problems, name+" cannot be withheld: "+support.Note)
			}
		case requested && !supported:
			c.Status, c.Note = CapabilityRefused, "The runtime cannot provide this capability."
			if required {
				problems = append(problems, "required capability "+name+" is unsupported")
			}
		case c.Granted && support.BoundedGrant, !c.Granted && support.BlockedDenial:
			c.Status = CapabilityEnforced
		default:
			c.Status = CapabilityAdvisory
			if policy == EnforcementStrict || (policy == EnforcementStrictRequired && required) {
				problems = append(problems, name+" cannot be enforced: "+support.Note)
			}
		}
		r.Capabilities = append(r.Capabilities, c)
	}
	if len(problems) > 0 {
		return r, &agentPrerequisiteError{summary: fmt.Sprintf("%s cannot run on %s under the %s policy: %s", r.Definition, runtime, policy, strings.Join(problems, "; "))}
	}
	return r, nil
}

// handoffToolCapability names the capability a handoff tool exercises; tools
// without an entry are part of the attempt protocol itself.
var handoffToolCapability = map[string]string{
	"artifact_register": "artifact.write",
	"artifact_list":     "artifact.read",
	"artifact_read":     "artifact.read",
	"artifact_search":   "artifact.read",
	"watch_create":      WatchCapability,
	"watch_renew":       WatchCapability,
	"watch_cancel":      WatchCapability,
	"watch_inbox":       WatchCapability,
	"delivery_outcome":  WatchCapability,
}

func handoffToolGranted(r AuthorityReceipt, tool string) bool {
	capability, ok := handoffToolCapability[tool]
	return !ok || r.Granted(capability)
}
func grantedHandoffTools(r AuthorityReceipt) []harness.DynamicTool {
	out := []harness.DynamicTool{}
	for _, tool := range HandoffTools() {
		if !handoffToolGranted(r, tool.Name) {
			continue
		}
		if tool.Name == "result_submit" && !r.Granted(ProposalCapability) {
			tool.InputSchema = withoutProperty(tool.InputSchema, "proposals")
		}
		out = append(out, tool)
	}
	return out
}

// withoutProperty removes one property from an object schema so that an
// ungranted field is not advertised at all.
func withoutProperty(raw json.RawMessage, name string) json.RawMessage {
	var schema map[string]any
	if json.Unmarshal(raw, &schema) != nil {
		return raw
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		delete(props, name)
	}
	if required, ok := schema["required"].([]any); ok {
		schema["required"] = slices.DeleteFunc(required, func(v any) bool { return v == name })
	}
	out, _ := json.Marshal(schema)
	return out
}

// runGranted reads the attempt's frozen authority receipt. A run without one
// (or one that cannot be read) is granted nothing.
func (s *Store) runGranted(run, capability string) bool {
	raw, err := s.Artifact(run, "authority.json")
	if err != nil {
		return false
	}
	var r AuthorityReceipt
	if json.Unmarshal(raw, &r) != nil {
		return false
	}
	return r.Granted(capability)
}
