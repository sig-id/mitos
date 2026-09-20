package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"mitos/internal/decision"
	"mitos/internal/workpool"
)

type TaskHead struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}
type Task struct {
	ServiceLifecycle          *ServiceLifecycleCause     `json:"service_lifecycle,omitempty"`
	ServiceIngress            *ServiceIngress            `json:"service_ingress,omitempty"`
	ServiceCycle              *ServiceCycleBinding       `json:"service_cycle,omitempty"`
	ServiceDependencyEvidence []ServiceDependencyWitness `json:"service_dependency_evidence,omitempty"`
	ServiceReplacement        *ServiceReplacement        `json:"service_replacement,omitempty"`
	ServiceQueue              *ServiceQueueBinding       `json:"service_queue,omitempty"`
	ContinuousEpoch           int64                      `json:"continuous_epoch,omitempty"`
	ServiceEvent              *ServiceEventCause         `json:"service_event,omitempty"`
	AdmissionPool             *workpool.Pool             `json:"admission_pool,omitempty"`
	ID                        string                     `json:"id"`
	Tenant                    decision.Tenant            `json:"tenant"`
	Source                    string                     `json:"source"`
	Status                    string                     `json:"status"`
	Brief                     Brief                      `json:"payload"`
	Scheduling                workpool.Request           `json:"scheduling"`
	Runs                      []string                   `json:"runs"`
	CurrentRunID              string                     `json:"current_run_id"`
	AdmittedRunID             string                     `json:"admitted_run_id,omitempty"`
	DependencyEvidence        []workpool.Witness         `json:"dependency_evidence,omitempty"`
	WaitingFor                []string                   `json:"waiting_for,omitempty"`
	CreatedAt                 time.Time                  `json:"created_at"`
	ReadyAt                   time.Time                  `json:"ready_at"`
	UpdatedAt                 time.Time                  `json:"updated_at"`
	LastScheduledSeq          int64                      `json:"last_scheduled_seq"`
	Head                      TaskHead                   `json:"head"`
	Sample                    bool                       `json:"sample"`
	Legacy                    bool                       `json:"legacy_projection,omitempty"`
}

