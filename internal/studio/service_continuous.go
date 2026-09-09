package studio

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"mitos/internal/service"
)

// One controller owns one Task at a time, including all of that Task's retries.
// Its next due time is written with the terminal Task, never inferred on startup.
type ServiceContinuous struct {
	ServiceID  string     `json:"service_id"`
	Epoch      int64      `json:"epoch"`
	Generation int64      `json:"generation"`
	TaskID     string     `json:"task_id,omitempty"`
	Status     string     `json:"status"`
	NextAt     *time.Time `json:"next_at,omitempty"`
	Detail     string     `json:"detail,omitempty"`
}

func cloneContinuous(c *ServiceContinuous) *ServiceContinuous {
	if c == nil {
		return nil
	}
	copy := *c
	if c.NextAt != nil {
		at := *c.NextAt
		copy.NextAt = &at
	}
	return &copy
}
func terminalServiceTask(t *Task) bool {
	return t != nil && (t.Status == "done" || t.Status == "failed" || t.Status == "cancelled")
}
func (s *Store) configureContinuousServices(c *ServiceCatalog, event Event) {
	old := activeServiceUnits(s.services)
	active := activeServiceUnits(c)
	ids := map[string]bool{}
	for id, state := range s.serviceStates {
		if state.Continuous != nil {
			ids[id] = true
		}
	}
	for id, unit := range active {
		if unit.Trigger == "continuous" {
			ids[id] = true
		}
	}
	for id := range ids {
		state := s.serviceStates[id]
		if state == nil {
			state = &ServiceState{ServiceID: id}
			s.serviceStates[id] = state
		}
		unit, enabled := active[id]
		enabled = enabled && unit.Trigger == "continuous"
		previous, wasEnabled := old[id]
		wasEnabled = wasEnabled && previous.Trigger == "continuous"
		unchanged := enabled && wasEnabled && reflect.DeepEqual(previous.Schedule, unit.Schedule) && reflect.DeepEqual(s.services.Work[id], c.Work[id])
		if unchanged && state.Continuous != nil {
			continue
		}
		next := cloneContinuous(state.Continuous)
		if next == nil {
			next = &ServiceContinuous{ServiceID: id}
		}
		// A disabled controller has no recurring work, but retains its Task until stop
		// is recorded. Removal from the visible catalog cannot lose that obligation.
		if !enabled && next.Status == "disabled" {
			continue
		}
		next.Epoch = event.Seq
		next.NextAt = nil
		if task := s.tasks[next.TaskID]; task != nil && !terminalServiceTask(task) {
			next.Status = "retiring"
			next.Detail = "Stopping managed work after activation, schedule, or work binding changed."
		} else if enabled {
			at := event.At
			next.NextAt = &at
			next.Status = "waiting"
			next.Detail = "Desired continuous service is ready to start."
		} else {
			next.Status = "disabled"
			next.Detail = "Continuous service is disabled."
		}
		state.Continuous = next
	}
}
func (s *Store) continuousAllowsTask(task *Task) bool {
	if task == nil || task.ContinuousEpoch == 0 {
		return true
	}
	id := strings.TrimPrefix(task.Source, "trigger:")
	state := s.serviceStates[id]
	return state != nil && state.Continuous != nil && state.Continuous.TaskID == task.ID && state.Continuous.Epoch == task.ContinuousEpoch && state.Continuous.Status == "running"
}
func (s *Store) continuousChange(id string, c change, now time.Time) *ServiceContinuous {
	if c.Run != nil && c.ServiceCall != nil && c.ServiceCall.Origin == "continuous" {
		saved := s.serviceStates[c.ServiceCall.ServiceID]
		if saved == nil || saved.Continuous == nil {
			return nil
		}
		next := cloneContinuous(saved.Continuous)
		next.Generation++
		next.TaskID = c.Run.TaskID
		next.Status = "running"
		next.NextAt = nil
		next.Detail = "A Task receipt owns the current continuous generation."
		return next
	}
	task := c.Task
	if !terminalServiceTask(task) || task.ContinuousEpoch == 0 {
		return nil
	}
	prior := s.tasks[task.ID]
	if prior == nil || prior.CurrentRunID != id || terminalServiceTask(prior) {
		return nil
	}
	serviceID := strings.TrimPrefix(task.Source, "trigger:")
	state := s.serviceStates[serviceID]
	if state == nil || state.Continuous == nil || state.Continuous.TaskID != task.ID {
		return nil
	}
	next := cloneContinuous(state.Continuous)
	next.NextAt = nil
	unit, enabled := activeServiceUnits(s.services)[serviceID]
	if enabled && unit.Trigger == "continuous" {
		at := now.Add(unit.Schedule.ContinuousDelay())
		next.NextAt = &at
		next.Status = "waiting"
		next.Detail = "Task finished; waiting for the saved restart delay."
	} else {
		next.Status = "disabled"
		next.Detail = "Managed Task stopped; continuous service is disabled."
	}
	return next
}
func validateContinuous(e Event, c change) error {
	next := c.ServiceContinuous
	if next == nil {
		return nil
	}
	if e.Type != "effect" || !service.ValidID(next.ServiceID) || next.Epoch < 1 || next.Epoch >= e.Seq || next.Generation < 0 || len(next.Detail) > 2000 {
		return errors.New("invalid continuous controller state")
	}
	switch next.Status {
	case "running":
		if c.Run == nil || c.ServiceCall == nil || c.ServiceCall.Origin != "continuous" || c.ServiceCall.ServiceID != next.ServiceID || c.ServiceCall.ContinuousEpoch != next.Epoch || c.ServiceCall.Key != fmt.Sprintf("%d:%d", next.Epoch, next.Generation) || next.TaskID != c.Run.TaskID || next.NextAt != nil || next.Generation < 1 {
			return errors.New("continuous receipt is not atomically bound")
		}
	case "waiting", "disabled":
		if !terminalServiceTask(c.Task) || c.Task.ID != next.TaskID || c.Task.Source != "trigger:"+next.ServiceID || c.Task.ContinuousEpoch == 0 || e.RunID != c.Task.CurrentRunID {
			return errors.New("continuous restart is not bound to Task completion")
		}
		if next.Status == "waiting" && (next.NextAt == nil || next.NextAt.Before(e.At)) || next.Status == "disabled" && next.NextAt != nil {
			return errors.New("invalid continuous restart time")
		}
	case "blocked", "rejected":
		if e.RunID != "" || e.Kind != "service_continuous_status" || c.Run != nil || c.Task != nil || c.ServiceCall != nil || next.Status == "blocked" && next.NextAt == nil || next.Status == "rejected" && next.NextAt != nil {
			return errors.New("invalid continuous admission outcome")
		}
	default:
		return errors.New("invalid continuous state transition")
	}
	return nil
}
func (s *Store) applyContinuous(e Event, c change) error {
	if c.ServiceContinuous == nil {
		return nil
	}
	if err := validateContinuous(e, c); err != nil {
		return err
	}
	next := c.ServiceContinuous
	if s.serviceStates == nil {
		s.serviceStates = map[string]*ServiceState{}
	}
	state := s.serviceStates[next.ServiceID]
	if state == nil {
		state = &ServiceState{ServiceID: next.ServiceID}
		s.serviceStates[next.ServiceID] = state
	}
	state.Continuous = cloneContinuous(next)
	return nil
}
func (e *Engine) reconcileContinuousServices(now time.Time) error {
	e.actions.Lock()
	defer e.actions.Unlock()
	e.Store.mu.RLock()
	catalog := cloneCatalog(e.Store.services)
	snapshots := []*ServiceContinuous{}
	for _, state := range e.Store.serviceStates {
		if state.Continuous != nil {
			snapshots = append(snapshots, cloneContinuous(state.Continuous))
		}
	}
	e.Store.mu.RUnlock()
	units := activeServiceUnits(&catalog)
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].ServiceID < snapshots[j].ServiceID })
	for _, saved := range snapshots {
		if e.ctx != nil && e.ctx.Err() != nil {
			return nil
		}
		// Recheck under the Store lock: catalog updates do not take actions.
		e.Store.mu.RLock()
		current := e.Store.serviceStates[saved.ServiceID]
		valid := e.Store.services != nil && e.Store.services.Revision == catalog.Revision && current != nil && reflect.DeepEqual(current.Continuous, saved)
		var task Task
		if t := e.Store.tasks[saved.TaskID]; t != nil {
			task = cloneTask(t)
		}
		e.Store.mu.RUnlock()
		if !valid {
			continue
		}
		if saved.Status == "retiring" {
			if task.ID != "" && !terminalServiceTask(&task) {
				if err := e.cancelWork(task.CurrentRunID, "Continuous service configuration stopped this managed Task."); err != nil && !errors.Is(err, ErrConflict) {
					return err
				}
			}
			continue
		}
		unit, enabled := units[saved.ServiceID]
		if !enabled || unit.Trigger != "continuous" || saved.NextAt == nil || now.Before(*saved.NextAt) || task.ID != "" && !terminalServiceTask(&task) {
			continue
		}
		e.Store.mu.RLock()
		reason := ""
		if e.Store.serviceAborted(saved.ServiceID) {
			reason = "Service abort policy is withholding continuous work."
		} else if !e.Store.serviceBreakerAllows(saved.ServiceID, now) {
			reason = "Service circuit breaker is withholding continuous work."
		} else if !e.Store.serviceCanEnqueue(saved.ServiceID) {
			reason = "Service concurrency or queue capacity is full."
		}
		e.Store.mu.RUnlock()
		var brief Brief
		var name string
		var admissionErr error
		if reason == "" {
			brief, name, admissionErr = e.prepareBrief(catalog.Work[saved.ServiceID])
		}
		e.Store.mu.Lock()
		current = e.Store.serviceStates[saved.ServiceID]
		if e.Store.services == nil || e.Store.services.Revision != catalog.Revision || current == nil || !reflect.DeepEqual(current.Continuous, saved) {
			e.Store.mu.Unlock()
			continue
		}
		// A source outcome can reserve capacity while prepareBrief is running.
		if reason == "" && (!e.Store.serviceCanEnqueue(saved.ServiceID) || e.Store.serviceAborted(saved.ServiceID) || !e.Store.serviceBreakerAllows(saved.ServiceID, now)) {
			reason = "Service admission is withholding continuous work."
		}
		if reason == "" && admissionErr == nil {
			admissionErr = e.Store.validateTaskRequest("continuous-validation", brief.Scheduling, false)
		}
		var err error
		if reason != "" || admissionErr != nil {
			next := cloneContinuous(saved)
			next.Status = "blocked"
			next.Detail = reason
			if admissionErr != nil {
				next.Status = "rejected"
				next.NextAt = nil
				next.Detail = fmt.Sprintf("Continuous admission paused: %.1800s", admissionErr.Error())
			}
			if !reflect.DeepEqual(next, saved) {
				err = e.Store.commit("", "effect", "service_continuous_status", change{ServiceContinuous: next})
			}
		} else {
			call := &ServiceCall{ServiceID: saved.ServiceID, Origin: "continuous", Revision: catalog.Revision, ContinuousEpoch: saved.Epoch, Key: fmt.Sprintf("%d:%d", saved.Epoch, saved.Generation+1)}
			call.Cycle = e.Store.serviceCycleBinding(saved.ServiceID, tickCycle(now), catalog.Revision)
			_, err = e.Store.createLocked(brief, name, "", false, call)
		}
		e.Store.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
