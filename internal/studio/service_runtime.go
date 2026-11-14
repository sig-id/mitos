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

type ServiceTick struct {
	ServiceID  string    `json:"service_id"`
	Revision   int64     `json:"catalog_revision"`
	Epoch      int64     `json:"schedule_epoch"`
	DueAt      time.Time `json:"due_at"`
	ObservedAt time.Time `json:"observed_at"`
	NextAt     time.Time `json:"next_at"`
	Outcome    string    `json:"outcome"`
	Detail     string    `json:"detail,omitempty"`
	Startup    bool      `json:"startup,omitempty"`
}
type ServiceState struct {
	GraphID         string                   `json:"graph_id,omitempty"`
	LifecycleStops  []int64                  `json:"lifecycle_stops,omitempty"`
	Condition       *ServiceConditionState   `json:"condition,omitempty"`
	Health          []ServiceHealthView      `json:"health,omitempty"`
	Replacements    []ServiceReplacementView `json:"replacements,omitempty"`
	Priority        string                   `json:"priority"`
	Continuous      *ServiceContinuous       `json:"continuous,omitempty"`
	EventsPending   int                      `json:"events_pending"`
	EventsDelivered int64                    `json:"events_delivered"`
	EventsSkipped   int64                    `json:"events_skipped"`
	EventsRejected  int64                    `json:"events_rejected"`
	LastEvent       *ServiceEventDelivery    `json:"last_event,omitempty"`
	Abort           *ServiceAbort            `json:"abort,omitempty"`
	Retries         []ServiceRetryPlan       `json:"retries,omitempty"`
	Breaker         *service.Circuit         `json:"circuit_breaker,omitempty"`
	ServiceID       string                   `json:"service_id"`
	Epoch           int64                    `json:"schedule_epoch"`
	NextAt          *time.Time               `json:"next_at,omitempty"`
	LastTick        *ServiceTick             `json:"last_tick,omitempty"`
	Enqueued        int64                    `json:"enqueued"`
	Skipped         int64                    `json:"skipped"`
	Rejected        int64                    `json:"rejected"`
	LastRunID       string                   `json:"last_run_id,omitempty"`
	LastStatus      string                   `json:"last_status,omitempty"`
	Pending         int                      `json:"pending"`
	InFlight        int                      `json:"in_flight"`
}

func serviceCallIndex(id, key, origin string) string {
	if origin == "" {
		origin = "manual"
	}
	return id + "\x00" + origin + "\x00" + key
}
func activeServiceUnits(c *ServiceCatalog) map[string]service.Unit {
	out := map[string]service.Unit{}
	if c == nil {
		return out
	}
	resolution, _ := service.Resolve(c.Config)
	active := map[string]bool{}
	for _, row := range resolution.Rows {
		active[row.ID] = row.Kind == "unit" && row.State == "active"
	}
	for _, unit := range c.Config.Units {
		if active[unit.ID] {
			out[unit.ID] = unit
		}
	}
	return out
}

