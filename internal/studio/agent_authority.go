package studio

import (
	"fmt"
	"slices"
	"strings"

	"mitos/agents"
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
	},
}

// RuntimeCapabilityProfiles publishes, per runtime, which capabilities a grant
// keeps within scope and which withheld capabilities are actually blocked.
func RuntimeCapabilityProfiles() map[string]any {
	out := map[string]any{}
	for _, runtime := range []string{"codex", "codex-headless"} {
		p, _ := nativeRuntimeProfile(runtime)
		caps := map[string]any{}
		for name, c := range p.Capabilities {
			caps[name] = map[string]any{"bounded_grant": c.BoundedGrant, "blocked_denial": c.BlockedDenial, "note": c.Note}
		}
		out[runtime] = map[string]any{"features": p.Features, "capabilities": caps}
	}
	return out
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
	}
	return runtimeProfile{}, false
}

type CapabilityReceipt struct {
	Name     string `json:"name"`
	Required bool   `json:"required,omitempty"`
	Granted  bool   `json:"granted"`
	Status   string `json:"status"`
	Note     string `json:"note,omitempty"`
}

// AuthorityReceipt freezes the effective authority and limits of one attempt.
type AuthorityReceipt struct {
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
	for _, c := range r.Capabilities {
		if c.Name == name {
			return c.Granted
		}
	}
	return false
}

// resolveAuthority intersects the definition's requests with what the runtime can
// provide and applies the enforcement policy. Without a project grant model the
// grant is the supported part of the definition's request.
func resolveAuthority(d agents.Definition, runtime, policy string, attemptSeconds int) (AuthorityReceipt, error) {
	if !validEnforcement(policy) {
		return AuthorityReceipt{}, fmt.Errorf("unknown enforcement policy %q", policy)
	}
	profile, ok := nativeRuntimeProfile(runtime)
	if !ok {
		return AuthorityReceipt{}, fmt.Errorf("runtime %s has no registered capability profile", runtime)
	}
	m := d.Manifest
	r := AuthorityReceipt{Policy: policy, Runtime: runtime, Definition: fmt.Sprintf("%s@%d", d.ID, d.Revision), Digest: d.Digest, Features: slices.Clone(m.Runtime.Requires), GrantSource: "definition request intersected with runtime support; no project capability grants are configured", MaxTurns: m.Limits.MaxTurns, TimeoutSeconds: m.Limits.TimeoutSeconds, TurnCounting: "model steps, counted as completed tool actions plus the final answer"}
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
		support, supported := profile.Capabilities[name]
		c := CapabilityReceipt{Name: name, Required: required, Granted: requested && supported, Note: support.Note}
		switch {
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
}

func handoffToolGranted(r AuthorityReceipt, tool string) bool {
	capability, ok := handoffToolCapability[tool]
	return !ok || r.Granted(capability)
}
func grantedHandoffTools(r AuthorityReceipt) []harness.DynamicTool {
	out := []harness.DynamicTool{}
	for _, tool := range HandoffTools() {
		if handoffToolGranted(r, tool.Name) {
			out = append(out, tool)
		}
	}
	return out
}
