package studio

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"mitos/internal/service"
)

type ServiceRestartBinding struct {
	Health    *service.Health      `json:"health,omitempty"`
	ServiceID string               `json:"service_id"`
	RootRunID string               `json:"root_run_id"`
	Attempt   int                  `json:"retry_attempt"`
	Deadline  time.Time            `json:"deadline"`
	Policy    service.RestartRules `json:"policy"`
}
type ServiceRetryPlan struct {
	Cause       string                `json:"cause,omitempty"`
	Binding     ServiceRestartBinding `json:"binding"`
	ParentRunID string                `json:"parent_run_id"`
	ChildRunID  string                `json:"child_run_id,omitempty"`
	DueAt       time.Time             `json:"due_at"`
	Status      string                `json:"status"`
	Detail      string                `json:"detail,omitempty"`
}
type ServiceAbort struct {
	RunID  string               `json:"run_id"`
	At     time.Time            `json:"at"`
	Policy service.RestartRules `json:"policy"`
}

func (s *Store) serviceRestartRules(id string) service.RestartRules {
	if s.services != nil {
		for _, u := range s.services.Config.Units {
			if u.ID == id {
				return u.RestartPolicy.Effective()
			}
		}
	}
	return (service.RestartPolicy{}).Effective()
}
func (s *Store) serviceAborted(id string) bool {
	state := s.serviceStates[id]
	return state != nil && state.Abort != nil
}
func (s *Store) serviceAbortAllowsTask(task *Task) bool {
	if !strings.HasPrefix(task.Source, "trigger:") {
		return true
	}
	// Recovery of an admitted attempt is still governed by its original decision.
	if task.AdmittedRunID == task.CurrentRunID {
		return true
	}
	return !s.serviceAborted(strings.TrimPrefix(task.Source, "trigger:"))
}
func (s *Store) configureServiceRestarts(c *ServiceCatalog) {
	previous := activeServiceUnits(s.services)
	active := activeServiceUnits(c)
	units := map[string]service.Unit{}
	for _, u := range c.Config.Units {
		units[u.ID] = u
	}
	for id, state := range s.serviceStates {
		if state.Abort == nil {
			continue
		}
		unit, exists := units[id]
		_, enabled := active[id]
		_, wasEnabled := previous[id]
		if !exists || unit.RestartPolicy.Effective() != state.Abort.Policy || enabled && !wasEnabled {
			state.Abort = nil
		}
	}
}
func (s *Store) pendingServiceRetries(id string) int {
	count := 0
	for _, p := range s.serviceRetryPlans {
		if p.Binding.ServiceID != id || p.Status != "pending" {
			continue
		}
		r := s.runs[p.ParentRunID]
		if r == nil {
			continue
		}
		t := s.tasks[r.TaskID]
		if t != nil && t.CurrentRunID == r.ID {
			count++
		}
	}
	return count
}

