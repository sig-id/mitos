// Package agentrouting selects resolvers without granting authority or executing effects.
package agentrouting

import (
	"encoding/json"
	"mitos/internal/agentpolicy"
	"mitos/internal/decisionmodel"
	"mitos/internal/harness"
)

type Route struct {
	Resolver string `json:"resolver"` // direct, decision, investigate, human, classify
	Choice   string `json:"choice,omitempty"`
	RuleID   string `json:"rule_id,omitempty"`
	Source   string `json:"source"`
	Reason   string `json:"reason"`
}

func Prepare(p agentpolicy.Policy, r harness.Request, workspace string) Route {
	human := func(reason string) Route { return Route{Resolver: "human", Source: "rule", Reason: reason} }
	if r.Method == "item/commandExecution/requestApproval" || r.Method == "PreToolUse" && len(r.Questions) == 0 {
		command, directory, ok := Command(r)
		if !ok {
			return human("unsupported approval scope")
		}
		rule, matched := p.Match(command, directory, workspace)
		if matched && rule.Action == "deny" {
			return Route{Resolver: "direct", Choice: "deny", RuleID: rule.ID, Source: "rule", Reason: "explicit deny rule"}
		}
		if p.Autonomy != "delegated" && p.Autonomy != "decision" {
			return human("automatic resolution is disabled")
		}
		if !matched && p.Autonomy == "decision" {
			return Route{Resolver: "decision", Source: "policy", Reason: "operator delegated command review within approval boundaries"}
		}
		if !matched {
			return human("no delegated authority for this exact operation")
		}
		if rule.Action == "allow" {
			return Route{Resolver: "direct", Choice: "approve", RuleID: rule.ID, Source: "rule", Reason: "exact delegated operation"}
		}
		return Route{Resolver: "decision", RuleID: rule.ID, Source: "rule", Reason: "delegated operation requires decision review"}
	}
	if p.Autonomy != "delegated" && p.Autonomy != "decision" {
		return human("automatic resolution is disabled")
	}
	if len(r.Questions) == 0 {
		return human("unsupported request kind")
	}
	return Route{Resolver: "classify", Source: "classifier", Reason: "question requires semantic route classification"}
}

// Additional access requests are not covered by an exact command grant.
func Command(r harness.Request) (string, string, bool) {
	if r.Method == "item/commandExecution/requestApproval" {
		var e struct {
			Params struct {
				Command    string          `json:"command"`
				CWD        string          `json:"cwd"`
				Additional json.RawMessage `json:"additionalPermissions"`
				Network    json.RawMessage `json:"networkApprovalContext"`
			} `json:"params"`
		}
		if json.Unmarshal(r.Payload, &e) != nil {
			return "", "", false
		}
		p := e.Params
		extra := func(b json.RawMessage) bool { return len(b) > 0 && string(b) != "null" }
		return p.Command, p.CWD, p.Command != "" && p.CWD != "" && !extra(p.Additional) && !extra(p.Network)
	}
	if r.Method == "PreToolUse" {
		var e struct {
			Tool  string `json:"tool_name"`
			CWD   string `json:"cwd"`
			Input struct {
				Command string `json:"command"`
			} `json:"tool_input"`
		}
		if json.Unmarshal(r.Payload, &e) == nil && e.Tool == "Bash" {
			return e.Input.Command, e.CWD, e.CWD != "" && e.Input.Command != ""
		}
	}
	return "", "", false
}
func CanChoose(r harness.Request) bool {
	if len(r.Cards) == 0 {
		return false
	}
	for _, c := range r.Cards {
		if c.Kind == "supply_value" {
			return false
		}
	}
	return true
}
func Choices(r harness.Request, investigate bool) []decisionmodel.Choice {
	out := []decisionmodel.Choice{{Value: "human", Description: "Needs user intent, unavailable facts, or explicit user judgment."}}
	if CanChoose(r) {
		out = append(out, decisionmodel.Choice{Value: "decision", Description: "Supplied evidence is sufficient to select one of the existing answer options."})
	}
	if investigate {
		out = append(out, decisionmodel.Choice{Value: "investigate", Description: "Requires a bounded read-only investigation before an answer can be proposed."})
	}
	return out
}
