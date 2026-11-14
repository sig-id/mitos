package studio

import (
	"mitos/internal/agentpolicy"
)

type ProjectAgentPolicy struct {
	ProjectID string             `json:"project_id"`
	Policy    agentpolicy.Policy `json:"policy"`
}

func (s *Store) projectAgentPolicy(project string) agentpolicy.Policy {
	p, ok := s.agentPolicies[project]
	if !ok {
		p = agentpolicy.Default()
	}
	p = graphClone(p)
	// Global changes invalidate cached routes and in-flight automatic answers.
	p.Revision += s.systemConfiguration.Revision
	return p
}
func (s *Store) agentPolicy(project string) agentpolicy.Policy {
	p := s.projectAgentPolicy(project)
	if p.DecisionProvider == nil && p.DecisionApplication == "" {
		p.DecisionProvider = graphClone(s.systemConfiguration.DecisionProvider)
		p.DecisionApplication = s.systemConfiguration.DecisionApplication
	}
	return p
}
func (s *Store) AgentPolicy(project string) agentpolicy.Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.projectAgentPolicy(project)
}
func (e *Engine) SaveAgentPolicy(project string, p agentpolicy.Policy) (agentpolicy.Policy, error) {
	if _, err := e.Desk.Project(project); err != nil {
		return p, err
	}
	if err := p.Validate(); err != nil {
		return p, err
	}
	if err := e.validateConfiguration(SystemConfiguration{Revision: p.Revision, DecisionProvider: p.DecisionProvider, DecisionApplication: p.DecisionApplication, Agents: p.AgentLaunch}); err != nil {
		return p, err
	}
	p.Supervision = p.Supervision.Effective()
	if p.Supervision.Mode == "model" {
		if _, err := e.decisionConfig(p.Supervision.Application); err != nil {
			return p, err
		}
	}
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentPolicy(project).Revision != p.Revision {
		return p, ErrConflict
	}
	p.Revision++
	p.Revision -= s.systemConfiguration.Revision
	if err := s.commit("", "effect", "project_agent_policy_changed", change{AgentPolicy: &ProjectAgentPolicy{ProjectID: project, Policy: p}}); err != nil {
		return p, err
	}
	return s.projectAgentPolicy(project), nil
}