// Binding and failure policy decisions are attached to existing Effects.
func (s *Store) serviceRestartChange(id, kind string, c change, now time.Time) (*ServiceRestartBinding, *ServiceRetryPlan) {
	run := s.runs[id]
	if run == nil {
		return nil, nil
	}
	task := s.tasks[run.TaskID]
	if task == nil || !strings.HasPrefix(task.Source, "trigger:") {
		return nil, nil
	}
	serviceID := strings.TrimPrefix(task.Source, "trigger:")
	bound := s.serviceRestartBindings[id]
	if (kind == "claims_acquired" || kind == "task_scheduled") && bound == nil {
		binding := ServiceRestartBinding{ServiceID: serviceID, RootRunID: id, Deadline: now.Add(time.Duration(run.Brief.TimeoutMinutes) * time.Minute), Policy: s.serviceRestartRules(serviceID)}
		if plan := s.serviceRetryChildren[id]; plan != nil {
			binding = plan.Binding
		}
		binding.Health = s.serviceHealthConfig(serviceID)
		if binding.Policy.OnFailure == "skip" && (binding.Health == nil || binding.Health.OnUnhealthy != "restart") && s.serviceRetryChildren[id] == nil {
			return nil, nil
		}
		if d := run.Brief.Scheduling.Deadline; d != nil && d.Before(binding.Deadline) {
			binding.Deadline = *d
		}
		return &binding, nil
	}
	if bound == nil || run.Terminal() || c.Status != "failed" || task.CurrentRunID != id {
		return nil, nil
	}
	if bound.Policy.OnFailure != "abort" && (kind == "service_health_killed" || bound.Policy.OnFailure == "skip" && kind != "service_health_restart") {
		return nil, nil
	}
	plan := &ServiceRetryPlan{Binding: *bound, ParentRunID: id, DueAt: now, Status: "exhausted", Detail: "The configured service retry limit was reached."}
	if kind == "service_health_restart" {
		plan.Cause = "health"
	}
	if s.serviceRestartRules(serviceID) != bound.Policy || plan.Cause == "health" && !healthPoliciesEqual(s.serviceHealthConfig(serviceID), bound.Health) {
		plan.Status = "suppressed"
		plan.Detail = "Restart policy changed after this attempt was admitted."
		return nil, plan
	}
	if bound.Policy.OnFailure == "abort" {
		plan.Status = "aborted"
		plan.Detail = "The service stopped admitting new work after failure."
		return nil, plan
	}
	if bound.Attempt >= bound.Policy.MaxRetries {
		return nil, plan
	}
	plan.Binding.Attempt++
	plan.DueAt = now.Add(bound.Policy.Delay(plan.Binding.Attempt))
	plan.Status = "pending"
	plan.Detail = "Waiting for the saved service retry backoff."
	if !plan.DueAt.Before(bound.Deadline) {
		plan.Status = "expired"
		plan.Detail = "Backoff would reach the original service retry deadline."
	}
	return nil, plan
}
func validateRestartBinding(b *ServiceRestartBinding) error {
	if b == nil || !service.ValidID(b.ServiceID) || b.RootRunID == "" || b.Attempt < 0 || b.Attempt > b.Policy.MaxRetries || b.Deadline.IsZero() {
		return errors.New("invalid service restart binding")
	}
	if b.Health != nil {
		if err := b.Health.Validate(); err != nil {
			return err
		}
	}
	return b.Policy.Validate()
}
func validateServiceRetryEvent(e Event, c change) error {
	if c.ServiceRestart != nil {
		if e.Type != "effect" || (e.Kind != "claims_acquired" && e.Kind != "task_scheduled") || c.Task == nil || c.Task.Source != "trigger:"+c.ServiceRestart.ServiceID {
			return errors.New("invalid service restart admission")
		}
		if err := validateRestartBinding(c.ServiceRestart); err != nil {
			return err
		}
	}
	if p := c.ServiceRetry; p != nil {
		if p.Cause != "" && p.Cause != "health" {
			return errors.New("invalid service retry cause")
		}
		if e.Type != "effect" || e.RunID == "" || p.ParentRunID == "" || p.DueAt.IsZero() || len(p.Detail) > 2000 {
			return errors.New("invalid service retry event")
		}
		if err := validateRestartBinding(&p.Binding); err != nil {
			return err
		}
		switch p.Status {
		case "pending", "exhausted", "aborted":
			if e.RunID != p.ParentRunID || c.Status != "failed" {
				return errors.New("retry policy must be recorded with failure")
			}
		case "started":
			if c.Run == nil || c.Run.ParentID != p.ParentRunID || c.Run.ID != p.ChildRunID || e.RunID != p.ChildRunID || c.Task == nil || c.Task.Source != "trigger:"+p.Binding.ServiceID {
				return errors.New("retry receipt not bound to parent")
			}
		case "suppressed", "rejected", "expired", "cancelled":
			if e.RunID != p.ParentRunID || (c.Status != "failed" && e.Kind != "service_retry_closed") {
				return errors.New("invalid closed retry plan")
			}
		default:
			return errors.New("unknown retry plan state")
		}
	}
	return nil
}
func (s *Store) applyServiceRestart(e Event, c change) error {
	if c.ServiceRestart == nil && c.ServiceRetry == nil {
		return nil
	}
	if err := validateServiceRetryEvent(e, c); err != nil {
		return err
	}
	if c.ServiceRestart != nil {
		if s.serviceRestartBindings == nil {
			s.serviceRestartBindings = map[string]*ServiceRestartBinding{}
		}
		if s.serviceRestartBindings[e.RunID] != nil {
			return errors.New("duplicate service restart admission")
		}
		copy := *c.ServiceRestart
		s.serviceRestartBindings[e.RunID] = &copy
	}
	if p := c.ServiceRetry; p != nil {
		if s.serviceRetryPlans == nil {
			s.serviceRetryPlans = map[string]*ServiceRetryPlan{}
		}
		copy := *p
		s.serviceRetryPlans[p.ParentRunID] = &copy
		if p.Status == "started" {
			if s.serviceRetryChildren == nil {
				s.serviceRetryChildren = map[string]*ServiceRetryPlan{}
			}
			s.serviceRetryChildren[p.ChildRunID] = &copy
		}
		if p.Status == "aborted" {
			if s.serviceStates == nil {
				s.serviceStates = map[string]*ServiceState{}
			}
			state := s.serviceStates[p.Binding.ServiceID]
			if state == nil {
				state = &ServiceState{ServiceID: p.Binding.ServiceID}
				s.serviceStates[p.Binding.ServiceID] = state
			}
			state.Abort = &ServiceAbort{RunID: e.RunID, At: e.At, Policy: p.Binding.Policy}
		}
	}
	return nil
}
func (e *Engine) reconcileServiceRetries(now time.Time) error {
	e.actions.Lock()
	defer e.actions.Unlock()
	e.Store.mu.RLock()
	plans := []ServiceRetryPlan{}
	for _, p := range e.Store.serviceRetryPlans {
		if p.Status == "pending" {
			plans = append(plans, *p)
		}
	}
	e.Store.mu.RUnlock()
	sort.Slice(plans, func(i, j int) bool {
		if !plans[i].DueAt.Equal(plans[j].DueAt) {
			return plans[i].DueAt.Before(plans[j].DueAt)
		}
		return plans[i].ParentRunID < plans[j].ParentRunID
	})
	for _, plan := range plans {
		if e.ctx != nil && e.ctx.Err() != nil {
			return nil
		}
		e.Store.mu.Lock()
		current := e.Store.serviceRetryPlans[plan.ParentRunID]
		if current == nil || current.Status != "pending" {
			e.Store.mu.Unlock()
			continue
		}
		parent := e.Store.runs[plan.ParentRunID]
		task := e.Store.tasks[parent.TaskID]
		units := activeServiceUnits(e.Store.services)
		unit, active := units[plan.Binding.ServiceID]
		reason, status := "", ""
		if task.CurrentRunID != parent.ID {
			reason = "Another retry already replaced this attempt."
			status = "suppressed"
		} else if !active || unit.RestartPolicy.Effective() != plan.Binding.Policy || e.Store.serviceAborted(plan.Binding.ServiceID) || plan.Cause == "health" && !healthPoliciesEqual(e.Store.serviceHealthConfig(plan.Binding.ServiceID), plan.Binding.Health) {
			reason = "Service activation or restart policy changed."
			status = "suppressed"
		} else if !now.Before(plan.Binding.Deadline) {
			reason = "The original service retry deadline expired."
			status = "expired"
		}
		if status != "" {
			plan.Status = status
			plan.Detail = reason
			err := e.Store.commit(parent.ID, "effect", "service_retry_closed", change{ServiceRetry: &plan})
			e.Store.mu.Unlock()
			if err != nil {
				return err
			}
			continue
		}
		if now.Before(plan.DueAt) {
			e.Store.mu.Unlock()
			continue
		}
		// A pending retry already reserves an outstanding service slot. Breaker and
		// concurrency admission are rechecked by the ordinary Task dispatcher.
		brief := parent.Brief
		deadline := plan.Binding.Deadline
		brief.Scheduling.Deadline = &deadline
		revision := e.Store.services.Revision
		e.Store.mu.Unlock()
		prepared, name, err := e.prepareBrief(brief)
		e.Store.mu.Lock()
		current = e.Store.serviceRetryPlans[plan.ParentRunID]
		if current == nil || current.Status != "pending" || e.Store.services.Revision != revision || e.Store.tasks[parent.TaskID].CurrentRunID != parent.ID {
			e.Store.mu.Unlock()
			continue
		}
		if err == nil {
			err = e.Store.validateTaskRequest(task.ID, prepared.Scheduling, false)
		}
		if err != nil {
			plan.Status = "rejected"
			plan.Detail = fmt.Sprintf("Retry admission: %.1900s", err.Error())
			err = e.Store.commit(parent.ID, "effect", "service_retry_closed", change{ServiceRetry: &plan})
		} else {
			plan.Status = "started"
			plan.Detail = "The saved backoff elapsed and a retry receipt was created."
			_, err = e.Store.createWithRetryLocked(prepared, name, parent.ID, false, nil, &plan)
		}
		e.Store.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) serviceRetryTaskChange(id string, t *Task, effect string, plan *ServiceRetryPlan, now time.Time) (*Task, string) {
	run := s.runs[id]
	if run == nil {
		return t, effect
	}
	latest := s.tasks[run.TaskID]
	if latest == nil || latest.CurrentRunID != id {
		return t, effect
	}
	if plan.Status == "pending" {
		if t == nil {
			copy := cloneTask(latest)
			t = &copy
		}
		t.Status = "blocked"
		t.WaitingFor = []string{"service-retry:" + id}
		t.UpdatedAt = now
		return t, "task_blocked"
	}
	if plan.Status != "started" && latest.Status == "blocked" && len(latest.WaitingFor) == 1 && latest.WaitingFor[0] == "service-retry:"+id {
		if t == nil {
			copy := cloneTask(latest)
			t = &copy
		}
		t.Status = "failed"
		if plan.Status == "cancelled" {
			t.Status = "cancelled"
		}
		t.WaitingFor = nil
		t.UpdatedAt = now
		return t, "task_" + t.Status
	}
	return t, effect
}

func (s *Store) cancelServiceRetry(id, reason string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plan := s.serviceRetryPlans[id]
	if plan == nil {
		return false, nil
	}
	if plan.Status == "cancelled" {
		return true, nil
	}
	run := s.runs[id]
	if plan.Status != "pending" || run == nil || s.tasks[run.TaskID].CurrentRunID != id {
		return false, nil
	}
	copy := *plan
	copy.Status = "cancelled"
	copy.Detail = reason
	return true, s.commit(id, "effect", "service_retry_closed", change{ServiceRetry: &copy})
}