// Configuration and initial clocks are one event; replay uses the original event
// time. Unchanged active schedules retain their clock through unrelated edits.
func (s *Store) applyServiceCatalog(c *ServiceCatalog, event Event, clocks []ServiceClock) {
	saved := map[string]ServiceClock{}
	for _, clock := range clocks {
		saved[clock.ServiceID] = clock
	}
	oldUnits := activeServiceUnits(s.services)
	units := activeServiceUnits(c)
	if s.serviceStates == nil {
		s.serviceStates = map[string]*ServiceState{}
	}
	for id, state := range s.serviceStates {
		if _, ok := units[id]; !ok {
			state.NextAt = nil
		}
	}
	for _, u := range c.Config.Units {
		state := s.serviceStates[u.ID]
		if state == nil {
			state = &ServiceState{ServiceID: u.ID}
			s.serviceStates[u.ID] = state
		}
		_, active := units[u.ID]
		if !active || u.Schedule == nil || !u.Schedule.Timed() {
			state.NextAt = nil
			continue
		}
		if clock, ok := saved[u.ID]; ok {
			state.Epoch = clock.Epoch
			next := clock.NextAt
			state.NextAt = &next
			continue
		}
		old, wasActive := oldUnits[u.ID]
		if wasActive && state.NextAt != nil && reflect.DeepEqual(old.Schedule, u.Schedule) {
			continue
		}
		state.Epoch = event.Seq
		next, _ := u.Schedule.Next(event.At, u.ID, state.Epoch)
		if u.Schedule.RunOnStartup {
			next = event.At
		}
		state.NextAt = &next
	}
	s.configureContinuousServices(c, event)
	s.configureServiceRestarts(c)
	s.configureServiceBreakers(c, event.Seq)
	s.services = c
}
func validateServiceTick(event Event, tick *ServiceTick, created bool) error {
	if tick == nil || !service.ValidID(tick.ServiceID) || tick.Revision < 1 || tick.Revision >= event.Seq || tick.Epoch < 1 || tick.Epoch > tick.Revision || tick.DueAt.IsZero() || tick.ObservedAt.Before(tick.DueAt) || (!tick.NextAt.After(tick.ObservedAt) && !(tick.NextAt.IsZero() && tick.Outcome == "rejected")) || event.Type != "effect" || len(tick.Detail) > 2000 {
		return errors.New("invalid service schedule occurrence")
	}
	if created {
		if tick.Outcome != "enqueued" || event.Kind != "run_created" || event.RunID == "" {
			return errors.New("invalid enqueued service occurrence")
		}
	} else if event.Kind != "service_tick" || event.RunID != "" || (tick.Outcome != "skipped" && tick.Outcome != "rejected") {
		return errors.New("invalid skipped service occurrence")
	}
	return nil
}
func (s *Store) applyServiceTick(tick *ServiceTick, runID string) {
	if s.serviceStates == nil {
		s.serviceStates = map[string]*ServiceState{}
	}
	state := s.serviceStates[tick.ServiceID]
	if state == nil {
		state = &ServiceState{ServiceID: tick.ServiceID}
		s.serviceStates[tick.ServiceID] = state
	}
	state.Epoch = tick.Epoch
	next := tick.NextAt
	state.NextAt = &next
	if tick.NextAt.IsZero() {
		state.NextAt = nil
	}
	copy := *tick
	state.LastTick = &copy
	switch tick.Outcome {
	case "enqueued":
		state.Enqueued++
		state.LastRunID = runID
	case "skipped":
		state.Skipped++
	case "rejected":
		state.Rejected++
	}
}
func (s *Store) serviceCounts(id, excluding string, activeIDs []string) (pending, inFlight int) {
	active := map[string]bool{}
	for _, runID := range activeIDs {
		active[runID] = true
	}
	for _, task := range s.tasks {
		if task.Source != "trigger:"+id || task.ID == excluding {
			continue
		}
		r := s.runs[task.CurrentRunID]
		if r == nil || (r.Terminal() && !active[r.ID]) {
			continue
		}
		pending++
		if r.Status != "queued" || task.AdmittedRunID == r.ID || active[r.ID] {
			inFlight++
		}
	}
	pending += s.pendingServiceRetries(id) + s.pendingServiceEvents(id)
	return
}
func (s *Store) serviceConcurrency(id string) service.Concurrency {
	if s.services != nil {
		for _, u := range s.services.Config.Units {
			if u.ID == id {
				return u.Concurrency.Effective()
			}
		}
	}
	return (service.Concurrency{}).Effective()
}
func (s *Store) serviceCanEnqueue(id string) bool {
	if s.serviceLifecycleHeld(id) {
		return false
	}
	pending, _ := s.serviceCounts(id, "", nil)
	c := s.serviceConcurrency(id)
	limit := c.MaxInstances
	if c.IfRunning == "queue" {
		limit += c.QueueMax
	}
	return pending < limit || c.IfRunning == "kill" && pending == limit && s.selectServiceReplacement(id, "") != nil
}
func (s *Store) serviceCanDispatch(task *Task, activeIDs []string) bool {
	if s.lifecycleTaskHeld(task) {
		return false
	}
	if waiting, _ := s.serviceCycleReadiness(task); len(waiting) > 0 {
		return false
	}
	if !strings.HasPrefix(task.Source, "trigger:") {
		return true
	}
	id := strings.TrimPrefix(task.Source, "trigger:")
	_, inFlight := s.serviceCounts(id, task.ID, activeIDs)
	return s.serviceReplacementWait(task, activeIDs) == "" && s.continuousAllowsTask(task) && inFlight < s.serviceConcurrency(id).MaxInstances && s.serviceAbortAllowsTask(task) && s.serviceBreakerAllowsTask(task, activeIDs, time.Now().UTC())
}
func (s *Store) serviceRuntimeView() []ServiceState {
	out := []ServiceState{}
	if s.services == nil {
		return out
	}
	for _, unit := range s.services.Config.Units {
		state := ServiceState{ServiceID: unit.ID}
		if saved := s.serviceStates[unit.ID]; saved != nil {
			state = *saved
			state.Continuous = cloneContinuous(saved.Continuous)
			if saved.LastEvent != nil {
				copy := cloneEventDelivery(saved.LastEvent)
				state.LastEvent = &copy
			}
			if saved.Abort != nil {
				copy := *saved.Abort
				state.Abort = &copy
			}
			if saved.Breaker != nil {
				copy := *saved.Breaker
				if copy.OpenUntil != nil {
					until := *copy.OpenUntil
					copy.OpenUntil = &until
				}
				state.Breaker = &copy
			}
			if saved.NextAt != nil {
				next := *saved.NextAt
				state.NextAt = &next
			}
			if saved.LastTick != nil {
				tick := *saved.LastTick
				state.LastTick = &tick
			}
		}
		state.LifecycleStops = nil
		for seq := range s.serviceHolds[unit.ID] {
			state.LifecycleStops = append(state.LifecycleStops, seq)
		}
		sort.Slice(state.LifecycleStops, func(i, j int) bool { return state.LifecycleStops[i] < state.LifecycleStops[j] })
		state.Replacements = s.serviceReplacementViews(unit.ID)
		state.Priority = unit.Concurrency.Effective().Priority
		state.Retries = []ServiceRetryPlan{}
		for _, plan := range s.serviceRetryPlans {
			if plan.Binding.ServiceID == unit.ID && plan.Status == "pending" {
				state.Retries = append(state.Retries, *plan)
			}
		}
		sort.Slice(state.Retries, func(i, j int) bool { return state.Retries[i].ParentRunID < state.Retries[j].ParentRunID })
		state.EventsPending = s.pendingServiceEvents(unit.ID)
		state.Pending, state.InFlight = s.serviceCounts(unit.ID, "", nil)
		if run := s.runs[state.LastRunID]; run != nil {
			state.LastStatus = run.Status
			if task := s.tasks[run.TaskID]; task != nil {
				if latest := s.runs[task.CurrentRunID]; latest != nil {
					state.LastRunID = latest.ID
					state.LastStatus = latest.Status
				}
			}
		}
		state.Health = s.serviceHealthViews(unit.ID, state.LastRunID)
		if run := s.runs[state.LastRunID]; run != nil {
			state.Condition = cloneCondition(run.Condition)
			if creation, ok := s.graphCreations["service:"+run.ID]; ok {
				state.GraphID = creation.ID
			}
		}
		out = append(out, state)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ServiceID < out[j].ServiceID })
	return out
}

