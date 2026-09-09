package studio

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mitos/internal/service"
)

func eventUnit(id string, patterns ...string) service.Unit {
	return service.Unit{ID: id, Scope: "project", Trigger: "event", Schedule: &service.Schedule{Type: "event", Trigger: service.EventPatterns(patterns)}}
}
func eventEngine(t *testing.T) (*Engine, *Store, ServiceView) {
	t.Helper()
	s := testStore(t)
	apps, _ := LoadApps("")
	e := &Engine{Store: s, Apps: apps, Root: t.TempDir()}
	c := ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "producer", Scope: "project", Trigger: "manual"}, eventUnit("consumer", "service.succeeded:producer", "service.completed:producer")}, Intent: service.Intent{Enable: []string{"producer", "consumer"}}}, Work: map[string]Brief{"producer": testBrief(e.Root), "consumer": testBrief(e.Root)}}
	v, err := e.ConfigureServices(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func finishEventService(t *testing.T, e *Engine, id, key string) Run {
	t.Helper()
	r, err := e.TriggerService(id, key)
	if err != nil {
		t.Fatal(err)
	}
	finishEventRun(t, e.Store, r.ID)
	r, _ = e.Store.Get(r.ID)
	return r
}
func finishEventRun(t *testing.T, s *Store, id string) {
	t.Helper()
	if ok, err := s.Schedule(id, nil); err != nil || !ok {
		t.Fatal("service schedule", err)
	}
	if err := s.Complete(id, "Completed", true); err != nil {
		t.Fatal(err)
	}
}
func TestServiceEventOutboxAtomicReplayDedupAndProvenance(t *testing.T) {
	e, s, _ := eventEngine(t)
	producer := finishEventService(t, e, "producer", "one")
	terminal := producer.Events[len(producer.Events)-1]
	var c change
	_ = json.Unmarshal(terminal.Data, &c)
	if c.ServiceEmission == nil || len(c.ServiceEmission.Recipients) != 1 || len(c.ServiceEmission.Recipients[0].Matched) != 2 || c.ServiceEmission.Recipients[0].Status != "pending" {
		t.Fatal("terminal effect did not atomically allocate one delivery for overlapping matches")
	}
	if _, err := e.TriggerService("consumer", "manual-overlap"); err == nil {
		t.Fatal("pending event delivery did not reserve service capacity")
	}
	before, _ := s.Services()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	e.Store = replay
	restored, _ := replay.Services()
	if !reflect.DeepEqual(before, restored) {
		t.Fatal("outbox did not replay")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- e.reconcileServiceEvents() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(replay.Tasks()) != 2 {
		t.Fatal("event delivery duplicated Task")
	}
	var consumer Task
	for _, task := range replay.Tasks() {
		if task.Source == "trigger:consumer" {
			consumer = task
		}
	}
	cause := consumer.ServiceEvent
	if cause == nil || cause.Seq != terminal.Seq || cause.Hash != terminal.Hash || cause.TaskID != producer.TaskID || cause.RunID != producer.ID || cause.Depth != 1 || cause.RootSeq != terminal.Seq || cause.RootHash != terminal.Hash {
		t.Fatal("event cause not bound to exact source effect")
	}
	run, _ := replay.Get(consumer.CurrentRunID)
	var creation change
	_ = json.Unmarshal(run.Events[0].Data, &creation)
	if creation.ServiceDelivery == nil || creation.ServiceDelivery.Recipient.Status != "enqueued" || creation.ServiceCall.Origin != "event" || creation.ServiceDelivery.RunID != run.ID {
		t.Fatal("receipt and outbox consumption not atomic")
	}
	finishEventRun(t, replay, run.ID)
	live, err := ReportAutonomy(replay.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(replay.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("event records broke metric equivalence", err)
	}
	if err = e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	if len(replay.Tasks()) != 2 {
		t.Fatal("consumed event redelivered")
	}
}
func TestServiceEventSubscriptionChangesSuppressionAndRejectedWork(t *testing.T) {
	for _, scenario := range []string{"disabled", "patterns", "work", "unrelated", "missing-app"} {
		t.Run(scenario, func(t *testing.T) {
			e, s, v := eventEngine(t)
			finishEventService(t, e, "producer", "one")
			switch scenario {
			case "disabled":
				v.Catalog.Config.Intent.Disable = []string{"consumer"}
			case "patterns":
				v.Catalog.Config.Units[1].Schedule.Trigger = service.EventPatterns{"service.failed:producer"}
			case "work":
				b := v.Catalog.Work["consumer"]
				b.Title = "Changed work"
				v.Catalog.Work["consumer"] = b
			case "unrelated":
				v.Catalog.Config.Units[1].Description = "Unrelated description"
			case "missing-app":
				e.Apps = nil
			}
			if scenario != "missing-app" {
				if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.reconcileServiceEvents(); err != nil {
				t.Fatal(err)
			}
			count := 1
			if scenario == "unrelated" {
				count = 2
			}
			if len(s.Tasks()) != count {
				t.Fatal("delivery did not revalidate subscription/work")
			}
			if s.pendingServiceEvents("consumer") != 0 {
				t.Fatal("delivery remained pending")
			}
			if _, err := AuditAutonomyLogs(s.Dir()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestServiceEventDoesNotReplayHistoryOrEmitIntermediateRetryFailures(t *testing.T) {
	e, s, v := eventEngine(t)
	v.Catalog.Config.Intent.Disable = []string{"consumer"}
	v, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	finishEventService(t, e, "producer", "before-subscription")
	v.Catalog.Config.Intent.Disable = nil
	v.Catalog.Config.Units[1].Schedule.Trigger = service.EventPatterns{"service.failed:producer"}
	count := 1
	v.Catalog.Config.Units[0].RestartPolicy = service.RestartPolicy{OnFailure: "retry", MaxRetries: &count, Backoff: service.Backoff{Initial: 1}}
	if _, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileServiceEvents(); err != nil || len(s.Tasks()) != 1 {
		t.Fatal("new subscription replayed history", err)
	}
	run, err := e.TriggerService("producer", "retry-root")
	if err != nil {
		t.Fatal(err)
	}
	plan := failServiceAttempt(t, s, run.ID)
	if s.pendingServiceEvents("consumer") != 0 {
		t.Fatal("intermediate retry failure emitted final failure event")
	}
	if err = e.reconcileServiceRetries(plan.DueAt); err != nil {
		t.Fatal(err)
	}
	task, _ := s.Task(run.TaskID)
	exhausted := failServiceAttempt(t, s, task.CurrentRunID)
	if exhausted.Status != "exhausted" || s.pendingServiceEvents("consumer") != 1 {
		t.Fatal("retry exhaustion did not emit final Task failure")
	}
	if err = e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	if len(s.Tasks()) != 3 {
		t.Fatal("failure delivery did not create one downstream Task")
	}
}
func TestServiceEventFeedbackDepthLimitAndReservedSelfSlot(t *testing.T) {
	e, s, v := eventEngine(t)
	v.Catalog.Config.Units = []service.Unit{eventUnit("loop", "service.succeeded:loop")}
	v.Catalog.Config.Intent = service.Intent{Enable: []string{"loop"}}
	v.Catalog.Work = map[string]Brief{"loop": testBrief(e.Root)}
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	finishEventService(t, e, "loop", "seed")
	for depth := 1; depth <= serviceEventDepth; depth++ {
		if err := e.reconcileServiceEvents(); err != nil {
			t.Fatal(err)
		}
		state, _ := s.Services()
		id := state.States[0].LastRunID
		run, _ := s.Get(id)
		task, _ := s.Task(run.TaskID)
		if task.ServiceEvent == nil || task.ServiceEvent.Depth != depth {
			t.Fatal("event depth reset")
		}
		finishEventRun(t, s, id)
	}
	if err := e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Services()
	if len(s.Tasks()) != serviceEventDepth+1 || s.pendingServiceEvents("loop") != 0 || state.States[0].EventsSkipped != 1 || !strings.Contains(state.States[0].LastEvent.Recipient.Detail, "depth") {
		t.Fatal("feedback depth limit failed")
	}
}
func TestServiceEventFanoutBudgetIsSharedAcrossBranches(t *testing.T) {
	e, s, v := eventEngine(t)
	v.Catalog.Config.Units = []service.Unit{{ID: "producer", Scope: "project", Trigger: "manual"}, eventUnit("middle-a", "service.succeeded:producer"), eventUnit("middle-b", "service.succeeded:producer")}
	v.Catalog.Config.Intent = service.Intent{Enable: []string{"producer", "middle-a", "middle-b"}}
	v.Catalog.Work = map[string]Brief{"producer": testBrief(e.Root), "middle-a": testBrief(e.Root), "middle-b": testBrief(e.Root)}
	for i := 0; i < 63; i++ {
		id := fmt.Sprintf("leaf-%02d", i)
		v.Catalog.Config.Units = append(v.Catalog.Config.Units, eventUnit(id, "service.succeeded:middle-*"))
		v.Catalog.Config.Intent.Enable = append(v.Catalog.Config.Intent.Enable, id)
		v.Catalog.Work[id] = testBrief(e.Root)
	}
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	producer := finishEventService(t, e, "producer", "seed")
	terminal := producer.Events[len(producer.Events)-1]
	if err := e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	middle := []string{}
	for _, task := range s.Tasks() {
		if strings.HasPrefix(task.Source, "trigger:middle-") {
			middle = append(middle, task.CurrentRunID)
		}
	}
	for _, id := range middle {
		finishEventRun(t, s, id)
	}
	if s.serviceEventRootUsed[eventRootKey(terminal.Seq, terminal.Hash)] != serviceEventBudget {
		t.Fatal("branch fanout reset root budget")
	}
	if err := e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	if len(s.Tasks()) != serviceEventBudget+1 {
		t.Fatal("causal root exceeded task budget")
	}
}
func TestServiceEventWorkerReceivesExactCause(t *testing.T) {
	root := t.TempDir()
	apps, _ := LoadApps("")
	consumer := executionApp(t, `cat; printf result > result.txt`)
	consumer.ID = "consume"
	apps = append(apps, consumer)
	e, s := testEngine(t, root, apps)
	c := ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "producer", Scope: "project", Trigger: "manual"}, {ID: "consumer", Scope: "project", Triggers: &service.Triggers{OnComplete: []string{"producer"}}}}, Intent: service.Intent{Enable: []string{"producer", "consumer"}}}, Work: map[string]Brief{"producer": testBrief(root), "consumer": testBrief(root)}}
	b := c.Work["consumer"]
	b.AppID = consumer.ID
	c.Work["consumer"] = b
	if _, err := e.ConfigureServices(c, 0); err != nil {
		t.Fatal(err)
	}
	producer, err := e.TriggerService("producer", "one")
	if err != nil {
		t.Fatal(err)
	}
	producer = waitRecovery(t, s, producer.ID)
	if producer.Status != "done" {
		t.Fatal(producer.Summary)
	}
	var task Task
	end := time.Now().Add(20 * time.Second)
	for time.Now().Before(end) {
		for _, candidate := range s.Tasks() {
			if candidate.Source == "trigger:consumer" {
				task = candidate
			}
		}
		if task.ID != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if task.ID == "" {
		t.Fatal("event drainer did not submit consumer")
	}
	result := waitRecovery(t, s, task.CurrentRunID)
	if result.Status != "done" {
		t.Fatal(result.Summary)
	}
	data, err := s.Artifact(result.ID, "output.txt")
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Cause *ServiceEventCause `json:"service_event"`
	}
	if json.Unmarshal(data, &input) != nil || !reflect.DeepEqual(input.Cause, task.ServiceEvent) || input.Cause.RunID != producer.ID {
		t.Fatal("worker cause was missing or unbound")
	}
}

func TestServiceEventBreakerProjectionAndCancellation(t *testing.T) {
	e, s, v := eventEngine(t)
	loop := eventUnit("loop", "service.failed:loop")
	loop.CircuitBreaker = service.BreakerConfig{Enabled: true, Threshold: 1, Cooldown: 300}
	v.Catalog.Config.Units = []service.Unit{loop, eventUnit("cleanup", "service.failed:loop")}
	v.Catalog.Config.Intent = service.Intent{Enable: []string{"loop", "cleanup"}}
	v.Catalog.Work = map[string]Brief{"loop": testBrief(e.Root), "cleanup": testBrief(e.Root)}
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	run, err := e.TriggerService("loop", "seed")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(run.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	if err = s.Transition(run.ID, "run_failed", "failed", "Failed", "Fixture failure"); err != nil {
		t.Fatal(err)
	}
	if s.pendingServiceEvents("loop") != 0 || s.pendingServiceEvents("cleanup") != 1 {
		t.Fatal("newly opened circuit did not gate self-delivery while preserving cleanup")
	}
	if err = e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	for _, task := range s.Tasks() {
		if task.Source == "trigger:cleanup" {
			if err = e.Cancel(task.CurrentRunID); err != nil {
				t.Fatal(err)
			}
			cancelled, _ := s.Get(task.CurrentRunID)
			var c change
			_ = json.Unmarshal(cancelled.Events[len(cancelled.Events)-1].Data, &c)
			if c.ServiceEmission != nil {
				t.Fatal("cancellation emitted success/failure completion")
			}
		}
	}
}
func TestServiceEventCauseSurvivesConsumerRetryAndConsumedReplay(t *testing.T) {
	e, s, v := eventEngine(t)
	retries := 1
	v.Catalog.Config.Units[1].RestartPolicy = service.RestartPolicy{OnFailure: "retry", MaxRetries: &retries, Backoff: service.Backoff{Initial: 1}}
	v.Catalog.Config.Units = append(v.Catalog.Config.Units, eventUnit("leaf", "service.succeeded:consumer"))
	v.Catalog.Config.Intent.Enable = append(v.Catalog.Config.Intent.Enable, "leaf")
	v.Catalog.Work["leaf"] = testBrief(e.Root)
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	producer := finishEventService(t, e, "producer", "seed")
	terminal := producer.Events[len(producer.Events)-1]
	if err := e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	var consumer Task
	for _, task := range s.Tasks() {
		if task.Source == "trigger:consumer" {
			consumer = task
		}
	}
	plan := failServiceAttempt(t, s, consumer.CurrentRunID)
	if err := e.reconcileServiceRetries(plan.DueAt); err != nil {
		t.Fatal(err)
	}
	retry, _ := s.Task(consumer.ID)
	if !reflect.DeepEqual(consumer.ServiceEvent, retry.ServiceEvent) {
		t.Fatal("automatic retry reset event provenance")
	}
	finishEventRun(t, s, retry.CurrentRunID)
	if err := e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	for _, task := range s.Tasks() {
		if task.Source == "trigger:leaf" {
			if task.ServiceEvent.Depth != 2 || task.ServiceEvent.RootHash != terminal.Hash {
				t.Fatal("descendant received new root after consumer retry")
			}
		}
	}
	before, _ := s.Services()
	count := len(s.Tasks())
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	e.Store = reopened
	restored, _ := reopened.Services()
	if !reflect.DeepEqual(before, restored) {
		t.Fatal("consumed outbox did not replay exactly")
	}
	if err = e.reconcileServiceEvents(); err != nil || len(reopened.Tasks()) != count {
		t.Fatal("consumed event delivered again after restart", err)
	}
	if reopened.serviceEventRootUsed[eventRootKey(terminal.Seq, terminal.Hash)] != 2 {
		t.Fatal("root budget reset after replay")
	}
}
