package studio

import (
	"errors"
	"sort"
	"strings"
	"time"

	"mitos/internal/service"
)

// A replacement captures a Task/attempt witness. It never authorizes stopping
// an unrelated later Task that happens to use the same service ID.
type ServiceReplacement struct {
	ServiceID string   `json:"service_id"`
	Revision  int64    `json:"catalog_revision"`
	TaskID    string   `json:"task_id"`
	RunID     string   `json:"run_id"`
	Head      TaskHead `json:"head"`
}
type ServiceReplacementView struct {
	TaskID   string             `json:"task_id"`
	RunID    string             `json:"run_id"`
	Previous ServiceReplacement `json:"previous"`
	Waiting  bool               `json:"waiting"`
}

func validateReplacement(b *ServiceReplacement, seq int64) error {
	if b == nil {
		return nil
	}
	if !service.ValidID(b.ServiceID) || b.Revision < 1 || b.Revision >= seq || b.TaskID == "" || b.RunID == "" || b.Head.Seq < 1 || b.Head.Seq >= seq || len(b.Head.Hash) != 64 {
		return errors.New("invalid service replacement witness")
	}
	return nil
}
func (s *Store) replacementSettled(b *ServiceReplacement, activeIDs []string) bool {
	if b == nil {
		return true
	}
	task := s.tasks[b.TaskID]
	if !terminalServiceTask(task) {
		return false
	}
	active := map[string]bool{}
	for _, id := range activeIDs {
		active[id] = true
	}
	for _, run := range s.runs {
		if run.TaskID == b.TaskID && (active[run.ID] || run.Claim != nil && run.Claim.ReleasedAt == nil) {
			return false
		}
	}
	return true
}
func (s *Store) countReplacement(b *ServiceReplacement, delta int) {
	if b == nil {
		return
	}
	if s.replacementVictims == nil {
		s.replacementVictims = map[string]int{}
	}
	count := s.replacementVictims[b.TaskID] + delta
	if count <= 0 {
		delete(s.replacementVictims, b.TaskID)
	} else {
		s.replacementVictims[b.TaskID] = count
	}
}
func (s *Store) trackTaskReplacement(task *Task, delta int) {
	if task != nil && !terminalServiceTask(task) {
		s.countReplacement(task.ServiceReplacement, delta)
	}
}
func (s *Store) isReplacementVictim(taskID string) bool {
	return s.replacementVictims[taskID] > 0
}
func (s *Store) selectServiceReplacement(id, excluding string) *ServiceReplacement {
	// At most one unsettled replacement per service. This bounds the transient
	// receipt/reservation above max_instances while a process shuts down.
	for _, task := range s.tasks {
		if !terminalServiceTask(task) && task.ServiceReplacement != nil && task.ServiceReplacement.ServiceID == id && !s.replacementSettled(task.ServiceReplacement, nil) {
			return nil
		}
	}
	for _, d := range s.serviceEventDeliveries {
		if d.Recipient.ServiceID == id && d.Recipient.Status == "pending" && d.Recipient.Replacement != nil {
			return nil
		}
	}
	candidates := []*Task{}
	for _, task := range s.tasks {
		if task.ID == excluding || task.Source != "trigger:"+id || task.Sample || terminalServiceTask(task) || s.isReplacementVictim(task.ID) {
			continue
		}
		run := s.runs[task.CurrentRunID]
		if run == nil || run.Status == "stopping" {
			continue
		}
		candidates = append(candidates, task)
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		admittedA, admittedB := len(a.Runs) > 0, len(b.Runs) > 0
		if admittedA != admittedB {
			return admittedA
		}
		if !a.CreatedAt.Equal(b.CreatedAt) {
			return a.CreatedAt.Before(b.CreatedAt)
		}
		return a.ID < b.ID
	})
	if len(candidates) == 0 || s.services == nil {
		return nil
	}
	task := candidates[0]
	return &ServiceReplacement{ServiceID: id, Revision: s.services.Revision, TaskID: task.ID, RunID: task.CurrentRunID, Head: task.Head}
}
func (s *Store) serviceReplacementWait(task *Task, activeIDs []string) string {
	id := strings.TrimPrefix(task.Source, "trigger:")
	if s.isReplacementVictim(task.ID) {
		return "service-superseded:" + id
	}
	if !s.replacementSettled(task.ServiceReplacement, activeIDs) {
		return "service-replacement:" + id
	}
	return ""
}
func (s *Store) serviceReplacementViews(id string) []ServiceReplacementView {
	out := []ServiceReplacementView{}
	for _, task := range s.tasks {
		if task.ServiceReplacement == nil || task.ServiceReplacement.ServiceID != id || terminalServiceTask(task) {
			continue
		}
		out = append(out, ServiceReplacementView{TaskID: task.ID, RunID: task.CurrentRunID, Previous: *task.ServiceReplacement, Waiting: !s.replacementSettled(task.ServiceReplacement, nil)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TaskID < out[j].TaskID })
	return out
}
func (e *Engine) reconcileServiceReplacements() error {
	e.actions.Lock()
	defer e.actions.Unlock()
	e.Store.mu.RLock()
	owners := []string{}
	for id, task := range e.Store.tasks {
		if task.ServiceReplacement != nil && !terminalServiceTask(task) {
			owners = append(owners, id)
		}
	}
	e.Store.mu.RUnlock()
	sort.Strings(owners)
	stopped := 0
	for _, ownerID := range owners {
		if e.ctx != nil && e.ctx.Err() != nil {
			return nil
		}
		e.Store.mu.RLock()
		owner := e.Store.tasks[ownerID]
		runID := ""
		if owner != nil && !terminalServiceTask(owner) && owner.ServiceReplacement != nil && e.Store.continuousAllowsTask(owner) && (owner.Scheduling.Deadline == nil || time.Now().Before(*owner.Scheduling.Deadline)) {
			victim := e.Store.tasks[owner.ServiceReplacement.TaskID]
			if victim != nil && !terminalServiceTask(victim) {
				runID = victim.CurrentRunID
			}
		}
		e.Store.mu.RUnlock()
		if runID == "" {
			continue
		}
		if err := e.cancelWork(runID, "A newer service occurrence replaced this work under if_running: kill."); err != nil && !errors.Is(err, ErrConflict) {
			return err
		}
		stopped++
		if stopped == 64 {
			return nil
		}
	}
	return nil
}
