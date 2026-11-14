package studio

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mitos/internal/acceptance"
	"mitos/internal/agentpolicy"
	"mitos/internal/decision"
	"mitos/internal/fleet"
	"mitos/internal/messaging"
	"mitos/internal/service"
	"mitos/internal/workpool"
	"os"
	"path/filepath"
	"sort"
	"syscall"
	"time"
)

type Store struct {
	supervisorVerdicts     map[string]SupervisorVerdict
	supervisionIncidents   map[string]SupervisionIncident
	agentRoutes            map[string]AgentRoute
	modelEvaluations       map[string]ModelEvaluation
	cooperation            map[string]CooperationRecord
	agentPolicies          map[string]agentpolicy.Policy
	graphs                 map[string]*WorkGraph
	graphCreations         map[string]graphCreation
	messagesOut            map[string]messaging.Outbound
	messagesIn             map[string]messaging.Inbound
	deskState              json.RawMessage
	home                   HomeServer
	agentSessions          map[string]AgentSession
	agentRequests          map[string]AgentRequest
	agentAnswers           map[string]AgentAnswer
	devices                map[string]Device
	deviceTokens           map[string]string
	deviceCommands         map[string]DeviceCommandRecord
	deviceHistory          map[string]decision.Card
	serviceLifecycle       map[int64]*serviceLifecycleState
	serviceLifecycleKeys   map[string]int64
	serviceHolds           map[string]map[int64]bool
	customEvents           map[string]CustomEventSaved
	webhookReceipts        map[string]string
	serviceCycleReceipts   map[string]map[string]string
	replacementVictims     map[string]int
	serviceEventDeliveries map[string]*ServiceEventDelivery
	serviceEventRootUsed   map[string]int
	serviceRestartBindings map[string]*ServiceRestartBinding
	serviceRetryPlans      map[string]*ServiceRetryPlan
	serviceRetryChildren   map[string]*ServiceRetryPlan
	serviceAdmissions      map[string]service.BreakerBinding
	services               *ServiceCatalog
	serviceCalls           map[string]string
	serviceStates          map[string]*ServiceState
	tasks                  map[string]*Task
	pools                  map[string]workpool.Pool
	mu                     storeMutex
	file                   eventLog
	dir                    string
	runs                   map[string]*Run
	seq                    int64
	hash                   string
	writeErr               error
	artifactDirs           map[string]bool
}

