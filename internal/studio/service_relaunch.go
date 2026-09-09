package studio

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"mitos/internal/service"
)

type ServiceLifecycleCause struct {
	Seq    int64  `json:"seq"`
	Hash   string `json:"hash"`
	RootID string `json:"root_id"`
}
type serviceRelaunchBinding struct {
	Unit     service.Unit
	Work     Brief
	Deadline time.Time
	RunID    string
}
type ServiceRelaunchView struct {
	ServiceID string    `json:"service_id"`
	Deadline  time.Time `json:"deadline"`
	RunID     string    `json:"run_id,omitempty"`
}

func relaunchViews(state *serviceLifecycleState) []ServiceRelaunchView {
	var out []ServiceRelaunchView
	for _, b := range state.Relaunch {
		out = append(out, ServiceRelaunchView{ServiceID: b.Unit.ID, Deadline: b.Deadline, RunID: b.RunID})
	}
	return out
}
func restartEventKind(status string) string {
	switch status {
	case "starting":
		return "service_restart_starting"
	case "restarted":
		return "service_restart_completed"
	case "restart_cancelling":
		return "service_restart_cancelling"
	case "restart_failed":
		return "service_restart_failed"
	}
	return "invalid"
}
func lifecycleActionName(op ServiceLifecycleOperation) string {
	if op.Action == "restart" {
		return "restart"
	}
	return "stop"
}
func lifecycleClosed(status string) bool {
	return status == "resumed" || status == "restarted" || status == "superseded"
}
func validLifecycleTransition(from, to string) bool {
	switch to {
	case "stopped":
		return from == "stopping"
	case "starting":
		return from == "stopping"
	case "restart_cancelling":
		return from == "stopping" || from == "starting"
	case "restart_failed":
		return from == "restart_cancelling"
	case "restarted":
		return from == "starting"
	case "resumed":
		return from == "stopped" || from == "restart_failed"
	}
	return false
}
func (s *Store) planServiceRelaunch(op ServiceLifecycleOperation, at time.Time) ([]serviceRelaunchBinding, error) {
	active := activeServiceUnits(s.services)
	members := map[string]bool{}
	for _, m := range op.Members {
		members[m.ID] = true
	}
	ids := []string{}
	for id := range active {
		if members[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	out := []serviceRelaunchBinding{}
	for _, id := range ids {
		u := active[id]
		for _, dep := range u.DependsOn {
			if !members[dep] || active[dep].ID == "" {
				return nil, fmt.Errorf("restart member %s needs active prerequisite %s in the same restart group", id, dep)
			}
		}
		work := s.services.Work[id]
		deadline := at.Add(time.Duration(work.TimeoutMinutes) * time.Minute)
		if work.Scheduling.Deadline != nil && work.Scheduling.Deadline.Before(deadline) {
			deadline = *work.Scheduling.Deadline
		}
		out = append(out, serviceRelaunchBinding{Unit: u, Work: work, Deadline: deadline})
	}
	return out, nil
}
func (s *Store) relaunchCause(state *serviceLifecycleState) *ServiceLifecycleCause {
	return &ServiceLifecycleCause{Seq: state.Operation.Seq, Hash: state.Hash, RootID: state.Operation.RootID}
}
func validLifecycleCause(c *ServiceLifecycleCause, seq int64) bool {
	return c != nil && c.Seq > 0 && c.Seq < seq && len(c.Hash) == 64 && service.ValidID(c.RootID)
}
func (s *Store) relaunchCallAllowed(call *ServiceCall) bool {
	if call == nil || call.Lifecycle == nil {
		return false
	}
	state := s.serviceLifecycle[call.Lifecycle.Seq]
	if state == nil || state.Operation.Status != "starting" || !reflect.DeepEqual(call.Lifecycle, s.relaunchCause(state)) {
		return false
	}
	for seq := range s.serviceHolds[call.ServiceID] {
		if seq != state.Operation.Seq {
			return false
		}
	}
	for _, b := range state.Relaunch {
		if b.Unit.ID == call.ServiceID && b.RunID == "" {
			return true
		}
	}
	return false
}
func validateLifecycleCall(e Event, c change) error {
	call := c.ServiceCall
	if call == nil {
		return nil
	}
	if call.Lifecycle == nil {
		if call.Origin == "lifecycle" {
			return errors.New("lifecycle call has no source")
		}
		return nil
	}
	if !validLifecycleCause(call.Lifecycle, e.Seq) || call.Origin != "lifecycle" && call.Origin != "continuous" || c.Task == nil || !reflect.DeepEqual(c.Task.ServiceLifecycle, call.Lifecycle) || call.Cycle == nil || call.Cycle.ID != fmt.Sprintf("restart:%d:%s", call.Lifecycle.Seq, call.Lifecycle.Hash) {
		return errors.New("invalid restart receipt binding")
	}
	return nil
}
func (s *Store) trackLifecycleTask(e Event, c change, prior *Task) error {
	if c.Task == nil {
		return nil
	}
	if cause := c.Task.ServiceLifecycle; cause != nil && !validLifecycleCause(cause, e.Seq) {
		return errors.New("invalid Task lifecycle cause")
	}
	if prior != nil && !reflect.DeepEqual(prior.ServiceLifecycle, c.Task.ServiceLifecycle) {
		return errors.New("Task lifecycle cause changed")
	}
	if c.ServiceCall == nil || c.ServiceCall.Lifecycle == nil {
		return nil
	}
	if err := validateLifecycleCall(e, c); err != nil {
		return err
	}
	state := s.serviceLifecycle[c.ServiceCall.Lifecycle.Seq]
	// Independent per-run evidence folds do not include global lifecycle records.
	if state == nil {
		return nil
	}
	if !s.relaunchCallAllowed(c.ServiceCall) {
		return errors.New("restart receipt is outside the pending plan")
	}
	for i, b := range state.Relaunch {
		if b.Unit.ID != c.ServiceCall.ServiceID {
			continue
		}
		if c.ServiceCall.Cycle.Revision != state.Operation.Revision || !sameServiceDependencies(c.ServiceCall.Cycle.DependsOn, b.Unit.DependsOn) || c.Task.Scheduling.Deadline == nil || !c.Task.Scheduling.Deadline.Equal(b.Deadline) {
			return errors.New("restart receipt changed bound cycle/deadline")
		}
		state.Relaunch[i].RunID = e.RunID
		return nil
	}
	return errors.New("restart member missing")
}
func relaunchPolicyEqual(a, b service.Unit) bool {
	// Descriptions and activation/ordering explanations do not alter execution.
	a.Description = ""
	b.Description = ""
	a.Relations = service.Relations{}
	b.Relations = service.Relations{}
	return reflect.DeepEqual(a, b)
}
func (s *Store) validateRelaunchState(state *serviceLifecycleState, now time.Time) (bool, string) {
	active := activeServiceUnits(s.services)
	for _, b := range state.Relaunch {
		current, ok := active[b.Unit.ID]
		if !ok || !relaunchPolicyEqual(current, b.Unit) || !reflect.DeepEqual(s.services.Work[b.Unit.ID], b.Work) {
			return false, "Restart work, policy, or activation changed for " + b.Unit.ID + "."
		}
		if !now.Before(b.Deadline) {
			return false, "Restart deadline expired for " + b.Unit.ID + "."
		}
		if s.serviceAborted(b.Unit.ID) || !s.serviceBreakerAllows(b.Unit.ID, now) {
			return false, "Restart admission is withheld by the abort policy or circuit breaker for " + b.Unit.ID + "."
		}
		if b.RunID != "" {
			if run := s.runs[b.RunID]; run == nil || run.Status != "queued" {
				return false, "A prepared restart Task was cancelled or changed before group publication."
			}
		}
	}
	for _, m := range state.Operation.Members {
		for seq := range s.serviceHolds[m.ID] {
			if seq != state.Operation.Seq {
				return true, ""
			}
		}
	}
	return false, ""
}
func (s *Store) failRelaunch(state *serviceLifecycleState, detail string) error {
	op := state.Operation
	op.Status = "restart_cancelling"
	op.Detail = detail
	return s.commit("", "effect", "service_restart_cancelling", change{ServiceLifecycle: &op})
}

// Caller holds Engine.actions. The operation's hold remains until every receipt
// exists and the final publication Effect releases the group atomically.
func (e *Engine) reconcileServiceRelaunch() error {
	s := e.Store
	s.mu.RLock()
	ids := []int64{}
	for seq, state := range s.serviceLifecycle {
		if state.Operation.Status == "starting" {
			ids = append(ids, seq)
		}
	}
	s.mu.RUnlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	remaining := 64
	for _, seq := range ids {
		for remaining > 0 {
			s.mu.Lock()
			state := s.serviceLifecycle[seq]
			if state.Operation.Status != "starting" {
				s.mu.Unlock()
				break
			}
			wait, reason := s.validateRelaunchState(state, time.Now().UTC())
			if reason != "" {
				err := s.failRelaunch(state, reason)
				s.mu.Unlock()
				if err != nil {
					return err
				}
				break
			}
			if wait {
				s.mu.Unlock()
				break
			}
			index := -1
			for i, b := range state.Relaunch {
				if b.RunID == "" {
					index = i
					break
				}
			}
			if index < 0 {
				op := state.Operation
				op.Status = "restarted"
				err := s.commit("", "effect", "service_restart_completed", change{ServiceLifecycle: &op})
				s.mu.Unlock()
				if err != nil {
					return err
				}
				break
			}
			binding := state.Relaunch[index]
			catalogRevision := s.services.Revision
			s.mu.Unlock()
			brief, name, prepareErr := e.prepareBrief(binding.Work)
			s.mu.Lock()
			state = s.serviceLifecycle[seq]
			if s.services == nil || s.services.Revision != catalogRevision {
				s.mu.Unlock()
				continue
			}
			if prepareErr != nil {
				err := s.failRelaunch(state, fmt.Sprintf("Restart preparation for %s: %.1750s", binding.Unit.ID, prepareErr))
				s.mu.Unlock()
				if err != nil {
					return err
				}
				break
			}
			deadline := binding.Deadline
			brief.Scheduling.Deadline = &deadline
			call := &ServiceCall{ServiceID: binding.Unit.ID, Origin: "lifecycle", Revision: catalogRevision, Key: fmt.Sprintf("restart:%d:%s", seq, binding.Unit.ID), Lifecycle: s.relaunchCause(state)}
			call.Cycle = s.serviceCycleBinding(binding.Unit.ID, fmt.Sprintf("restart:%d:%s", seq, state.Hash), state.Operation.Revision)
			if binding.Unit.TriggerKind() == "continuous" {
				controller := s.serviceStates[binding.Unit.ID].Continuous
				if controller == nil {
					err := s.failRelaunch(state, "Continuous controller is unavailable.")
					s.mu.Unlock()
					if err != nil {
						return err
					}
					break
				}
				call.Origin = "continuous"
				call.ContinuousEpoch = controller.Epoch
				call.Key = fmt.Sprintf("%d:%d", controller.Epoch, controller.Generation+1)
			}
			_, err := s.createLocked(brief, name, "", false, call)
			if err != nil {
				if s.writeErr != nil {
					s.mu.Unlock()
					return err
				}
				err = s.failRelaunch(state, fmt.Sprintf("Restart intake for %s: %.1750s", binding.Unit.ID, err))
			}
			s.mu.Unlock()
			if err != nil {
				return err
			}
			remaining--
		}
		if remaining == 0 {
			return nil
		}
	}
	return nil
}
