package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"mitos/internal/service"
)

const serviceEventDepth = 8
const serviceEventBudget = 64

type ServiceEventCause struct {
	Custom    *CustomEventInput `json:"custom,omitempty"`
	Seq       int64             `json:"seq"`
	Hash      string            `json:"hash"`
	ServiceID string            `json:"service_id"`
	TaskID    string            `json:"task_id"`
	RunID     string            `json:"run_id"`
	Names     []string          `json:"names"`
	RootSeq   int64             `json:"root_seq"`
	RootHash  string            `json:"root_hash"`
	Depth     int               `json:"depth"`
}
type ServiceEventRecipient struct {
	DependsOn   []string             `json:"depends_on,omitempty"`
	Replacement *ServiceReplacement  `json:"replacement,omitempty"`
	Queue       *ServiceQueueBinding `json:"queue,omitempty"`
	ServiceID   string               `json:"service_id"`
	Revision    int64                `json:"catalog_revision"`
	Patterns    []string             `json:"patterns"`
	Matched     []string             `json:"matched"`
	Work        *Brief               `json:"work,omitempty"`
	Status      string               `json:"status"`
	Detail      string               `json:"detail,omitempty"`
}
type ServiceEmission struct {
	ServiceID  string                  `json:"service_id"`
	TaskID     string                  `json:"task_id"`
	RunID      string                  `json:"run_id"`
	Names      []string                `json:"names"`
	RootSeq    int64                   `json:"root_seq,omitempty"`
	RootHash   string                  `json:"root_hash,omitempty"`
	Depth      int                     `json:"depth"`
	Recipients []ServiceEventRecipient `json:"recipients"`
}
type ServiceEventDelivery struct {
	Key       string                `json:"key"`
	Cause     ServiceEventCause     `json:"cause"`
	Recipient ServiceEventRecipient `json:"recipient"`
	RunID     string                `json:"run_id,omitempty"`
}

