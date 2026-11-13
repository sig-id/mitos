package studio

import (
	"errors"
	"mitos/internal/agentpolicy"
)

type ProjectAgentPolicy struct {
	ProjectID string             `json:"project_id"`
	Policy    agentpolicy.Policy `json:"policy"`
}

func (s *Store) agentPolicy(project string) agentpolicy.Policy {
	if p, ok := s.agentPolicies[project]; ok {
		return graphClone(p)
	}
	return agentpolicy.Default()
}
func (s *Store) AgentPolicy(project string) agentpolicy.Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.agentPolicy(project)
}
func (e *Engine) SaveAgentPolicy(project string, p agentpolicy.Policy) (agentpolicy.Policy, error) {
	if _, err := e.Desk.Project(project); err != nil {
		return p, err
	}
	if err := p.Validate(); err != nil {
		return p, err
	}
	if p.DecisionApplication != "" {
		found := false
		for _, a := range e.Apps {
			if a.ID == p.DecisionApplication && a.DecisionModel != nil {
				if err := a.DecisionModel.Validate(); err != nil {
					return p, err
				}
				found = true
			}
		}
		if !found {
			return p, errors.New("select an installed application with a valid decision_model")
		}
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
	if err := s.commit("", "effect", "project_agent_policy_changed", change{AgentPolicy: &ProjectAgentPolicy{ProjectID: project, Policy: p}}); err != nil {
		return p, err
	}
	return graphClone(p), nil
}
