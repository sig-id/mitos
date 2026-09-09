package studio

import (
	"errors"
	"reflect"
	"strings"
	"time"

	"mitos/internal/service"
)

type ServiceBreakerEffect struct {
	ServiceID string                 `json:"service_id"`
	Kind      string                 `json:"kind"`
	Binding   service.BreakerBinding `json:"binding"`
	State     service.Circuit        `json:"state"`
}

func (s *Store) configureServiceBreakers(c *ServiceCatalog, seq int64) {
	configured := map[string]bool{}
	for _, unit := range c.Config.Units {
		configured[unit.ID] = true
		state := s.serviceStates[unit.ID]
		if state == nil {
			state = &ServiceState{ServiceID: unit.ID}
			s.serviceStates[unit.ID] = state
		}
		config := unit.CircuitBreaker.Effective()
		if !config.Enabled {
			state.Breaker = nil
			continue
		}
		if state.Breaker == nil || !reflect.DeepEqual(state.Breaker.Config, config) {
			fresh := service.NewCircuit(config, seq)
			state.Breaker = &fresh
		}
	}
	for id, state := range s.serviceStates {
		if !configured[id] {
			state.Breaker = nil
		}
	}
}
func (s *Store) serviceCircuit(id string) *service.Circuit {
	if state := s.serviceStates[id]; state != nil {
		return state.Breaker
	}
	return nil
}
func (s *Store) serviceBreakerAllows(id string, now time.Time) bool {
	c := s.serviceCircuit(id)
	return c == nil || c.Available(now)
}
func (s *Store) serviceBreakerAllowsTask(task *Task, activeIDs []string, now time.Time) bool {
	if !strings.HasPrefix(task.Source, "trigger:") {
		return true
	}
	// A checkpoint recovery continues its already admitted attempt and binding.
	if _, admitted := s.serviceAdmissions[task.CurrentRunID]; admitted {
		return true
	}
	id := strings.TrimPrefix(task.Source, "trigger:")
	c := s.serviceCircuit(id)
	if c == nil {
		return true
	}
	if !c.Available(now) {
		return false
	}
	if c.State == "open" {
		_, inFlight := s.serviceCounts(id, task.ID, activeIDs)
		if inFlight > 0 {
			return false
		}
	}
	return true
}

// Called under the Store lock before event serialization and fsync. The snapshot
// and binding are part of the admission/terminal Effect, never a later append.
func (s *Store) serviceBreakerChange(id, kind string, c change, now time.Time) (*ServiceBreakerEffect, error) {
	run := s.runs[id]
	if run == nil {
		return nil, nil
	}
	task := s.tasks[run.TaskID]
	if task == nil || !strings.HasPrefix(task.Source, "trigger:") {
		return nil, nil
	}
	serviceID := strings.TrimPrefix(task.Source, "trigger:")
	state := s.serviceCircuit(serviceID)
	if state == nil {
		return nil, nil
	}
	prior, admitted := s.serviceAdmissions[id]
	if kind == "claims_acquired" || kind == "task_scheduled" {
		if admitted {
			return nil, nil
		}
		if !s.serviceBreakerAllowsTask(task, nil, now) {
			return nil, errors.New("service circuit breaker is withholding new execution")
		}
		next, binding, err := state.Admit(now)
		if err != nil {
			return nil, err
		}
		return &ServiceBreakerEffect{ServiceID: serviceID, Kind: "admitted", Binding: binding, State: next}, nil
	}
	if admitted && !run.Terminal() && (c.Status == "done" || c.Status == "failed" || c.Status == "cancelled") {
		next, changed := state.Outcome(prior, c.Status, now)
		if changed {
			return &ServiceBreakerEffect{ServiceID: serviceID, Kind: c.Status, Binding: prior, State: next}, nil
		}
	}
	return nil, nil
}
func validateServiceBreakerEffect(e Event, c change) error {
	effect := c.ServiceBreaker
	if effect == nil || e.Type != "effect" || e.RunID == "" || (c.Task != nil && c.Task.Source != "trigger:"+effect.ServiceID) || !service.ValidID(effect.ServiceID) || effect.Binding.Epoch < 1 || effect.Binding.Generation < 1 || effect.State.Epoch != effect.Binding.Epoch || effect.State.Epoch >= e.Seq {
		return errors.New("invalid service circuit effect")
	}
	if err := effect.State.Validate(); err != nil {
		return err
	}
	switch effect.Kind {
	case "admitted":
		if (e.Kind != "claims_acquired" && e.Kind != "task_scheduled") || c.Task == nil || c.Task.AdmittedRunID != e.RunID || effect.State.Generation != effect.Binding.Generation {
			return errors.New("invalid service probe reservation")
		}
	case "done", "failed", "cancelled":
		if c.Status != effect.Kind {
			return errors.New("service circuit result mismatch")
		}
	default:
		return errors.New("unknown service circuit effect")
	}
	return nil
}
func (s *Store) applyServiceBreaker(e Event, c change) error {
	if c.ServiceBreaker == nil {
		return nil
	}
	if err := validateServiceBreakerEffect(e, c); err != nil {
		return err
	}
	effect := c.ServiceBreaker
	run := s.runs[e.RunID]
	if run == nil || s.tasks[run.TaskID] == nil || s.tasks[run.TaskID].Source != "trigger:"+effect.ServiceID {
		return errors.New("service circuit effect does not match its Task source")
	}
	if effect.Kind == "admitted" {
		if s.serviceAdmissions == nil {
			s.serviceAdmissions = map[string]service.BreakerBinding{}
		}
		if _, exists := s.serviceAdmissions[e.RunID]; exists {
			return errors.New("duplicate service circuit admission")
		}
		s.serviceAdmissions[e.RunID] = effect.Binding
	} else if prior, ok := s.serviceAdmissions[e.RunID]; !ok || prior != effect.Binding {
		return errors.New("service circuit result has no matching admission")
	}
	if s.serviceStates == nil {
		s.serviceStates = map[string]*ServiceState{}
	}
	state := s.serviceStates[effect.ServiceID]
	if state == nil {
		state = &ServiceState{ServiceID: effect.ServiceID}
		s.serviceStates[effect.ServiceID] = state
	}
	copy := effect.State
	state.Breaker = &copy
	return nil
}