// Each due occurrence either advances the cursor with its creation Effect or
// records an explicit skip/rejection. A crash cannot leave a submitted Task with
// an unadvanced cursor. The single Store writer fences catalog edits and ticks.
func (e *Engine) scheduleServices(now time.Time, startup bool) error {
	e.actions.Lock()
	defer e.actions.Unlock()
	e.Store.mu.RLock()
	catalog := cloneCatalog(e.Store.services)
	e.Store.mu.RUnlock()
	units := activeServiceUnits(&catalog)
	ids := []string{}
	for id := range units {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if e.ctx != nil && e.ctx.Err() != nil {
			return nil
		}
		unit := units[id]
		if unit.Schedule == nil || !unit.Schedule.Timed() {
			continue
		}
		e.Store.mu.RLock()
		state := e.Store.serviceStates[id]
		if state == nil || state.NextAt == nil {
			e.Store.mu.RUnlock()
			continue
		}
		due, epoch := *state.NextAt, state.Epoch
		e.Store.mu.RUnlock()
		isStartup := startup && unit.Schedule.RunOnStartup
		if isStartup {
			due = now
		}
		if now.Before(due) {
			continue
		}
		next, nextErr := unit.Schedule.Next(now, id, epoch)
		tick := &ServiceTick{ServiceID: id, Revision: catalog.Revision, Epoch: epoch, DueAt: due, ObservedAt: now, NextAt: next, Startup: isStartup}
		// Validate outside the Store lock; recheck the catalog and cursor before commit.
		brief, name, admissionErr := e.prepareBrief(catalog.Work[id])
		e.Store.mu.Lock()
		current := e.Store.serviceStates[id]
		if e.Store.services == nil || e.Store.services.Revision != catalog.Revision || current == nil || current.Epoch != epoch || current.NextAt == nil || (!isStartup && !current.NextAt.Equal(due)) {
			e.Store.mu.Unlock()
			continue
		}
		if nextErr != nil {
			tick.Outcome = "rejected"
			tick.Detail = "Schedule paused: " + nextErr.Error()
		} else if e.Store.serviceLifecycleHeld(id) {
			tick.Outcome = "skipped"
			tick.Detail = "Service lifecycle is stopped."
		} else if e.Store.serviceAborted(id) {
			tick.Outcome = "skipped"
			tick.Detail = "The service abort policy is withholding new execution."
		} else if !e.Store.serviceBreakerAllows(id, now) {
			tick.Outcome = "skipped"
			tick.Detail = "Service circuit breaker is withholding new execution."
		} else if !e.Store.serviceCanEnqueue(id) {
			tick.Outcome = "skipped"
			tick.Detail = "Service concurrency or queue capacity is full."
		} else if admissionErr != nil {
			tick.Outcome = "rejected"
			tick.Detail = admissionErr.Error()
		} else if err := e.Store.validateTaskRequest("service-validation", brief.Scheduling, false); err != nil {
			tick.Outcome = "rejected"
			tick.Detail = err.Error()
		} else {
			tick.Outcome = "enqueued"
		}
		var err error
		if tick.Outcome == "enqueued" {
			key := fmt.Sprintf("%d:%d:%t", epoch, due.UnixNano(), isStartup)
			if unit.Trigger == "cron" {
				key = fmt.Sprintf("%d:%s:%t", epoch, due.UTC().Format(time.RFC3339Nano), isStartup)
			}
			call := &ServiceCall{ServiceID: id, Key: key, Revision: catalog.Revision, Origin: unit.Trigger, Tick: tick}
			_, err = e.Store.createLocked(brief, name, "", false, call)
		} else {
			if len(tick.Detail) > 2000 {
				tick.Detail = tick.Detail[:2000]
			}
			err = e.Store.commit("", "effect", "service_tick", change{ServiceTick: tick})
		}
		e.Store.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
