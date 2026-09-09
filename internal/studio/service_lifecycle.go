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

type ServiceLifecycleOperation struct {
	Action      string                    `json:"action,omitempty"`
	Replaces    int64                     `json:"replaces,omitempty"`
	CancelKey   string                    `json:"cancel_key,omitempty"`
	Detail      string                    `json:"detail,omitempty"`
	Seq         int64                     `json:"seq"`
	RootID      string                    `json:"root_id"`
	Key         string                    `json:"idempotency_key"`
	Revision    int64                     `json:"catalog_revision"`
	Members     []service.LifecycleMember `json:"members"`
	ActiveTasks []string                  `json:"active_tasks,omitempty"`
	Status      string                    `json:"status"`
	ResumeKey   string                    `json:"resume_key,omitempty"`
}
type serviceLifecycleState struct {
	Operation     ServiceLifecycleOperation
	At, UpdatedAt time.Time
	Hash          string
	Relaunch      []serviceRelaunchBinding
	Victims       []ServiceReplacement
}
type ServiceLifecycleView struct {
	ServiceLifecycleOperation
	At        time.Time             `json:"at"`
	UpdatedAt time.Time             `json:"updated_at"`
	TaskCount int                   `json:"task_count"`
	Tasks     []ServiceReplacement  `json:"tasks"`
	MoreTasks bool                  `json:"more_tasks"`
	Relaunch  []ServiceRelaunchView `json:"relaunch,omitempty"`
}

