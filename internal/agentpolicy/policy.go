// Package agentpolicy separates operator-delegated authority from agent prose.
package agentpolicy

import (
	"errors"
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
	Revision            int    `json:"revision"`
	Autonomy            string `json:"autonomy"` // manual, delegated
	Guidance            string `json:"guidance,omitempty"`
	DecisionApplication string `json:"decision_application,omitempty"`
	Fallback            string `json:"fallback"` // human, investigate
	MaxCalls            int    `json:"max_calls"`
	Rules               []Rule `json:"rules"`
}

func Default() Policy {
	return Policy{Autonomy: "manual", Fallback: "human", MaxCalls: 4, Rules: []Rule{}}
}
func (p Policy) Validate() error {
	if p.Revision < 0 || (p.Autonomy != "manual" && p.Autonomy != "delegated") || (p.Fallback != "human" && p.Fallback != "investigate") || p.MaxCalls < 1 || p.MaxCalls > 64 || len(p.Guidance) > 8000 || strings.ContainsRune(p.Guidance, 0) || len(p.DecisionApplication) > 100 || len(p.Rules) > 64 {
		return errors.New("invalid agent policy")
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
