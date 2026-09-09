package studio

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"mitos/internal/service"
)

func customEventEngine(t *testing.T, units ...service.Unit) (*Engine, *Store, ServiceView) {
	t.Helper()
	e, s, v := eventEngine(t)
	c := ServiceCatalog{Config: service.Config{Units: units}, Work: map[string]Brief{}}
	for _, u := range units {
		c.Config.Intent.Enable = append(c.Config.Intent.Enable, u.ID)
		c.Work[u.ID] = testBrief(e.Root)
	}
	v, err := e.ConfigureServices(c, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func TestCustomEventAtomicReplayAndConcurrentIdempotency(t *testing.T) {
	e, s, _ := customEventEngine(t, eventUnit("consumer", "inventory.*", "inventory.ready"))
	var wg sync.WaitGroup
	results := make(chan CustomEventView, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := e.EmitServiceEvent("inventory.ready", "one", []byte(`{ "n":9007199254740993,"a":1 }`))
			results <- v
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var source CustomEventView
	for v := range results {
		if source.Seq != 0 && v.Seq != source.Seq {
			t.Fatal("duplicate source")
		}
		source = v
	}
	if source.Pending != 1 || len(s.Tasks()) != 0 || len(s.List()) != 0 || len(s.CustomEvents()) != 1 {
		t.Fatal("source was not one atomic outbox without a fake Task", source)
	}
	if _, err := e.TriggerService("consumer", "overlap"); err == nil {
		t.Fatal("pending event failed to reserve capacity")
	}
	if _, err := e.EmitServiceEvent("inventory.ready", "one", []byte(`{"a":2}`)); !errors.Is(err, ErrConflict) {
		t.Fatal("changed duplicate accepted", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	e.Store = replay
	restored, err := replay.CustomEvent(source.Seq)
	if err != nil || !reflect.DeepEqual(restored, source) {
		t.Fatal("source replay changed", err)
	}
	if err = e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	tasks := replay.Tasks()
	if len(tasks) != 1 {
		t.Fatal("wrong subscriber count")
	}
	cause := tasks[0].ServiceEvent
	if cause == nil || cause.Custom == nil || cause.Seq != source.Seq || cause.Hash != source.Hash || cause.RootSeq != source.Seq || cause.Depth != 1 || cause.RunID != "" || cause.TaskID != "" || string(cause.Custom.Payload) != `{"a":1,"n":9007199254740993}` {
		t.Fatal("custom cause lost", cause)
	}
	run, _ := replay.Get(tasks[0].CurrentRunID)
	var creation change
	if json.Unmarshal(run.Events[0].Data, &creation) != nil || creation.ServiceDelivery == nil || creation.ServiceCall == nil || !reflect.DeepEqual(creation.ServiceCall.Event, cause) {
		t.Fatal("Task and delivery not atomic")
	}
	finishEventRun(t, replay, run.ID)
	duplicate, err := e.EmitServiceEvent("inventory.ready", "one", []byte(`{"n":9007199254740993,"a":1}`))
	if err != nil || duplicate.Seq != source.Seq || duplicate.Enqueued != 1 {
		t.Fatal("receipt progress or idempotency changed", err)
	}
	if err = e.reconcileServiceEvents(); err != nil || len(replay.Tasks()) != 1 {
		t.Fatal("consumed event redelivered", err)
	}
	live, err := ReportAutonomy(replay.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(replay.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("custom event changed offline metrics", err)
	}
}
func TestCustomEventUnmatchedAndChangedSubscriptions(t *testing.T) {
	e, s, v := customEventEngine(t, eventUnit("consumer", "later"))
	first, err := e.EmitServiceEvent("inventory.ready", "empty", []byte(`null`))
	if err != nil || len(first.Recipients) != 0 {
		t.Fatal(err)
	}
	v.Catalog.Config.Units[0].Schedule.Trigger = service.EventPatterns{"inventory.*"}
	v, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.EmitServiceEvent("inventory.ready", "empty", []byte(`null`)); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileServiceEvents(); err != nil || len(s.Tasks()) != 0 {
		t.Fatal("subscription replayed old event", err)
	}
	next, err := e.EmitServiceEvent("inventory.ready", "next", []byte(`null`))
	if err != nil || next.Pending != 1 {
		t.Fatal(err)
	}
	v.Catalog.Config.Intent.Disable = []string{"consumer"}
	if _, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	closed, _ := s.CustomEvent(next.Seq)
	if closed.Skipped != 1 || closed.Recipients[0].Status != "suppressed" || len(s.Tasks()) != 0 {
		t.Fatal("disabled allocation not suppressed", closed)
	}
	for _, name := range []string{"worker.completed", "service.succeeded:consumer", "scheduling_event", "run.end"} {
		if _, err = e.EmitServiceEvent(name, "reserved", []byte(`{}`)); err == nil {
			t.Fatal("forged built-in event", name)
		}
	}
	if _, err = e.EmitServiceEvent("valid", "bad-json", []byte(`{"a":1,"a":2}`)); err == nil {
		t.Fatal("ambiguous JSON accepted")
	}
}
func TestCustomEventSiblingDependencies(t *testing.T) {
	down := eventUnit("a-down", "inventory.ready")
	down.DependsOn = []string{"z-up"}
	e, s, _ := customEventEngine(t, down, eventUnit("z-up", "inventory.ready"))
	root, err := e.EmitServiceEvent("inventory.ready", "cycle", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	var upTask, downTask Task
	for _, task := range s.Tasks() {
		if task.Source == "trigger:z-up" {
			upTask = task
		} else {
			downTask = task
		}
	}
	if upTask.ServiceCycle == nil || downTask.ServiceCycle == nil || upTask.ServiceCycle.ID != downTask.ServiceCycle.ID || upTask.ServiceCycle.ID != fmt.Sprintf("event:%d:%s", root.Seq, root.Hash) {
		t.Fatal("custom siblings not in one cycle")
	}
	if ok, err := s.AcquireClaims(downTask.CurrentRunID); err != nil || ok {
		t.Fatal("dependent admitted before upstream", err)
	}
	finishEventRun(t, s, upTask.CurrentRunID)
	finishEventRun(t, s, downTask.CurrentRunID)
	downTask, _ = s.Task(downTask.ID)
	if len(downTask.ServiceDependencyEvidence) != 1 || downTask.ServiceDependencyEvidence[0].TaskID != upTask.ID {
		t.Fatal("wrong dependency witness")
	}
}
func TestCustomEventDescendantsShareRootBudgetAndDepth(t *testing.T) {
	t.Run("budget", func(t *testing.T) {
		units := []service.Unit{eventUnit("middle", "seed")}
		for i := 0; i < 65; i++ {
			units = append(units, eventUnit(fmt.Sprintf("leaf-%02d", i), "service.succeeded:middle"))
		}
		e, s, _ := customEventEngine(t, units...)
		root, err := e.EmitServiceEvent("seed", "root", []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if err = e.reconcileServiceEvents(); err != nil {
			t.Fatal(err)
		}
		finishEventRun(t, s, s.Tasks()[0].CurrentRunID)
		if s.serviceEventRootUsed[eventRootKey(root.Seq, root.Hash)] != 64 {
			t.Fatal("descendants reset root budget")
		}
		if err = e.reconcileServiceEvents(); err != nil {
			t.Fatal(err)
		}
		if len(s.Tasks()) != 64 {
			t.Fatal("root budget not enforced", len(s.Tasks()))
		}
		for _, task := range s.Tasks() {
			if task.Source != "trigger:middle" && (task.ServiceEvent.Custom != nil || task.ServiceEvent.RootSeq != root.Seq || task.ServiceEvent.Depth != 2) {
				t.Fatal("descendant source not its own outcome")
			}
		}
		if err = s.Close(); err != nil {
			t.Fatal(err)
		}
		replay, err := OpenStore(s.Dir())
		if err != nil {
			t.Fatal(err)
		}
		defer replay.Close()
		if replay.serviceEventRootUsed[eventRootKey(root.Seq, root.Hash)] != 64 {
			t.Fatal("budget lost on replay")
		}
	})
	t.Run("depth", func(t *testing.T) {
		e, s, _ := customEventEngine(t, eventUnit("loop", "seed", "service.succeeded:loop"))
		root, err := e.EmitServiceEvent("seed", "root", []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		for depth := 1; depth <= serviceEventDepth; depth++ {
			if err = e.reconcileServiceEvents(); err != nil {
				t.Fatal(err)
			}
			view, _ := s.Services()
			run, _ := s.Get(view.States[0].LastRunID)
			task, _ := s.Task(run.TaskID)
			if task.ServiceEvent.Depth != depth || task.ServiceEvent.RootSeq != root.Seq {
				t.Fatal("root/depth changed")
			}
			finishEventRun(t, s, run.ID)
		}
		if err = e.reconcileServiceEvents(); err != nil || len(s.Tasks()) != serviceEventDepth || s.pendingServiceEvents("loop") != 0 {
			t.Fatal("custom feedback escaped limit", err)
		}
	})
}
