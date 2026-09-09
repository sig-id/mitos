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

type ServiceCycleBinding struct {
	ID        string   `json:"id"`
	Revision  int64    `json:"catalog_revision"`
	ServiceID string   `json:"service_id"`
	DependsOn []string `json:"depends_on,omitempty"`
}
type ServiceDependencyWitness struct {
	ServiceID string `json:"service_id"`
	TaskID    string `json:"task_id"`
	RunID     string `json:"run_id"`
	Seq       int64  `json:"event_seq"`
	Hash      string `json:"event_hash"`
}

func cycleIndex(c *ServiceCycleBinding) string { return fmt.Sprintf("%d:%s", c.Revision, c.ID) }
func tickCycle(now time.Time) string           { return "tick:" + now.UTC().Format(time.RFC3339Nano) }
func (s *Store) serviceCycleBinding(id, cycle string, revision int64) *ServiceCycleBinding {
	out := &ServiceCycleBinding{ID: cycle, Revision: revision, ServiceID: id}
	if s.services != nil {
		for _, u := range s.services.Config.Units {
			if u.ID == id {
				out.DependsOn = append([]string(nil), u.DependsOn...)
				sort.Strings(out.DependsOn)
				break
			}
		}
	}
	return out
}
func validateCycleBinding(c *ServiceCycleBinding) error {
	if c == nil {
		return nil
	}
	if c.ID == "" || len(c.ID) > 320 || strings.ContainsAny(c.ID, "\x00\r\n") || c.Revision < 1 || !service.ValidID(c.ServiceID) || len(c.DependsOn) > 128 {
		return errors.New("invalid service cycle binding")
	}
	seen := map[string]bool{}
	for _, dep := range c.DependsOn {
		if !service.ValidID(dep) || dep == c.ServiceID || seen[dep] {
			return errors.New("invalid service completion prerequisite")
		}
		seen[dep] = true
	}
	return nil
}
func validateServiceDependencies(e Event, c change) error {
	if c.ServiceCall != nil && c.ServiceCall.Cycle != nil {
		b := c.ServiceCall.Cycle
		if err := validateCycleBinding(b); err != nil {
			return err
		}
		if b.Revision > c.ServiceCall.Revision || b.ServiceID != c.ServiceCall.ServiceID || c.Task == nil || !reflect.DeepEqual(c.Task.ServiceCycle, b) {
			return errors.New("service cycle is not bound to its creation receipt")
		}
	}
	if t := c.Task; t != nil {
		if err := validateCycleBinding(t.ServiceCycle); err != nil {
			return err
		}
		if t.ServiceCycle != nil && (t.Source != "trigger:"+t.ServiceCycle.ServiceID || t.ServiceCycle.Revision >= e.Seq) {
			return errors.New("service cycle does not match Task source")
		}
		if len(t.ServiceDependencyEvidence) > 0 {
			if t.ServiceCycle == nil || len(t.ServiceDependencyEvidence) != len(t.ServiceCycle.DependsOn) || t.AdmittedRunID == "" {
				return errors.New("service completion evidence lacks admission")
			}
			seen := map[string]bool{}
			wanted := map[string]bool{}
			for _, dep := range t.ServiceCycle.DependsOn {
				wanted[dep] = true
			}
			for _, w := range t.ServiceDependencyEvidence {
				if !wanted[w.ServiceID] || seen[w.ServiceID] || w.TaskID == "" || w.RunID == "" || w.Seq < 1 || w.Seq >= e.Seq || len(w.Hash) != 64 {
					return errors.New("invalid service completion evidence")
				}
				seen[w.ServiceID] = true
			}
		}
	}
	return nil
}
func (s *Store) trackServiceCycle(e Event, c change, prior *Task) error {
	if err := validateServiceDependencies(e, c); err != nil {
		return err
	}
	task := c.Task
	if task == nil || task.ServiceCycle == nil {
		return nil
	}
	if prior != nil && !reflect.DeepEqual(prior.ServiceCycle, task.ServiceCycle) {
		return errors.New("Task service cycle changed")
	}
	if s.serviceCycleReceipts == nil {
		s.serviceCycleReceipts = map[string]map[string]string{}
	}
	key := cycleIndex(task.ServiceCycle)
	if s.serviceCycleReceipts[key] == nil {
		s.serviceCycleReceipts[key] = map[string]string{}
	}
	receipts := s.serviceCycleReceipts[key]
	if existing := receipts[task.ServiceCycle.ServiceID]; existing != "" && existing != task.ID {
		return errors.New("duplicate service receipt in one cycle")
	}
	receipts[task.ServiceCycle.ServiceID] = task.ID
	return nil
}
func (s *Store) serviceCycleReadiness(task *Task) ([]string, []ServiceDependencyWitness) {
	if task == nil || task.ServiceCycle == nil || len(task.ServiceCycle.DependsOn) == 0 {
		return nil, nil
	}
	if task.AdmittedRunID == task.CurrentRunID {
		return nil, append([]ServiceDependencyWitness(nil), task.ServiceDependencyEvidence...)
	}
	receipts := s.serviceCycleReceipts[cycleIndex(task.ServiceCycle)]
	waiting := []string{}
	witnesses := []ServiceDependencyWitness{}
	for _, dep := range task.ServiceCycle.DependsOn {
		upstream := s.tasks[receipts[dep]]
		if upstream == nil || upstream.Sample || upstream.Status != "done" {
			waiting = append(waiting, "service-dependency:"+dep)
			continue
		}
		witnesses = append(witnesses, ServiceDependencyWitness{ServiceID: dep, TaskID: upstream.ID, RunID: upstream.CurrentRunID, Seq: upstream.Head.Seq, Hash: upstream.Head.Hash})
	}
	return waiting, witnesses
}
func (s *Store) prepareServiceCycle(call *ServiceCall, delivery *ServiceEventDelivery) error {
	if call.Cycle == nil {
		id := "single:" + call.ServiceID + ":" + call.Key
		switch call.Origin {
		case "interval", "cron":
			if call.Tick == nil {
				return errors.New("timed cycle requires its schedule occurrence")
			}
			id = tickCycle(call.Tick.ObservedAt)
		case "event":
			if call.Event == nil {
				return errors.New("event cycle requires its source outcome")
			}
			id = "event:" + eventRootKey(call.Event.Seq, call.Event.Hash)
		}
		call.Cycle = s.serviceCycleBinding(call.ServiceID, id, call.Revision)
		if delivery != nil {
			call.Cycle.Revision = delivery.Recipient.Revision
			call.Cycle.DependsOn = append([]string(nil), delivery.Recipient.DependsOn...)
		}
	}
	if err := validateCycleBinding(call.Cycle); err != nil {
		return err
	}
	if s.serviceCycleReceipts[cycleIndex(call.Cycle)][call.ServiceID] != "" {
		return errors.New("this service already has a Task receipt in the requested cycle; reuse its original key or choose a new cycle")
	}
	return nil
}

func sameServiceDependencies(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
