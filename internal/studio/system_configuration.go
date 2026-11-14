package studio

import (
	"errors"
	"mitos/internal/agentcli"
	"mitos/internal/decisionmodel"
)

// SystemConfiguration holds operator defaults for this Mitos server.
// Credentials are environment-variable references, never secret values.
type SystemConfiguration struct {
	Revision            int                                `json:"revision"`
	DecisionProvider    *decisionmodel.Config              `json:"decision_provider,omitempty"`
	DecisionApplication string                             `json:"decision_application,omitempty"`
	Agents              map[string]agentcli.LaunchDefaults `json:"agents"`
}

func (s *Store) SystemConfiguration() SystemConfiguration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return graphClone(s.systemConfiguration)
}

// ConfigurationSnapshot reads defaults and project overrides at one revision.
func (s *Store) ConfigurationSnapshot(project string) (SystemConfiguration, SystemConfiguration) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	global := graphClone(s.systemConfiguration)
	if project == "" {
		return graphClone(global), global
	}
	p := s.projectAgentPolicy(project)
	return SystemConfiguration{Revision: p.Revision, DecisionProvider: p.DecisionProvider, DecisionApplication: p.DecisionApplication, Agents: p.AgentLaunch}, global
}

func (e *Engine) validateConfiguration(c SystemConfiguration) error {
	if c.Revision < 0 || len(c.Agents) > 64 || c.DecisionProvider != nil && c.DecisionApplication != "" {
		return errors.New("invalid system configuration")
	}
	if c.DecisionProvider != nil {
		if err := c.DecisionProvider.Validate(); err != nil {
			return err
		}
	}
	if c.DecisionApplication != "" {
		if _, err := e.decisionConfig(c.DecisionApplication); err != nil {
			return err
		}
	}
	for id, defaults := range c.Agents {
		if err := defaults.Validate(); err != nil {
			return err
		}
		if _, err := e.Desk.Agents.Inspect(id, defaults.ProfileID, e.Root); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) SaveSystemConfiguration(c SystemConfiguration) (SystemConfiguration, error) {
	if err := e.validateConfiguration(c); err != nil {
		return c, err
	}
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Revision != s.systemConfiguration.Revision {
		return c, ErrConflict
	}
	c.Revision++
	if err := s.commit("", "effect", "system_configuration_changed", change{SystemConfiguration: &c}); err != nil {
		return c, err
	}
	return graphClone(c), nil
}

func (s *Store) AgentLaunchDefaults(project, agent string) agentcli.LaunchDefaults {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if p, ok := s.agentPolicies[project]; ok {
		if d, ok := p.AgentLaunch[agent]; ok {
			return graphClone(d)
		}
	}
	return graphClone(s.systemConfiguration.Agents[agent])
}
