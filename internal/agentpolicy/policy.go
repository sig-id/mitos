// Package agentpolicy separates operator-delegated authority from agent prose.
package agentpolicy

import (
	"errors"
	"mitos/internal/decisionmodel"
	"path/filepath"
	"strings"
)

type Rule struct {
	ID        string `json:"id"`
	Command   string `json:"command"`
	Directory string `json:"directory"` // relative to the actual session workspace
	Action    string `json:"action"`    // allow, review, deny
}
type Policy struct {
	DecisionProvider    *decisionmodel.Config `json:"decision_provider,omitempty"`
	Supervision         Supervision           `json:"supervision"`
	Revision            int                   `json:"revision"`
	Autonomy            string                `json:"autonomy"` // manual, delegated, decision
	Guidance            string                `json:"guidance,omitempty"`
	DecisionApplication string                `json:"decision_application,omitempty"`
	Fallback            string                `json:"fallback"` // human, investigate
	MaxCalls            int                   `json:"max_calls"`
	Rules               []Rule                `json:"rules"`
}

func Default() Policy {
	return Policy{Supervision: Supervision{Mode: "rules", MaxCalls: 16}, Autonomy: "manual", Fallback: "human", MaxCalls: 4, Rules: []Rule{}}
}
func (p Policy) Validate() error {
	if err := p.Supervision.Validate(); err != nil {
		return err
	}
	if p.Revision < 0 || (p.Autonomy != "manual" && p.Autonomy != "delegated" && p.Autonomy != "decision") || (p.Fallback != "human" && p.Fallback != "investigate") || p.MaxCalls < 1 || p.MaxCalls > 64 || len(p.Guidance) > 8000 || strings.ContainsRune(p.Guidance, 0) || len(p.DecisionApplication) > 100 || len(p.Rules) > 64 {
		return errors.New("invalid agent policy")
	}
	if p.DecisionProvider != nil {
		if err := p.DecisionProvider.Validate(); err != nil {
			return err
		}
	}
	if p.Autonomy == "decision" && (strings.TrimSpace(p.Guidance) == "" || p.DecisionProvider == nil && p.DecisionApplication == "") {
		return errors.New("automatic decision review requires a decision provider and approval boundaries")
	}
	seen := map[string]bool{}
	operations := map[string]bool{}
	for _, r := range p.Rules {
		key := r.Command + "\x00" + r.Directory
		if r.ID == "" || len(r.ID) > 100 || seen[r.ID] || operations[key] || r.Command == "" || len(r.Command) > 8000 || strings.ContainsRune(r.Command, 0) || r.Directory == "" || !filepath.IsLocal(r.Directory) || filepath.Clean(r.Directory) != r.Directory || (r.Action != "allow" && r.Action != "review" && r.Action != "deny") {
			return errors.New("rules require unique IDs, exact commands, local clean directories and allow/review/deny actions")
		}
		seen[r.ID] = true
		operations[key] = true
	}
	return nil
}

// Match never treats a command prefix, model description or prose as a grant.
func (p Policy) Match(command, directory, workspace string) (Rule, bool) {
	if !filepath.IsAbs(directory) || !filepath.IsAbs(workspace) {
		return Rule{}, false
	}
	relative, err := filepath.Rel(workspace, directory)
	if err != nil || !filepath.IsLocal(relative) {
		return Rule{}, false
	}
	for _, r := range p.Rules {
		if r.Command == command && r.Directory == relative {
			return r, true
		}
	}
	return Rule{}, false
}

type Supervision struct {
	Mode          string `json:"mode"` // off, rules, model
	Application   string `json:"application,omitempty"`
	Guidance      string `json:"guidance,omitempty"`
	WorkspaceOnly bool   `json:"workspace_only"`
	MaxCalls      int    `json:"max_calls"` // per session, separate from routing
}

func (s Supervision) Effective() Supervision {
	if s.Mode == "" {
		s.Mode = "rules"
	}
	if s.MaxCalls == 0 {
		s.MaxCalls = 16
	}
	return s
}
func (s Supervision) Validate() error {
	s = s.Effective()
	if (s.Mode != "off" && s.Mode != "rules" && s.Mode != "model") || s.MaxCalls < 1 || s.MaxCalls > 64 || len(s.Application) > 100 || len(s.Guidance) > 8000 || strings.ContainsRune(s.Guidance, 0) {
		return errors.New("invalid supervision policy")
	}
	if s.Mode == "model" && (s.Application == "" || strings.TrimSpace(s.Guidance) == "") {
		return errors.New("model supervision requires a configured application and project boundaries")
	}
	return nil
}