func cloneEventDelivery(d *ServiceEventDelivery) ServiceEventDelivery {
	data, _ := json.Marshal(d)
	var copy ServiceEventDelivery
	_ = json.Unmarshal(data, &copy)
	return copy
}
func eventRootKey(seq int64, hash string) string { return fmt.Sprintf("%d:%s", seq, hash) }
func (s *Store) pendingServiceEvents(id string) int {
	pending := 0
	for _, d := range s.serviceEventDeliveries {
		if d.Recipient.ServiceID == id && d.Recipient.Status == "pending" {
			pending++
		}
	}
	return pending
}
func (s *Store) serviceEmissionChange(id string, c change, now time.Time) *ServiceEmission {
	task := c.Task
	if task == nil || (task.Status != "done" && task.Status != "failed") || !strings.HasPrefix(task.Source, "trigger:") || task.Sample {
		return nil
	}
	previous := s.tasks[task.ID]
	if previous == nil || previous.CurrentRunID != id || previous.Status == task.Status {
		return nil
	}
	source := strings.TrimPrefix(task.Source, "trigger:")
	outcome := "service.succeeded:"
	if task.Status == "failed" {
		outcome = "service.failed:"
	}
	emission := &ServiceEmission{ServiceID: source, TaskID: task.ID, RunID: id, Names: []string{outcome + source, "service.completed:" + source}, Recipients: []ServiceEventRecipient{}}
	used := 0
	if cause := task.ServiceEvent; cause != nil {
		emission.RootSeq = cause.RootSeq
		emission.RootHash = cause.RootHash
		emission.Depth = cause.Depth
		used = s.serviceEventRootUsed[eventRootKey(cause.RootSeq, cause.RootHash)]
	}
	emission.Recipients = s.allocateEventRecipients(emission.Names, emission.Depth, used, task.ID, id, c, now)
	return emission
}
func (s *Store) allocateEventRecipients(names []string, depth, used int, sourceTaskID, sourceRunID string, c change, now time.Time) []ServiceEventRecipient {
	recipients := []ServiceEventRecipient{}
	units := activeServiceUnits(s.services)
	ids := []string{}
	for unitID, u := range units {
		if u.TriggerKind() == "event" {
			ids = append(ids, unitID)
		}
	}
	sort.Strings(ids)
	for _, unitID := range ids {
		unit := units[unitID]
		patterns, _ := unit.EventPatterns()
		matched := service.MatchEvents(patterns, names)
		if len(matched) == 0 {
			continue
		}
		r := ServiceEventRecipient{ServiceID: unitID, Revision: s.services.Revision, Patterns: patterns, Matched: matched, Status: "pending", Queue: s.serviceQueueBinding(unitID, s.services.Revision)}
		r.DependsOn = append([]string(nil), unit.DependsOn...)
		// Project the source's terminal Task and its breaker/abort/retry change when
		// reserving capacity. Self-chaining must neither leak nor lose a final slot.
		pending, _ := s.serviceCounts(unitID, sourceTaskID, nil)
		if p := s.serviceRetryPlans[sourceRunID]; p != nil && p.Status == "pending" && p.Binding.ServiceID == unitID {
			pending--
		}
		capacity := unit.Concurrency.Effective()
		limit := capacity.MaxInstances
		if capacity.IfRunning == "queue" {
			limit += capacity.QueueMax
		}
		if capacity.IfRunning == "kill" && pending == limit {
			r.Replacement = s.selectServiceReplacement(unitID, sourceTaskID)
		}
		circuitAllows := s.serviceBreakerAllows(unitID, now)
		if c.ServiceBreaker != nil && c.ServiceBreaker.ServiceID == unitID {
			circuitAllows = c.ServiceBreaker.State.Available(now)
		}
		aborted := s.serviceAborted(unitID) || c.ServiceRetry != nil && c.ServiceRetry.Status == "aborted" && c.ServiceRetry.Binding.ServiceID == unitID
		switch {
		case s.serviceLifecycleHeld(unitID):
			r.Status = "skipped"
			r.Detail = "Service lifecycle is stopped."
		case depth >= serviceEventDepth:
			r.Status = "skipped"
			r.Detail = "Service event chain depth limit reached."
		case used >= serviceEventBudget:
			r.Status = "skipped"
			r.Detail = "Service event root delivery budget reached."
		case aborted:
			r.Status = "skipped"
			r.Detail = "Service abort policy is withholding new work."
		case !circuitAllows:
			r.Status = "skipped"
			r.Detail = "Service circuit breaker is withholding new work."
		case pending >= limit && r.Replacement == nil:
			r.Status = "skipped"
			r.Detail = "Service concurrency or queue capacity is full."
		default:
			work := s.services.Work[unitID]
			r.Work = &work
			used++
		}
		if r.Status != "pending" {
			r.Replacement = nil
		}
		recipients = append(recipients, r)
	}
	return recipients
}
func validServiceCause(cause *ServiceEventCause, maxDepth int) bool {
	if cause != nil && cause.Custom != nil {
		return validateCustomInput(cause.Custom) == nil && cause.Seq > 0 && len(cause.Hash) == 64 && cause.ServiceID == "" && cause.TaskID == "" && cause.RunID == "" && reflect.DeepEqual(cause.Names, []string{cause.Custom.Name}) && cause.RootSeq == cause.Seq && cause.RootHash == cause.Hash && cause.Depth == 1 && maxDepth >= 1
	}
	return cause != nil && cause.Seq > 0 && len(cause.Hash) == 64 && service.ValidID(cause.ServiceID) && cause.TaskID != "" && cause.RunID != "" && len(cause.Names) == 2 && cause.RootSeq > 0 && cause.RootSeq <= cause.Seq && len(cause.RootHash) == 64 && cause.Depth >= 1 && cause.Depth <= maxDepth
}
func validateServiceEmission(e Event, c change) error {
	emission := c.ServiceEmission
	if emission == nil {
		return nil
	}
	if e.Type != "effect" || emission.RunID != e.RunID || !service.ValidID(emission.ServiceID) || c.Task == nil || c.Task.ID != emission.TaskID || c.Task.Source != "trigger:"+emission.ServiceID || (c.Task.Status != "done" && c.Task.Status != "failed") || emission.Depth < 0 || emission.Depth > serviceEventDepth || len(emission.Recipients) > 256 {
		return errors.New("invalid service outcome emission")
	}
	expected := "service.succeeded:"
	if c.Task.Status == "failed" {
		expected = "service.failed:"
	}
	if !reflect.DeepEqual(emission.Names, []string{expected + emission.ServiceID, "service.completed:" + emission.ServiceID}) {
		return errors.New("service outcome names do not match Task result")
	}
	if emission.Depth > 0 && (emission.RootSeq < 1 || emission.RootSeq >= e.Seq || len(emission.RootHash) != 64) || emission.Depth == 0 && (emission.RootSeq != 0 || emission.RootHash != "") {
		return errors.New("invalid service event root")
	}
	return validateEventRecipients(e, emission)
}
func validateEventRecipients(e Event, emission *ServiceEmission) error {
	if len(emission.Recipients) > 256 {
		return errors.New("too many event recipients")
	}
	seen := map[string]bool{}
	pending := 0
	for _, r := range emission.Recipients {
		if err := validateCycleBinding(&ServiceCycleBinding{ID: "event", Revision: r.Revision, ServiceID: r.ServiceID, DependsOn: r.DependsOn}); err != nil {
			return err
		}
		if !service.ValidID(r.ServiceID) || seen[r.ServiceID] || r.Revision < 1 || r.Revision >= e.Seq || (r.Status != "pending" && r.Status != "skipped") || len(r.Detail) > 2000 || len(r.Patterns) == 0 || len(r.Patterns) > 32 {
			return errors.New("invalid service event recipient")
		}
		if err := validateReplacement(r.Replacement, e.Seq); err != nil {
			return err
		}
		if r.Replacement != nil && (r.Status != "pending" || r.Replacement.ServiceID != r.ServiceID || r.Replacement.Revision != r.Revision || r.Replacement.TaskID == emission.TaskID) {
			return errors.New("invalid replacement allocation")
		}
		if err := validateServiceQueue(r.Queue); err != nil {
			return err
		}
		if r.Queue != nil && (r.Queue.ServiceID != r.ServiceID || r.Queue.Revision != r.Revision) {
			return errors.New("event queue policy not bound to allocation")
		}
		seen[r.ServiceID] = true
		for _, pattern := range r.Patterns {
			if !service.ValidEventPattern(pattern) {
				return errors.New("invalid recipient event pattern")
			}
		}
		if len(r.Matched) == 0 || !reflect.DeepEqual(r.Matched, service.MatchEvents(r.Patterns, emission.Names)) {
			return errors.New("recipient did not match service outcome")
		}
		if r.Status == "pending" {
			pending++
			if emission.Depth >= serviceEventDepth || pending > serviceEventBudget || r.Work == nil {
				return errors.New("invalid pending event work")
			}
			if err := r.Work.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}
func validateServiceDelivery(e Event, c change) error {
	d := c.ServiceDelivery
	if d == nil {
		return nil
	}
	if err := validateCycleBinding(&ServiceCycleBinding{ID: "event", Revision: d.Recipient.Revision, ServiceID: d.Recipient.ServiceID, DependsOn: d.Recipient.DependsOn}); err != nil {
		return err
	}
	if e.Type != "effect" || !validServiceCause(&d.Cause, serviceEventDepth) || d.Cause.Seq >= e.Seq || d.Key != fmt.Sprintf("%d:%s", d.Cause.Seq, d.Recipient.ServiceID) || !service.ValidID(d.Recipient.ServiceID) || len(d.Recipient.Detail) > 2000 {
		return errors.New("invalid service event delivery")
	}
	if err := validateReplacement(d.Recipient.Replacement, e.Seq); err != nil {
		return err
	}
	if d.Recipient.Replacement != nil && (d.Recipient.Replacement.ServiceID != d.Recipient.ServiceID || d.Recipient.Replacement.Revision != d.Recipient.Revision) {
		return errors.New("invalid replacement delivery binding")
	}
	if err := validateServiceQueue(d.Recipient.Queue); err != nil {
		return err
	}
	if d.Recipient.Queue != nil && (d.Recipient.Queue.ServiceID != d.Recipient.ServiceID || d.Recipient.Queue.Revision != d.Recipient.Revision) {
		return errors.New("delivery queue priority is not bound to allocation")
	}
	switch d.Recipient.Status {
	case "enqueued":
		if c.ServiceCall == nil || !reflect.DeepEqual(c.ServiceCall.Replacement, d.Recipient.Replacement) {
			return errors.New("event delivery changed replacement target")
		}
		if d.Recipient.Queue != nil && (c.ServiceCall == nil || !reflect.DeepEqual(d.Recipient.Queue, c.ServiceCall.Queue)) {
			return errors.New("event intake changed allocated queue priority")
		}
		if c.Run == nil || c.Run.ID != d.RunID || e.RunID != d.RunID || c.ServiceCall == nil || c.ServiceCall.Origin != "event" || c.ServiceCall.ServiceID != d.Recipient.ServiceID || c.ServiceCall.Key != d.Key || !reflect.DeepEqual(c.ServiceCall.Event, &d.Cause) {
			return errors.New("event delivery receipt is not atomically bound")
		}
	case "skipped", "suppressed", "rejected":
		if e.RunID != "" || e.Kind != "service_event_delivery" || c.Run != nil || c.Task != nil {
			return errors.New("invalid closed event delivery")
		}
	default:
		return errors.New("invalid event delivery outcome")
	}
	return nil
}
func (s *Store) applyServiceEmission(e Event, c change) error {
	if c.ServiceEmission == nil {
		return nil
	}
	if err := validateServiceEmission(e, c); err != nil {
		return err
	}
	emission := c.ServiceEmission
	rootSeq, rootHash := emission.RootSeq, emission.RootHash
	if rootSeq == 0 {
		rootSeq = e.Seq
		rootHash = e.Hash
	}
	cause := ServiceEventCause{Seq: e.Seq, Hash: e.Hash, ServiceID: emission.ServiceID, TaskID: emission.TaskID, RunID: emission.RunID, Names: emission.Names, RootSeq: rootSeq, RootHash: rootHash, Depth: emission.Depth + 1}
	return s.applyEventRecipients(e, cause, emission.Recipients)
}
func (s *Store) applyEventRecipients(e Event, cause ServiceEventCause, recipients []ServiceEventRecipient) error {
	if s.serviceEventDeliveries == nil {
		s.serviceEventDeliveries = map[string]*ServiceEventDelivery{}
	}
	if s.serviceEventRootUsed == nil {
		s.serviceEventRootUsed = map[string]int{}
	}
	rootKey := eventRootKey(cause.RootSeq, cause.RootHash)
	for _, r := range recipients {
		d := ServiceEventDelivery{Key: fmt.Sprintf("%d:%s", e.Seq, r.ServiceID), Cause: cause, Recipient: r}
		if s.serviceEventDeliveries[d.Key] != nil {
			return errors.New("duplicate event delivery allocation")
		}
		if r.Status == "pending" {
			s.countReplacement(r.Replacement, 1)
			s.serviceEventRootUsed[rootKey]++
			if s.serviceEventRootUsed[rootKey] > serviceEventBudget {
				return errors.New("service event root exceeded its budget")
			}
		}
		copy := cloneEventDelivery(&d)
		s.serviceEventDeliveries[d.Key] = &copy
		s.noteServiceDelivery(&copy)
	}
	return nil
}
func (s *Store) noteServiceDelivery(d *ServiceEventDelivery) {
	if s.serviceStates == nil {
		s.serviceStates = map[string]*ServiceState{}
	}
	state := s.serviceStates[d.Recipient.ServiceID]
	if state == nil {
		state = &ServiceState{ServiceID: d.Recipient.ServiceID}
		s.serviceStates[d.Recipient.ServiceID] = state
	}
	copy := cloneEventDelivery(d)
	state.LastEvent = &copy
	switch d.Recipient.Status {
	case "enqueued":
		state.EventsDelivered++
	case "skipped", "suppressed":
		state.EventsSkipped++
	case "rejected":
		state.EventsRejected++
	}
}
func (s *Store) applyServiceDelivery(e Event, c change) error {
	if c.ServiceDelivery == nil {
		return nil
	}
	if err := validateServiceDelivery(e, c); err != nil {
		return err
	}
	if s.serviceEventDeliveries == nil {
		s.serviceEventDeliveries = map[string]*ServiceEventDelivery{}
	}
	d := cloneEventDelivery(c.ServiceDelivery)
	// Per-run metric folds may see the child without the producer's global history.
	if previous := s.serviceEventDeliveries[d.Key]; previous != nil && previous.Recipient.Status != "pending" {
		return errors.New("service event delivery already closed")
	}
	if previous := s.serviceEventDeliveries[d.Key]; previous != nil && previous.Recipient.Status == "pending" {
		s.countReplacement(previous.Recipient.Replacement, -1)
	}
	s.serviceEventDeliveries[d.Key] = &d
	s.noteServiceDelivery(&d)
	return nil
}
func (e *Engine) reconcileServiceEvents() error {
	e.actions.Lock()
	defer e.actions.Unlock()
	e.Store.mu.RLock()
	catalog := cloneCatalog(e.Store.services)
	deliveries := []ServiceEventDelivery{}
	for _, d := range e.Store.serviceEventDeliveries {
		if d.Recipient.Status == "pending" {
			deliveries = append(deliveries, cloneEventDelivery(d))
		}
	}
	e.Store.mu.RUnlock()
	units := activeServiceUnits(&catalog)
	sort.Slice(deliveries, func(i, j int) bool {
		a, b := deliveries[i], deliveries[j]
		if a.Cause.Seq != b.Cause.Seq {
			return a.Cause.Seq < b.Cause.Seq
		}
		return a.Recipient.ServiceID < b.Recipient.ServiceID
	})
	// Bound one drain pass; durable remaining recipients are revisited next tick.
	if len(deliveries) > 64 {
		deliveries = deliveries[:64]
	}
	for _, d := range deliveries {
		if e.ctx != nil && e.ctx.Err() != nil {
			return nil
		}
		unit, active := units[d.Recipient.ServiceID]
		patterns, _ := unit.EventPatterns()
		status, detail := "", ""
		if d.Recipient.Replacement != nil && unit.Concurrency.Effective().IfRunning != "kill" || !active || unit.TriggerKind() != "event" || !sameServiceDependencies(unit.DependsOn, d.Recipient.DependsOn) || !reflect.DeepEqual(patterns, d.Recipient.Patterns) || d.Recipient.Work == nil || !reflect.DeepEqual(catalog.Work[d.Recipient.ServiceID], *d.Recipient.Work) {
			status = "suppressed"
			detail = "Subscription, work binding, or service activation changed."
		}
		var brief Brief
		var name string
		var err error
		if status == "" {
			brief, name, err = e.prepareBrief(*d.Recipient.Work)
			if err != nil {
				status = "rejected"
				detail = fmt.Sprintf("Event admission: %.1850s", err.Error())
			}
		}
		e.Store.mu.Lock()
		current := e.Store.serviceEventDeliveries[d.Key]
		if current == nil || current.Recipient.Status != "pending" || e.Store.services == nil || e.Store.services.Revision != catalog.Revision {
			e.Store.mu.Unlock()
			continue
		}
		if e.Store.serviceLifecycleHeld(d.Recipient.ServiceID) {
			status = "suppressed"
			detail = "Service lifecycle is stopped."
		}
		if status == "" {
			if e.Store.serviceAborted(d.Recipient.ServiceID) || !e.Store.serviceBreakerAllows(d.Recipient.ServiceID, time.Now().UTC()) {
				status = "skipped"
				detail = "Service abort or circuit breaker is withholding new work."
			} else if err = e.Store.validateTaskRequest("service-event-validation", brief.Scheduling, false); err != nil {
				status = "rejected"
				detail = fmt.Sprintf("Event scheduling: %.1850s", err.Error())
			}
		}
		if status == "" {
			d.Recipient.Status = "enqueued"
			d.Recipient.Detail = "The matched outcome created a Task receipt."
			queue := d.Recipient.Queue
			if queue == nil {
				queue = &ServiceQueueBinding{ServiceID: d.Recipient.ServiceID, Revision: d.Recipient.Revision, Priority: "normal"}
			}
			call := &ServiceCall{Replacement: d.Recipient.Replacement, Queue: queue, ServiceID: d.Recipient.ServiceID, Key: d.Key, Revision: catalog.Revision, Origin: "event", Event: &d.Cause}
			_, err = e.Store.createWithEventLocked(brief, name, "", false, call, nil, &d)
		} else {
			d.Recipient.Status = status
			d.Recipient.Detail = detail
			err = e.Store.commit("", "effect", "service_event_delivery", change{ServiceDelivery: &d})
		}
		e.Store.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}