func validateServiceLifecycle(e Event, c change) error {
	op := c.ServiceLifecycle
	if op == nil {
		return nil
	}
	if e.RunID != "" || e.Type != "effect" || c.Run != nil || c.Task != nil || c.ServiceCall != nil || op.Seq < 1 || op.Seq > e.Seq || op.Revision < 1 || op.Revision >= op.Seq || !service.ValidID(op.RootID) || !validServiceKey(op.Key) || len(op.Members) < 1 || len(op.Members) > 256 || op.Members[0].ID != op.RootID || op.Members[0].Via != "" {
		return errors.New("invalid service lifecycle record")
	}
	seen := map[string]bool{}
	for i, m := range op.Members {
		if !service.ValidID(m.ID) || seen[m.ID] || i > 0 && !seen[m.Via] {
			return errors.New("invalid lifecycle propagation")
		}
		seen[m.ID] = true
	}
	if len(op.ActiveTasks) > 32 {
		return errors.New("too many active local tasks")
	}
	for _, id := range op.ActiveTasks {
		if id == "" || len(id) > 128 {
			return errors.New("invalid active Task witness")
		}
	}
	if op.Action != "" && op.Action != "restart" || len(op.Detail) > 2000 || op.Replaces < 0 || op.Replaces >= op.Seq || op.Action != "restart" && op.Replaces != 0 {
		return errors.New("invalid lifecycle action")
	}
	if op.CancelKey != "" && !validServiceKey(op.CancelKey) {
		return errors.New("invalid restart cancellation key")
	}
	switch op.Status {
	case "stopping":
		if (e.Kind != "service_stop_requested" || op.Action != "") && (e.Kind != "service_restart_requested" || op.Action != "restart") || op.Seq != e.Seq || op.ResumeKey != "" || op.CancelKey != "" || op.Detail != "" {
			return errors.New("invalid service stop request")
		}
	case "stopped":
		if e.Kind != "service_stop_settled" || op.Seq >= e.Seq || op.ResumeKey != "" {
			return errors.New("invalid service stop completion")
		}
	case "starting", "restarted", "restart_cancelling", "restart_failed":
		if op.Action != "restart" || op.Seq >= e.Seq || e.Kind != restartEventKind(op.Status) || op.ResumeKey != "" {
			return errors.New("invalid restart transition")
		}
	case "resumed":
		if e.Kind != "service_resumed" || op.Seq >= e.Seq || !validServiceKey(op.ResumeKey) {
			return errors.New("invalid service resume")
		}
	default:
		return errors.New("unknown service lifecycle state")
	}
	return nil
}
func lifecycleIdentity(root, action, key string) string { return root + "\x00" + action + "\x00" + key }
func (s *Store) serviceLifecycleHeld(id string) bool    { return len(s.serviceHolds[id]) > 0 }
func (s *Store) lifecycleTaskHeld(task *Task) bool {
	return task != nil && strings.HasPrefix(task.Source, "trigger:") && s.serviceLifecycleHeld(strings.TrimPrefix(task.Source, "trigger:"))
}
func (s *Store) applyServiceLifecycle(e Event, c change) error {
	if err := validateServiceLifecycle(e, c); err != nil {
		return err
	}
	op := *c.ServiceLifecycle
	if s.serviceLifecycle == nil {
		s.serviceLifecycle = map[int64]*serviceLifecycleState{}
		s.serviceLifecycleKeys = map[string]int64{}
		s.serviceHolds = map[string]map[int64]bool{}
	}
	previous := s.serviceLifecycle[op.Seq]
	if op.Status == "stopping" {
		if previous != nil || s.services == nil || s.services.Revision != op.Revision {
			return errors.New("invalid service stop catalog or identity")
		}
		members, err := service.LifecycleMembers(s.services.Config, op.RootID)
		if err != nil || !reflect.DeepEqual(members, op.Members) {
			return errors.New("stop propagation does not match catalog")
		}
		key := lifecycleIdentity(op.RootID, lifecycleActionName(op), op.Key)
		if s.serviceLifecycleKeys[key] != 0 {
			return errors.New("duplicate service stop key")
		}
		for _, state := range s.serviceLifecycle {
			if state.Operation.RootID == op.RootID && !lifecycleClosed(state.Operation.Status) && state.Operation.Seq != op.Replaces {
				return errors.New("service root already stopped")
			}
		}
		var replaced *serviceLifecycleState
		if op.Replaces != 0 {
			replaced = s.serviceLifecycle[op.Replaces]
			if replaced == nil || replaced.Operation.RootID != op.RootID || (replaced.Operation.Status != "stopped" && replaced.Operation.Status != "restart_failed") {
				return errors.New("invalid replaced stop")
			}
		}
		state := &serviceLifecycleState{Operation: op, Hash: e.Hash, At: e.At, UpdatedAt: e.At, Victims: []ServiceReplacement{}}
		if op.Action == "restart" {
			var err error
			state.Relaunch, err = s.planServiceRelaunch(op, e.At)
			if err != nil {
				return err
			}
		}
		if replaced != nil {
			replaced.Operation.Status = "superseded"
			replaced.UpdatedAt = e.At
			for _, m := range replaced.Operation.Members {
				delete(s.serviceHolds[m.ID], op.Replaces)
			}
		}
		memberSet := map[string]bool{}
		active := map[string]bool{}
		for _, m := range op.Members {
			memberSet[m.ID] = true
			if s.serviceHolds[m.ID] == nil {
				s.serviceHolds[m.ID] = map[int64]bool{}
			}
			s.serviceHolds[m.ID][op.Seq] = true
		}
		for _, id := range op.ActiveTasks {
			active[id] = true
		}
		held := map[string]bool{}
		for _, run := range s.runs {
			if run.Claim != nil && run.Claim.ReleasedAt == nil {
				held[run.TaskID] = true
			}
		}
		for _, task := range s.tasks {
			id := strings.TrimPrefix(task.Source, "trigger:")
			if task.Sample || !strings.HasPrefix(task.Source, "trigger:") || !memberSet[id] || terminalServiceTask(task) && !held[task.ID] && !active[task.ID] {
				continue
			}
			state.Victims = append(state.Victims, ServiceReplacement{ServiceID: id, Revision: op.Revision, TaskID: task.ID, RunID: task.CurrentRunID, Head: task.Head})
		}
		rank := map[string]int{}
		for i, m := range op.Members {
			rank[m.ID] = i
		}
		sort.Slice(state.Victims, func(i, j int) bool {
			a, b := state.Victims[i], state.Victims[j]
			if rank[a.ServiceID] != rank[b.ServiceID] {
				return rank[a.ServiceID] > rank[b.ServiceID]
			}
			return a.TaskID < b.TaskID
		})
		s.serviceLifecycle[op.Seq] = state
		s.serviceLifecycleKeys[key] = op.Seq
		return nil
	}
	if previous == nil {
		return errors.New("lifecycle update has no request")
	}
	original := op
	original.Status = previous.Operation.Status
	original.ResumeKey = previous.Operation.ResumeKey
	original.CancelKey = previous.Operation.CancelKey
	original.Detail = previous.Operation.Detail
	if !reflect.DeepEqual(original, previous.Operation) {
		return errors.New("lifecycle update changed original scope")
	}
	if !validLifecycleTransition(previous.Operation.Status, op.Status) {
		return errors.New("invalid lifecycle transition")
	}
	if op.CancelKey != previous.Operation.CancelKey {
		if op.Status != "restart_cancelling" || !validServiceKey(op.CancelKey) || previous.Operation.CancelKey != "" {
			return errors.New("invalid restart cancellation")
		}
		key := lifecycleIdentity(op.RootID, "stop", op.CancelKey)
		if s.serviceLifecycleKeys[key] != 0 {
			return errors.New("duplicate cancellation key")
		}
		s.serviceLifecycleKeys[key] = op.Seq
	}
	if op.Status == "restarted" {
		for _, m := range op.Members {
			delete(s.serviceHolds[m.ID], op.Seq)
		}
	}
	if op.Status == "resumed" {
		key := lifecycleIdentity(op.RootID, "resume", op.ResumeKey)
		if s.serviceLifecycleKeys[key] != 0 {
			return errors.New("duplicate service resume key")
		}
		s.serviceLifecycleKeys[key] = op.Seq
		for _, m := range op.Members {
			delete(s.serviceHolds[m.ID], op.Seq)
		}
	}
	previous.Operation = op
	previous.UpdatedAt = e.At
	return nil
}
func lifecycleView(state *serviceLifecycleState) ServiceLifecycleView {
	op := state.Operation
	op.Members = append([]service.LifecycleMember(nil), op.Members...)
	op.ActiveTasks = append([]string(nil), op.ActiveTasks...)
	return ServiceLifecycleView{ServiceLifecycleOperation: op, At: state.At, UpdatedAt: state.UpdatedAt, TaskCount: len(state.Victims), Tasks: append([]ServiceReplacement{}, state.Victims[:min(50, len(state.Victims))]...), MoreTasks: len(state.Victims) > 50, Relaunch: relaunchViews(state)}
}
func (s *Store) ServiceLifecycles() []ServiceLifecycleView {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ids := []int64{}
	for seq := range s.serviceLifecycle {
		ids = append(ids, seq)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	out := []ServiceLifecycleView{}
	for _, seq := range ids[:min(50, len(ids))] {
		out = append(out, lifecycleView(s.serviceLifecycle[seq]))
	}
	return out
}
func (s *Store) ServiceLifecycle(seq int64) (ServiceLifecycleView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if state := s.serviceLifecycle[seq]; state != nil {
		return lifecycleView(state), nil
	}
	return ServiceLifecycleView{}, ErrNotFound
}
func (e *Engine) StopService(root, key string) (ServiceLifecycleView, error) {
	return e.requestServiceLifecycle(root, key, false)
}
func (e *Engine) RestartService(root, key string) (ServiceLifecycleView, error) {
	return e.requestServiceLifecycle(root, key, true)
}
func (e *Engine) requestServiceLifecycle(root, key string, restart bool) (ServiceLifecycleView, error) {
	if !service.ValidID(root) || !validServiceKey(key) {
		return ServiceLifecycleView{}, errors.New("stop requires a service/target and bounded idempotency key")
	}
	e.actions.Lock()
	defer e.actions.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	e.Store.mu.Lock()
	defer e.Store.mu.Unlock()
	s := e.Store
	action := "stop"
	if restart {
		action = "restart"
	}
	if seq := s.serviceLifecycleKeys[lifecycleIdentity(root, action, key)]; seq != 0 {
		return lifecycleView(s.serviceLifecycle[seq]), nil
	}
	if s.services == nil {
		return ServiceLifecycleView{}, ErrNotFound
	}
	var replaces int64
	for _, state := range s.serviceLifecycle {
		if state.Operation.RootID != root || lifecycleClosed(state.Operation.Status) {
			continue
		}
		if !restart && state.Operation.Action == "restart" && (state.Operation.Status == "stopping" || state.Operation.Status == "starting") {
			op := state.Operation
			op.Status = "restart_cancelling"
			op.CancelKey = key
			op.Detail = "The operator cancelled this restart; the group will remain stopped."
			if err := s.commit("", "effect", "service_restart_cancelling", change{ServiceLifecycle: &op}); err != nil {
				return ServiceLifecycleView{}, err
			}
			return lifecycleView(state), nil
		}
		if restart && (state.Operation.Status == "stopped" || state.Operation.Status == "restart_failed") {
			replaces = state.Operation.Seq
			continue
		}
		return ServiceLifecycleView{}, fmt.Errorf("%w: this root already has an active lifecycle operation", ErrConflict)
	}
	members, err := service.LifecycleMembers(s.services.Config, root)
	if err != nil {
		return ServiceLifecycleView{}, fmt.Errorf("%w: %s", ErrNotFound, err)
	}
	op := &ServiceLifecycleOperation{Seq: s.seq + 1, RootID: root, Key: key, Revision: s.services.Revision, Members: members, Status: "stopping", Replaces: replaces}
	if restart {
		for _, m := range members {
			for seq := range s.serviceHolds[m.ID] {
				state := s.serviceLifecycle[seq]
				if seq != replaces && state.Operation.Action == "restart" {
					return ServiceLifecycleView{}, fmt.Errorf("%w: another restart holds member %s", ErrConflict, m.ID)
				}
			}
		}
		op.Action = "restart"
		if _, err := s.planServiceRelaunch(*op, time.Now().UTC()); err != nil {
			return ServiceLifecycleView{}, err
		}
	}
	for id := range e.cancel {
		if run := s.runs[id]; run != nil {
			op.ActiveTasks = append(op.ActiveTasks, run.TaskID)
		}
	}
	sort.Strings(op.ActiveTasks)
	kind := "service_stop_requested"
	if restart {
		kind = "service_restart_requested"
	}
	if err = s.commit("", "effect", kind, change{ServiceLifecycle: op}); err != nil {
		return ServiceLifecycleView{}, err
	}
	return lifecycleView(s.serviceLifecycle[op.Seq]), nil
}
func (e *Engine) ResumeService(root, key string) (ServiceLifecycleView, error) {
	if !service.ValidID(root) || !validServiceKey(key) {
		return ServiceLifecycleView{}, errors.New("resume requires a service/target and bounded idempotency key")
	}
	e.actions.Lock()
	defer e.actions.Unlock()
	e.Store.mu.Lock()
	defer e.Store.mu.Unlock()
	s := e.Store
	if seq := s.serviceLifecycleKeys[lifecycleIdentity(root, "resume", key)]; seq != 0 {
		return lifecycleView(s.serviceLifecycle[seq]), nil
	}
	for _, state := range s.serviceLifecycle {
		if state.Operation.RootID != root || lifecycleClosed(state.Operation.Status) {
			continue
		}
		if state.Operation.Status != "stopped" && state.Operation.Status != "restart_failed" {
			return ServiceLifecycleView{}, fmt.Errorf("%w: workers, claims, or deliveries are still settling", ErrConflict)
		}
		op := state.Operation
		op.Status = "resumed"
		op.ResumeKey = key
		if err := s.commit("", "effect", "service_resumed", change{ServiceLifecycle: &op}); err != nil {
			return ServiceLifecycleView{}, err
		}
		return lifecycleView(state), nil
	}
	return ServiceLifecycleView{}, ErrNotFound
}
func (e *Engine) reconcileServiceLifecycle() error {
	e.actions.Lock()
	defer e.actions.Unlock()
	e.Store.mu.RLock()
	ids := []int64{}
	for seq, state := range e.Store.serviceLifecycle {
		if state.Operation.Status == "stopping" || state.Operation.Status == "restart_cancelling" {
			ids = append(ids, seq)
		}
	}
	e.Store.mu.RUnlock()
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	remaining := 64
	for _, seq := range ids {
		e.Store.mu.Lock()
		state := e.Store.serviceLifecycle[seq]
		members := map[string]bool{}
		for _, m := range state.Operation.Members {
			members[m.ID] = true
		}
		keys := []string{}
		for key, d := range e.Store.serviceEventDeliveries {
			if members[d.Recipient.ServiceID] && d.Recipient.Status == "pending" {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		for _, key := range keys {
			if remaining == 0 {
				break
			}
			d := cloneEventDelivery(e.Store.serviceEventDeliveries[key])
			d.Recipient.Status = "suppressed"
			d.Recipient.Detail = "A service lifecycle stop suppressed this pending delivery."
			if err := e.Store.commit("", "effect", "service_event_delivery", change{ServiceDelivery: &d}); err != nil {
				e.Store.mu.Unlock()
				return err
			}
			remaining--
		}
		victims := append([]ServiceReplacement(nil), state.Victims...)
		if state.Operation.Status == "restart_cancelling" {
			for _, binding := range state.Relaunch {
				if run := e.Store.runs[binding.RunID]; run != nil {
					task := e.Store.tasks[run.TaskID]
					victims = append(victims, ServiceReplacement{TaskID: task.ID, RunID: task.CurrentRunID})
				}
			}
		}
		e.Store.mu.Unlock()
		for _, victim := range victims {
			if remaining == 0 {
				break
			}
			e.Store.mu.RLock()
			task := e.Store.tasks[victim.TaskID]
			runID := ""
			if task != nil && !terminalServiceTask(task) {
				if run := e.Store.runs[task.CurrentRunID]; run != nil && run.Status != "stopping" {
					runID = task.CurrentRunID
				}
			}
			e.Store.mu.RUnlock()
			if runID != "" {
				if err := e.cancelWork(runID, "A service lifecycle stop requested shutdown of this Task."); err != nil && !errors.Is(err, ErrConflict) {
					return err
				}
				remaining--
			}
		}
		e.mu.Lock()
		active := []string{}
		for id := range e.cancel {
			active = append(active, id)
		}
		e.Store.mu.Lock()
		settled := true
		for _, victim := range victims {
			if !e.Store.replacementSettled(&victim, active) {
				settled = false
				break
			}
		}
		for _, d := range e.Store.serviceEventDeliveries {
			if members[d.Recipient.ServiceID] && d.Recipient.Status == "pending" {
				settled = false
				break
			}
		}
		var err error
		if settled {
			op := state.Operation
			kind := "service_stop_settled"
			op.Status = "stopped"
			if state.Operation.Action == "restart" {
				op.Status = "starting"
				if state.Operation.Status == "restart_cancelling" {
					op.Status = "restart_failed"
				}
				kind = restartEventKind(op.Status)
			}
			err = e.Store.commit("", "effect", kind, change{ServiceLifecycle: &op})
		}
		e.Store.mu.Unlock()
		e.mu.Unlock()
		if err != nil {
			return err
		}
		if remaining == 0 {
			return nil
		}
	}
	return e.reconcileServiceRelaunch()
}