func OpenStore(dir string) (*Store, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(abs, "events.jsonl"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("this data directory is already open by another mitos process")
	}
	if err = syncDirectory(abs); err == nil {
		err = syncDirectory(filepath.Dir(abs))
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	s := &Store{file: f, dir: abs, runs: map[string]*Run{}}
	if err = s.replay(); err != nil {
		f.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { s.mu.Lock(); defer s.mu.Unlock(); return s.file.Close() }
func (s *Store) Dir() string  { return s.dir }

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func eventHash(e Event) string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func (s *Store) replay() error {
	r := bufio.NewReader(s.file)
	var offset int64
	for {
		line, err := r.ReadBytes('\n')
		if err == io.EOF {
			// Only an incomplete final record can be discarded. A complete corrupt
			// record is never silently skipped or rewritten.
			if len(line) > 0 {
				if err = s.file.Truncate(offset); err != nil {
					return err
				}
				if err = s.file.Sync(); err != nil {
					return err
				}
			}
			_, err = s.file.Seek(0, io.SeekEnd)
			return err
		}
		if err != nil {
			return err
		}
		var e Event
		if err = json.Unmarshal(line, &e); err != nil {
			return fmt.Errorf("invalid event at byte %d: %w", offset, err)
		}
		if e.Seq != s.seq+1 || e.Previous != s.hash || e.Hash != eventHash(e) {
			return fmt.Errorf("event chain damaged at sequence %d", e.Seq)
		}
		if err = s.apply(e); err != nil {
			return err
		}
		s.seq = e.Seq
		s.hash = e.Hash
		offset += int64(len(line))
	}
}

func (s *Store) apply(e Event) error {
	var c change
	if err := json.Unmarshal(e.Data, &c); err != nil {
		return err
	}
	if c.Graph != nil {
		if err := s.applyGraph(e, c.Graph); err != nil {
			return err
		}
		if e.RunID == "" {
			return nil
		}
	}
	if c.Message != nil {
		if err := s.applyMessage(e, c.Message); err != nil {
			return err
		}
		if c.CustomEvent == nil {
			return nil
		}
	}
	if c.SupervisorVerdict != nil {
		if s.supervisorVerdicts == nil {
			s.supervisorVerdicts = map[string]SupervisorVerdict{}
		}
		s.supervisorVerdicts[c.SupervisorVerdict.Key] = graphClone(*c.SupervisorVerdict)
	}
	if c.SupervisionIncident != nil {
		if s.supervisionIncidents == nil {
			s.supervisionIncidents = map[string]SupervisionIncident{}
		}
		s.supervisionIncidents[c.SupervisionIncident.ID] = graphClone(*c.SupervisionIncident)
	}
	if c.SupervisorVerdict != nil || c.SupervisionIncident != nil {
		return nil
	}
	if c.AgentRoute != nil {
		if s.agentRoutes == nil {
			s.agentRoutes = map[string]AgentRoute{}
		}
		s.agentRoutes[c.AgentRoute.Key] = graphClone(*c.AgentRoute)
		if request, ok := s.agentRequests[c.AgentRoute.Key]; ok {
			for _, saved := range request.Runs {
				if r := s.runs[saved.ID]; r != nil && r.Decision != nil && r.Decision.Resolution == nil {
					r.Summary = "Request routed to " + c.AgentRoute.Route.Resolver + " via " + c.AgentRoute.Route.Source + ": " + c.AgentRoute.Route.Reason
				}
			}
		}
		return nil
	}
	if c.ModelEvaluation != nil {
		if s.modelEvaluations == nil {
			s.modelEvaluations = map[string]ModelEvaluation{}
		}
		s.modelEvaluations[c.ModelEvaluation.ID] = graphClone(*c.ModelEvaluation)
		return nil
	}
	if c.Cooperation != nil {
		if s.cooperation == nil {
			s.cooperation = map[string]CooperationRecord{}
		}
		s.cooperation[c.Cooperation.ProjectID+"/"+c.Cooperation.Message.ID] = graphClone(*c.Cooperation)
		return nil
	}
	if c.AgentPolicy != nil {
		if s.agentPolicies == nil {
			s.agentPolicies = map[string]agentpolicy.Policy{}
		}
		s.agentPolicies[c.AgentPolicy.ProjectID] = graphClone(c.AgentPolicy.Policy)
		return nil
	}
	if c.Home != nil {
		s.home = *c.Home
		return nil
	}
	if c.DeskState != nil {
		return s.applyDesk(e, c.DeskState)
	}
	if c.AgentSession != nil || c.AgentRequest != nil || c.AgentAnswer != nil {
		if err := s.applyAgentEvent(e, c); err != nil {
			return err
		}
		if e.RunID == "" {
			return nil
		}
	}
	if c.Device != nil {
		s.applyDevice(*c.Device)
		return nil
	}
	if c.DeviceCommand != nil {
		if s.deviceCommands == nil {
			s.deviceCommands = map[string]DeviceCommandRecord{}
		}
		s.deviceCommands[c.DeviceCommand.DeviceID+"/"+c.DeviceCommand.Receipt.CommandID] = *c.DeviceCommand
	}
	if c.ServiceLifecycle != nil {
		return s.applyServiceLifecycle(e, c)
	}
	if c.CustomEvent != nil {
		return s.applyCustomEvent(e, c)
	}
	if c.Services != nil {
		if err := validateServiceEvent(e, c); err != nil {
			return err
		}
		s.applyServiceCatalog(c.Services, e, c.ServiceClocks)
		return nil
	}
	if c.ServiceContinuous != nil && e.RunID == "" {
		return s.applyContinuous(e, c)
	}
	if c.ServiceDelivery != nil && e.RunID == "" {
		return s.applyServiceDelivery(e, c)
	}
	if c.ServiceTick != nil {
		if c.Run != nil || c.Task != nil || c.ServiceCall != nil || c.Pool != nil {
			return errors.New("mixed service tick event")
		}
		if err := validateServiceTick(e, c.ServiceTick, false); err != nil {
			return err
		}
		s.applyServiceTick(c.ServiceTick, "")
		return nil
	}
	if c.Pool != nil {
		if err := validatePoolEvent(e, c); err != nil {
			return err
		}
		if s.pools == nil {
			s.pools = workpool.Defaults()
		}
		s.pools[c.Pool.ID] = *c.Pool
		return nil
	}
	if c.Run != nil {
		if s.runs[e.RunID] != nil {
			return errors.New("duplicate run creation")
		}
		s.runs[e.RunID] = c.Run
	}
	r := s.runs[e.RunID]
	if r == nil {
		return fmt.Errorf("event refers to missing run %s", e.RunID)
	}
	if err := s.applyRecovery(e, r, c); err != nil {
		return err
	}
	if c.Claim != nil {
		r.Claim = c.Claim
		r.ClaimConflicts = nil
	}
	if c.ClaimConflicts != nil {
		r.ClaimConflicts = c.ClaimConflicts
	}
	if c.NativeThreadID != "" {
		r.NativeThreadID = c.NativeThreadID
	}
	if c.Workspace != nil {
		r.Workspace = c.Workspace
	}
	if c.Acceptance != nil {
		r.Acceptance = c.Acceptance
	}
	if c.Status != "" {
		r.Status = c.Status
	}
	if c.Stage != "" {
		r.Stage = c.Stage
	}
	if c.Summary != "" {
		r.Summary = c.Summary
	}
	if c.Verified {
		r.Verified = true
	}
	if c.Artifact != nil {
		r.Artifacts = append(r.Artifacts, *c.Artifact)
	}
	if c.DecisionRequest != nil {
		r.PendingDecision = c.DecisionRequest
	}
	if c.DecisionError != "" && r.Decision != nil {
		r.Decision.ApplicationError = c.DecisionError
	}
	if c.Decision != nil {
		r.PendingDecision = nil
		r.Decision = c.Decision
		r.DecisionRewind = nil
	}
	if c.Resolution != nil && r.Decision != nil {
		r.Decision.Resolution = c.Resolution
		r.DecisionRewind = c.ResolutionRewind
	}
	if c.DecisionApplied && r.Decision != nil {
		at := e.At
		r.Decision.AppliedAt = &at
		r.Decision.ResultRunID = c.ResultRunID
		r.Decision.ApplicationError = c.DecisionError
	}
	if c.Choice != "" && r.Decision != nil {
		r.Decision.Choice = c.Choice
		at := e.At
		r.Decision.ResolvedAt = &at
	}
	if r.Decision != nil {
		if s.deviceHistory == nil {
			s.deviceHistory = map[string]decision.Card{}
		}
		s.deviceHistory[r.ID+"/"+r.Decision.ID] = *r.Decision
	}
	if c.GraphAttempt != nil {
		r.GraphAttempt = c.GraphAttempt
	}
	r.UpdatedAt = e.At
	if r.Terminal() && r.FinishedAt.IsZero() {
		r.FinishedAt = e.At
	}
	r.Events = append(r.Events, e)
	if c.ServiceCall != nil {
		if err := validateServiceCall(e, c); err != nil {
			return err
		}
		if s.serviceCalls == nil {
			s.serviceCalls = map[string]string{}
		}
		key := serviceCallIndex(c.ServiceCall.ServiceID, c.ServiceCall.Key, c.ServiceCall.Origin)
		if s.serviceCalls[key] != "" {
			return errors.New("duplicate service trigger key")
		}
		s.serviceCalls[key] = r.ID
		if c.ServiceCall.Tick != nil {
			s.applyServiceTick(c.ServiceCall.Tick, r.ID)
		} else {
			if s.serviceStates == nil {
				s.serviceStates = map[string]*ServiceState{}
			}
			state := s.serviceStates[c.ServiceCall.ServiceID]
			if state == nil {
				state = &ServiceState{ServiceID: c.ServiceCall.ServiceID}
				s.serviceStates[c.ServiceCall.ServiceID] = state
			}
			state.Enqueued++
			state.LastRunID = r.ID
		}
	}
	if s.tasks == nil {
		s.tasks = map[string]*Task{}
	}
	if c.Task != nil {
		if err := s.trackServiceIngress(e, c); err != nil {
			return err
		}
		if err := s.trackLifecycleTask(e, c, s.tasks[c.Task.ID]); err != nil {
			return err
		}
		if err := s.trackServiceCycle(e, c, s.tasks[c.Task.ID]); err != nil {
			return err
		}
		if c.Task.ID != r.TaskID {
			return errors.New("task/run binding mismatch")
		}
		s.trackTaskReplacement(s.tasks[c.Task.ID], -1)
		s.trackTaskReplacement(c.Task, 1)
		c.Task.Head = TaskHead{Seq: e.Seq, Hash: e.Hash}
		s.tasks[c.Task.ID] = c.Task
	} else if prior := s.tasks[r.TaskID]; prior == nil || prior.Legacy {
		// Historical run records acquire only a labelled projection. Replay
		// never adds scheduling Decisions or dependencies to the old log.
		if task, _ := s.taskChange(e.RunID, e.Kind, c, e.At, e.Seq); task != nil {
			task.Legacy = true
			task.Head = TaskHead{Seq: e.Seq, Hash: e.Hash}
			s.tasks[task.ID] = task
		}
	}
	if err := applyServiceCondition(e, r, c); err != nil {
		return err
	}
	if err := applyServiceHealth(e, r, c); err != nil {
		return err
	}
	if err := s.applyServiceBreaker(e, c); err != nil {
		return err
	}
	if err := s.applyServiceRestart(e, c); err != nil {
		return err
	}
	if err := s.applyServiceEmission(e, c); err != nil {
		return err
	}
	if err := s.applyContinuous(e, c); err != nil {
		return err
	}
	return s.applyServiceDelivery(e, c)
}

// commit requires mu. Publish to readers only after the record is synced.
func (s *Store) commit(id, typ, kind string, c change) error {
	e, err := s.prepareEvent(id, typ, kind, c, s.seq+1, s.hash)
	if err != nil {
		return err
	}
	return s.persist(e)
}

// prepareEvent derives an event from the current durable state without changing
// it. Callers batching events must not prepare a dependent state change before
// its predecessor has been applied.
func (s *Store) prepareEvent(id, typ, kind string, c change, seq int64, previous string) (Event, error) {
	if s.writeErr != nil {
		return Event{}, fmt.Errorf("event log unavailable: %w", s.writeErr)
	}
	at := time.Now().UTC()
	if typ == "effect" && c.Task == nil && c.Pool == nil {
		c.Task, c.TaskEffect = s.taskChange(id, kind, c, at, seq)
	}
	if typ == "effect" && c.ServiceHealth == nil {
		c.ServiceHealth = s.serviceHealthAdmission(id, kind)
	}
	if typ == "effect" && c.ServiceCondition == nil {
		c.ServiceCondition = s.serviceConditionAdmission(id, kind)
	}
	if typ == "effect" && c.ServiceBreaker == nil {
		update, err := s.serviceBreakerChange(id, kind, c, at)
		if err != nil {
			return Event{}, err
		}
		c.ServiceBreaker = update
	}
	if typ == "effect" {
		if c.ServiceRestart == nil && c.ServiceRetry == nil {
			c.ServiceRestart, c.ServiceRetry = s.serviceRestartChange(id, kind, c, at)
		}
		if c.ServiceRetry != nil && c.Run == nil {
			c.Task, c.TaskEffect = s.serviceRetryTaskChange(id, c.Task, c.TaskEffect, c.ServiceRetry, at)
		}
	}
	if typ == "effect" && c.ServiceEmission == nil {
		c.ServiceEmission = s.serviceEmissionChange(id, c, at)
	}
	if typ == "effect" && c.ServiceContinuous == nil {
		c.ServiceContinuous = s.continuousChange(id, c, at)
	}
	if c.Graph == nil {
		var err error
		c.Graph, err = s.graphRunChange(id, kind, c, at, seq)
		if err != nil {
			return Event{}, err
		}
	}
	data, err := json.Marshal(c)
	if c.Graph != nil && len(data) > 8<<20 {
		return Event{}, errors.New("graph journal record capacity exceeded")
	}
	if err != nil {
		return Event{}, err
	}
	e := Event{Seq: seq, RunID: id, Type: typ, Kind: kind, At: at, Data: data, Previous: previous}
	e.Hash = eventHash(e)
	return e, nil
}

// persist keeps the writer gate throughout IO, but lets readers access the last
// durable state. Even queued writers cannot prevent those reads. A failed write
// poisons the log; no later action may proceed on an uncertain durable head.
func (s *Store) persist(events ...Event) error {
	if s.writeErr != nil {
		return fmt.Errorf("event log unavailable: %w", s.writeErr)
	}
	var data []byte
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		data = append(data, line...)
		data = append(data, '\n')
	}
	err := s.mu.io(func() error {
		n, err := s.file.Write(data)
		if err == nil && n != len(data) {
			err = io.ErrShortWrite
		}
		if err == nil {
			err = s.file.Sync()
		}
		return err
	})
	if err != nil {
		s.writeErr = err
		return err
	}
	for _, e := range events {
		if err = s.apply(e); err != nil {
			s.writeErr = err
			return err
		}
		s.seq = e.Seq
		s.hash = e.Hash
	}
	return nil
}

func cloneRun(r *Run) Run {
	b, _ := json.Marshal(r)
	var out Run
	_ = json.Unmarshal(b, &out)
	return out
}
func (s *Store) Get(id string) (Run, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[id]
	if r == nil {
		return Run{}, ErrNotFound
	}
	return cloneRun(r), nil
}
func (s *Store) List() []Run {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, cloneRun(r))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out
}

func (s *Store) Create(b Brief, appName, parentID string, sample bool) (Run, error) {
	if err := b.Validate(); err != nil {
		return Run{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.createLocked(b, appName, parentID, sample, nil)
}
func (s *Store) createLocked(b Brief, appName, parentID string, sample bool, call *ServiceCall) (Run, error) {
	return s.createWithRetryLocked(b, appName, parentID, sample, call, nil)
}
func (s *Store) createWithRetryLocked(b Brief, appName, parentID string, sample bool, call *ServiceCall, retry *ServiceRetryPlan) (Run, error) {
	return s.createWithEventLocked(b, appName, parentID, sample, call, retry, nil)
}
func (s *Store) createWithEventLocked(b Brief, appName, parentID string, sample bool, call *ServiceCall, retry *ServiceRetryPlan, delivery *ServiceEventDelivery) (Run, error) {
	if call != nil && s.serviceLifecycleHeld(call.ServiceID) && !s.relaunchCallAllowed(call) {
		return Run{}, errors.New("service lifecycle is stopped")
	}
	taskID := newID("task")
	if parentID != "" {
		p := s.runs[parentID]
		if p == nil {
			return Run{}, ErrNotFound
		}
		taskID = p.TaskID
		if current := s.tasks[taskID]; current != nil {
			if current.GraphID != "" {
				return Run{}, errors.New("graph tasks retry through a graph amendment")
			}
			if s.lifecycleTaskHeld(current) {
				return Run{}, errors.New("service lifecycle is stopped")
			}
			if s.isReplacementVictim(current.ID) {
				return Run{}, errors.New("a service replacement is stopping this Task")
			}
			if !s.continuousAllowsTask(current) {
				return Run{}, errors.New("continuous lifecycle no longer owns this Task; enable the service for fresh work")
			}
			b.Scheduling = current.Scheduling
		}
	}
	if retry != nil {
		deadline := retry.Binding.Deadline
		b.Scheduling.Deadline = &deadline
	}
	if call != nil {
		copy := *call
		call = &copy
		if err := s.prepareServiceCycle(call, delivery); err != nil {
			return Run{}, err
		}
		if len(call.Cycle.DependsOn) > 0 {
			deadline := time.Now().UTC().Add(time.Duration(b.TimeoutMinutes) * time.Minute)
			if b.Scheduling.Deadline == nil || deadline.Before(*b.Scheduling.Deadline) {
				b.Scheduling.Deadline = &deadline
			}
		}
	}
	if err := s.validateTaskRequest(taskID, b.Scheduling, sample); err != nil {
		return Run{}, err
	}
	if call != nil {
		copy := *call
		call = &copy
		if call.Origin != "event" && call.Replacement == nil && s.serviceConcurrency(call.ServiceID).IfRunning == "kill" {
			pending, _ := s.serviceCounts(call.ServiceID, "", nil)
			if pending >= s.serviceConcurrency(call.ServiceID).MaxInstances {
				call.Replacement = s.selectServiceReplacement(call.ServiceID, "")
				if call.Replacement == nil {
					return Run{}, ErrConflict
				}
			}
		}
		if call.Queue == nil {
			call.Queue = s.serviceQueueBinding(call.ServiceID, call.Revision)
		}
	}
	r := Run{Tenant: decision.LocalTenant, ID: newID("local"), TaskID: taskID, ParentID: parentID, Brief: b, AppName: appName, Status: "queued", Stage: "Ready to start", Summary: "The brief is saved. Waiting for an execution slot.", CreatedAt: time.Now().UTC(), Sample: sample}
	if retry != nil {
		copy := *retry
		copy.ChildRunID = r.ID
		retry = &copy
	}
	if delivery != nil {
		copy := cloneEventDelivery(delivery)
		copy.RunID = r.ID
		delivery = &copy
	}
	err := s.commit(r.ID, "effect", "run_created", change{Run: &r, ServiceCall: call, ServiceRetry: retry, ServiceDelivery: delivery})
	if err != nil {
		return Run{}, err
	}
	return cloneRun(s.runs[r.ID]), nil
}

func (s *Store) Transition(id, kind, status, stage, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	if r.Status == "stopping" && status != "cancelled" {
		return ErrConflict
	}
	return s.commit(id, "effect", kind, change{Status: status, Stage: stage, Summary: summary})
}
func (s *Store) RecordDecision(id, kind, summary string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	return s.commit(id, "decision", kind, change{Summary: summary})
}
func (s *Store) Complete(id, summary string, verified bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	if r.Status == "stopping" {
		return ErrConflict
	}
	var claim *fleet.Claim
	if r.Claim != nil && r.Claim.ReleasedAt == nil {
		copy := *r.Claim
		at := time.Now().UTC()
		copy.ReleasedAt = &at
		copy.ReleaseReason = "execution_stopped_and_harvest_finished"
		claim = &copy
	}
	return s.commit(id, "effect", "run_finished", change{Claim: claim, Status: "done", Stage: "Complete", Summary: summary, Verified: verified})
}
func (s *Store) Artifact(id, name string) ([]byte, error) {
	r, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	for _, a := range r.Artifacts {
		if a.Name == name {
			b, err := os.ReadFile(filepath.Join(s.dir, "artifacts", id, name))
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(b)
			if hex.EncodeToString(sum[:]) != a.SHA256 {
				return nil, errors.New("artifact checksum mismatch")
			}
			return b, nil
		}
	}
	return nil, ErrNotFound
}

// RecordEvidence appends observed data without deriving run state from harness
// claims. The canonical record is synced before its producer may advance.
func (s *Store) RecordEvidence(id, kind, summary string, evidence any) error {
	data, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	return s.commit(id, "effect", kind, change{Summary: summary, Evidence: data})
}

func (s *Store) RecordWorkspace(id, kind, summary string, record WorkspaceRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	if record.Mode != "copy" || len(record.BaselineCommit) != 40 || len(record.Fingerprint) != 64 || record.ChangedFiles < 0 {
		return errors.New("invalid workspace record")
	}
	if _, err := hex.DecodeString(record.BaselineCommit); err != nil {
		return errors.New("invalid baseline commit")
	}
	if _, err := hex.DecodeString(record.Fingerprint); err != nil {
		return errors.New("invalid workspace fingerprint")
	}
	if r.Workspace != nil && r.Workspace.BaselineCommit != record.BaselineCommit {
		return errors.New("workspace baseline cannot change during harvest")
	}
	if record.ResultCommit != "" {
		if len(record.ResultCommit) != 40 {
			return errors.New("invalid result commit")
		}
		if _, err := hex.DecodeString(record.ResultCommit); err != nil {
			return errors.New("invalid result commit")
		}
	}
	return s.commit(id, "effect", kind, change{Workspace: &record, Summary: summary})
}
func (s *Store) RecordAcceptance(id string, verdict acceptance.Verdict) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Terminal() {
		return ErrConflict
	}
	if verdict.Verdict != "pass" && verdict.Verdict != "fail" && verdict.Verdict != "unknown" {
		return errors.New("invalid acceptance verdict")
	}
	if r.Workspace == nil || r.Workspace.ResultCommit == "" || r.Workspace.ResultCommit != verdict.WorkspaceCommit {
		return errors.New("acceptance must refer to the harvested commit")
	}
	evidenceFound := false
	for _, artifact := range r.Artifacts {
		if artifact.Name == verdict.EvidenceRef {
			evidenceFound = true
			break
		}
	}
	if !evidenceFound {
		return errors.New("acceptance evidence has not been saved")
	}
	return s.commit(id, "effect", "predicate_evaluated", change{Acceptance: &verdict})
}