func cloneTask(t *Task) Task {
	b, _ := json.Marshal(t)
	var copy Task
	_ = json.Unmarshal(b, &copy)
	return copy
}
func (s *Store) Task(id string) (Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t := s.tasks[id]
	if t == nil {
		return Task{}, ErrNotFound
	}
	return cloneTask(t), nil
}
func (s *Store) Tasks() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []Task{}
	for _, t := range s.tasks {
		out = append(out, cloneTask(t))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out
}
func (s *Store) configuredPools() map[string]workpool.Pool {
	if s.pools == nil {
		return workpool.Defaults()
	}
	return s.pools
}
func (s *Store) Pools() []workpool.Pool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := []workpool.Pool{}
	for _, p := range s.configuredPools() {
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func validatePoolEvent(e Event, c change) error {
	if e.RunID != "" || e.Kind != "pool_configured" || e.Type != "effect" || c.Run != nil || c.Task != nil {
		return errors.New("invalid pool event")
	}
	return c.Pool.Validate()
}
func (s *Store) ConfigurePool(p workpool.Pool) error {
	if err := p.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pools := s.configuredPools()
	if len(pools) >= 32 {
		if _, exists := pools[p.ID]; !exists {
			return errors.New("at most 32 pools may be configured")
		}
	}
	reserved := p.Reserved
	for id, other := range pools {
		if id != p.ID {
			reserved += other.Reserved
		}
	}
	if reserved > workpool.Capacity {
		return errors.New("pool reservations exceed provider capacity")
	}
	if old, exists := pools[p.ID]; exists && old == p {
		return nil
	}
	return s.commit("", "effect", "pool_configured", change{Pool: &p})
}
func (s *Store) validateTaskRequest(id string, r workpool.Request, sample bool) error {
	r.Normalize()
	if err := r.Validate(); err != nil {
		return err
	}
	if _, ok := s.configuredPools()[r.Pool]; !ok {
		return fmt.Errorf("unknown pool %s", r.Pool)
	}
	graph := map[string]workpool.Request{}
	for tid, t := range s.tasks {
		graph[tid] = t.Scheduling
	}
	graph[id] = r
	for _, dep := range r.Deps {
		upstream := s.tasks[dep.From]
		if upstream == nil {
			return fmt.Errorf("dependency task %s does not exist", dep.From)
		}
		if upstream.Tenant != decision.LocalTenant || (!sample && upstream.Sample) {
			return errors.New("dependency must belong to the same local tenant and cannot use sample work")
		}
	}
	return workpool.Graph(graph)
}

// Task transitions are part of the same synced Effect as the run transition.
// A late event on an older attempt cannot overwrite the task's current attempt.
func (s *Store) taskChange(id, kind string, c change, at time.Time, seq int64) (*Task, string) {
	r := s.runs[id]
	if c.Run != nil {
		r = c.Run
	}
	if r == nil || r.Decision != nil && r.Decision.Source != nil {
		return nil, ""
	}
	prior := s.tasks[r.TaskID]
	if prior != nil && c.Status == "" && c.Run == nil {
		return nil, ""
	}
	var task Task
	if prior != nil {
		task = cloneTask(prior)
	} else {
		request := r.Brief.Scheduling
		request.Normalize()
		task = Task{ID: r.TaskID, Tenant: runTenant(r), Source: "human", Brief: r.Brief, Scheduling: request, CreatedAt: r.CreatedAt, ReadyAt: r.CreatedAt, Sample: r.Sample, Runs: []string{}}
		if c.ServiceCall != nil {
			task.ServiceLifecycle = c.ServiceCall.Lifecycle
			task.ServiceIngress = c.ServiceCall.Ingress
			task.ServiceCycle = c.ServiceCall.Cycle
			task.Source = "trigger:" + c.ServiceCall.ServiceID
			task.ServiceEvent = c.ServiceCall.Event
			task.ServiceQueue = c.ServiceCall.Queue
			task.ServiceReplacement = c.ServiceCall.Replacement
			task.ContinuousEpoch = c.ServiceCall.ContinuousEpoch
		}
	}
	if c.Run != nil && c.ServiceRetry != nil {
		task.Brief = r.Brief
		task.Scheduling = r.Brief.Scheduling
		task.Scheduling.Normalize()
	}
	if c.Run != nil || task.CurrentRunID == "" {
		if task.CurrentRunID != r.ID {
			task.CurrentRunID = r.ID
			task.AdmittedRunID = ""
			task.AdmissionPool = nil
			task.DependencyEvidence = nil
			task.ServiceDependencyEvidence = nil
			task.WaitingFor = nil
			task.ReadyAt = at
		}
	} else if task.CurrentRunID != r.ID {
		return nil, ""
	}
	status := r.Status
	if c.Status != "" {
		status = c.Status
	}
	switch status {
	case "queued":
		task.Status = "queued"
		if task.AdmittedRunID == r.ID && (kind == "checkpoint_restored" || kind == "rewind_resumed") {
			task.Status = "scheduled"
		}
	case "running":
		task.Status = "running"
	case "stopping":
		task.Status = "running"
	case "parked", "done", "failed", "cancelled":
		task.Status = status
	}
	if kind == "claims_acquired" || kind == "task_scheduled" {
		_, task.ServiceDependencyEvidence = s.serviceCycleReadiness(prior)
		task.Status = "scheduled"
		task.LastScheduledSeq = seq
		task.AdmittedRunID = r.ID
		task.WaitingFor = nil
	}
	if kind == "claims_acquired" || kind == "task_scheduled" || kind == "run_started" {
		found := false
		for _, runID := range task.Runs {
			if runID == r.ID {
				found = true
				break
			}
		}
		if !found {
			task.Runs = append(task.Runs, r.ID)
		}
	}
	if prior != nil && prior.Status == "blocked" && status == "queued" && c.Status == "" && c.Run == nil {
		task.Status = "blocked"
	}
	if prior != nil && reflect.DeepEqual(task, *prior) {
		return nil, ""
	}
	task.UpdatedAt = at
	effect := "task_" + task.Status
	if c.Run != nil {
		effect = "task_enqueued"
	}
	return &task, effect
}
func (s *Store) taskStates() map[string]workpool.State {
	out := map[string]workpool.State{}
	for id, t := range s.tasks {
		out[id] = workpool.State{ID: id, Status: t.Status, RunID: t.CurrentRunID, Hash: t.Head.Hash, Seq: t.Head.Seq, Sample: t.Sample}
	}
	return out
}
func (s *Store) readiness(t *Task, states map[string]workpool.State) workpool.Readiness {
	if t.AdmittedRunID == t.CurrentRunID {
		return workpool.Readiness{Ready: true, Matched: append([]workpool.Witness(nil), t.DependencyEvidence...)}
	}
	return workpool.Evaluate(t.Scheduling, states)
}
func (s *Store) activePools(ids []string) map[string]int {
	active := map[string]int{}
	for _, id := range ids {
		if r := s.runs[id]; r != nil {
			if t := s.tasks[r.TaskID]; t != nil {
				active[t.Scheduling.Pool]++
			}
		}
	}
	return active
}

// ReadyRuns computes dependency eligibility before applying pool disciplines.
// The dispatcher revalidates admission under the same locks when taking claims.
type ReadyRun struct {
	Run    Run
	Policy workpool.Pool
}

func (s *Store) ReadyRuns(activeIDs []string) ([]ReadyRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	states := s.taskStates()
	active := s.activePools(activeIDs)
	activeRuns := map[string]bool{}
	for _, id := range activeIDs {
		activeRuns[id] = true
	}
	pools := s.configuredPools()
	byPool := map[string][]workpool.Candidate{}
	turns := map[string]int64{}
	ids := []string{}
	for id := range s.tasks {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		t := s.tasks[id]
		turns[t.Scheduling.Pool] = max(turns[t.Scheduling.Pool], t.LastScheduledSeq)
		r := s.runs[t.CurrentRunID]
		if r == nil || r.Status != "queued" || r.Sample || activeRuns[r.ID] {
			continue
		}
		if deadline := t.Scheduling.Deadline; deadline != nil && !time.Now().Before(*deadline) {
			if err := s.commit(r.ID, "effect", "task_deadline_expired", change{Status: "failed", Stage: "Task deadline expired", Summary: "The absolute task deadline expired before dispatch. No new execution was started."}); err != nil {
				return nil, err
			}
			continue
		}
		ready := s.readiness(t, states)
		status := "blocked"
		summary := "Waiting for task dependencies."
		waiting := ready.Waiting
		if ready.Ready {
			status = "queued"
			summary = "Task dependencies are satisfied. Waiting for a pool slot."
			waiting = nil
		}
		serviceReady := s.serviceCanDispatch(t, activeIDs)
		stage := map[bool]string{true: "Ready to start", false: "Waiting for dependencies"}[ready.Ready]
		if ready.Ready && !serviceReady {
			status = "blocked"
			stage = "Waiting for service capacity"
			summary = "Another instance holds this service's execution capacity."
			waiting = []string{"service:" + strings.TrimPrefix(t.Source, "trigger:")}
			if reason := s.serviceReplacementWait(t, activeIDs); reason != "" {
				stage = "Waiting for service replacement"
				summary = "Replacement waits for the previous Task to stop and its execution claims to be released."
				waiting = []string{reason}
			}
			if !s.serviceAbortAllowsTask(t) {
				stage = "Service aborted"
				summary = "The service abort policy is withholding new execution."
				waiting = []string{"service-abort:" + strings.TrimPrefix(t.Source, "trigger:")}
			}
			if !s.serviceBreakerAllowsTask(t, activeIDs, time.Now().UTC()) {
				stage = "Waiting for service circuit recovery"
				summary = "The service circuit breaker is withholding new execution."
				waiting = []string{"service-breaker:" + strings.TrimPrefix(t.Source, "trigger:")}
			}
			if s.lifecycleTaskHeld(t) {
				stage = "Service stopped"
				summary = "A service lifecycle operation is withholding execution."
				waiting = []string{"service-stop:" + strings.TrimPrefix(t.Source, "trigger:")}
			}
			if dependencies, _ := s.serviceCycleReadiness(t); len(dependencies) > 0 {
				stage = "Waiting for service dependencies"
				summary = "Prerequisite services must succeed in this Task's bound scheduler cycle."
				waiting = dependencies
			}
		}
		if t.Status != status || !reflect.DeepEqual(t.WaitingFor, waiting) {
			updated := cloneTask(t)
			updated.Status = status
			updated.WaitingFor = waiting
			updated.UpdatedAt = time.Now().UTC()
			if status == "queued" && t.Status == "blocked" {
				updated.ReadyAt = updated.UpdatedAt
			}
			if err := s.commit(r.ID, "effect", "task_"+status, change{Task: &updated, TaskEffect: "task_" + status, Stage: stage, Summary: summary}); err != nil {
				return nil, err
			}
			t = s.tasks[id]
		}
		if ready.Ready && serviceReady && workpool.Available(t.Scheduling.Pool, pools, active) {
			byPool[t.Scheduling.Pool] = append(byPool[t.Scheduling.Pool], workpool.Candidate{ID: r.ID, Request: t.Scheduling, ReadyAt: t.ReadyAt, SoftScore: ready.SoftScore, PriorityAdjustment: taskServiceAdjustment(t)})
		}
	}
	order := []string{}
	for id := range byPool {
		order = append(order, id)
	}
	sort.Slice(order, func(i, j int) bool {
		if turns[order[i]] != turns[order[j]] {
			return turns[order[i]] < turns[order[j]]
		}
		return order[i] < order[j]
	})
	out := []ReadyRun{}
	for _, pool := range order {
		for _, candidate := range workpool.Order(pools[pool], byPool[pool], time.Now().UTC()) {
			out = append(out, ReadyRun{Run: cloneRun(s.runs[candidate.ID]), Policy: pools[pool]})
		}
	}
	return out, nil
}
func (s *Store) Schedule(id string, activeIDs []string) (bool, error) {
	return s.schedule(id, activeIDs, nil)
}
func (s *Store) ScheduleSelected(id string, activeIDs []string, policy workpool.Pool) (bool, error) {
	return s.schedule(id, activeIDs, &policy)
}
func (s *Store) schedule(id string, activeIDs []string, expected *workpool.Pool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return false, ErrNotFound
	}
	if r.Status != "queued" || r.Sample {
		return false, ErrConflict
	}
	task := s.tasks[r.TaskID]
	if task == nil || task.CurrentRunID != id {
		return false, ErrConflict
	}
	if expected != nil && s.configuredPools()[task.Scheduling.Pool] != *expected {
		return false, nil
	}
	ready := s.readiness(task, s.taskStates())
	if !ready.Ready || !s.serviceCanDispatch(task, activeIDs) || !workpool.Available(task.Scheduling.Pool, s.configuredPools(), s.activePools(activeIDs)) {
		return false, nil
	}
	if task.AdmittedRunID != id {
		bound, err := s.bindDependencyInputs(task, ready.Matched)
		if err != nil {
			return false, s.commit(id, "effect", "dependency_input_unavailable", change{Status: "failed", Stage: "Dependency input unavailable", Summary: err.Error()})
		}
		ready.Matched = bound
	}
	return s.acquireClaims(id, ready.Matched)
}
