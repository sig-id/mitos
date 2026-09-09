package studio

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mitos/internal/service"
	"mitos/internal/workpool"
)

func TestServicePriorityQueueBindingOrderAndReplay(t *testing.T) {
	s := testStore(t)
	apps, _ := LoadApps("")
	e := &Engine{Store: s, Apps: apps, Root: t.TempDir()}
	c := ServiceCatalog{Config: service.Config{}, Work: map[string]Brief{}}
	for _, id := range []string{"low", "normal", "high", "critical"} {
		c.Config.Units = append(c.Config.Units, service.Unit{ID: id, Scope: "project", Trigger: "manual", Concurrency: service.Concurrency{Priority: id, IfRunning: "queue"}})
		c.Config.Intent.Enable = append(c.Config.Intent.Enable, id)
		c.Work[id] = testBrief(e.Root)
	}
	v, err := e.ConfigureServices(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	runs := map[string]Run{}
	for _, id := range []string{"low", "normal", "high", "critical"} {
		runs[id], err = e.TriggerService(id, "one")
		if err != nil {
			t.Fatal(err)
		}
	}
	ready, err := s.ReadyRuns(nil)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range []string{"critical", "high", "normal", "low"} {
		if ready[i].Run.ID != runs[id].ID {
			t.Fatal("priority order", ready)
		}
		task, _ := s.Task(runs[id].TaskID)
		if task.ServiceQueue == nil || task.ServiceQueue.Priority != id || task.ServiceQueue.Revision != v.Catalog.Revision || task.Scheduling.Priority != 0 {
			t.Fatal("missing frozen queue policy", task)
		}
		var created change
		_ = json.Unmarshal(runs[id].Events[0].Data, &created)
		if !reflect.DeepEqual(created.Task.ServiceQueue, created.ServiceCall.Queue) {
			t.Fatal("binding not atomic with creation")
		}
	}
	// Existing receipts keep their saved preference when the catalog changes.
	c = v.Catalog
	c.Config.Units[0].Concurrency.Priority = "critical"
	edited, err := e.ConfigureServices(c, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := e.TriggerService("low", "one")
	if err != nil || duplicate.ID != runs["low"].ID {
		t.Fatal("idempotency changed with priority")
	}
	newer, err := e.TriggerService("low", "two")
	if err != nil {
		t.Fatal(err)
	}
	newTask, _ := s.Task(newer.TaskID)
	oldTask, _ := s.Task(runs["low"].TaskID)
	if newTask.ServiceQueue.Priority != "critical" || newTask.ServiceQueue.Revision != edited.Catalog.Revision || oldTask.ServiceQueue.Priority != "low" {
		t.Fatal("priority edit altered prior receipt")
	}
	// Priority never bypasses an unsatisfied dependency or service capacity.
	ok, err := s.Schedule(runs["critical"].ID, nil)
	if err != nil || !ok {
		t.Fatal(err)
	}
	blocked, err := e.TriggerService("critical", "queued")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err = s.Schedule(blocked.ID, nil); err != nil || ok {
		t.Fatal("critical bypassed service capacity")
	}
	before := s.Tasks()
	view, _ := s.Services()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	e.Store = replay
	after, _ := replay.Services()
	if !reflect.DeepEqual(view, after) || !reflect.DeepEqual(before, replay.Tasks()) {
		t.Fatal("priority binding did not replay")
	}
	live, err := ReportAutonomy(replay.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(replay.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("priority broke metrics", err)
	}
}
func TestServicePriorityAllOriginsAndRetryPreservation(t *testing.T) {
	for _, origin := range []string{"manual", "interval", "cron", "event", "continuous"} {
		t.Run(origin, func(t *testing.T) {
			e, s, v := continuousEngine(t)
			c := v.Catalog
			c.Config.Units[0].Concurrency.Priority = "high"
			unit := &c.Config.Units[0]
			unit.Trigger = origin
			unit.Schedule = nil
			switch origin {
			case "interval":
				unit.Schedule = &service.Schedule{Type: "interval", Interval: 60}
			case "cron":
				unit.Schedule = &service.Schedule{Type: "cron", Cron: "* * * * *"}
			case "continuous":
				unit.Schedule = &service.Schedule{Type: "continuous"}
			case "event":
				unit.Schedule = &service.Schedule{Type: "event", Trigger: service.EventPatterns{"service.succeeded:producer"}}
				c.Config.Units = append(c.Config.Units, service.Unit{ID: "producer", Scope: "project", Trigger: "manual"})
				c.Config.Intent.Enable = []string{"producer"}
				c.Work["producer"] = testBrief(e.Root)
			}
			retries := 1
			c.Config.Units[0].RestartPolicy = service.RestartPolicy{OnFailure: "retry", MaxRetries: &retries, Backoff: service.Backoff{Initial: 1}}
			v, err := e.ConfigureServices(c, v.Catalog.Revision)
			if err != nil {
				t.Fatal(err)
			}
			var run Run
			switch origin {
			case "manual":
				run, err = e.TriggerService("inventory", "one")
			case "interval", "cron":
				err = e.scheduleServices(*v.States[0].NextAt, false)
			case "continuous":
				err = e.reconcileContinuousServices(time.Now())
			case "event":
				finishEventService(t, e, "producer", "one")
				err = e.reconcileServiceEvents()
			}
			if err != nil {
				t.Fatal(err)
			}
			if run.ID == "" {
				for _, task := range s.Tasks() {
					if task.Source == "trigger:inventory" {
						run, _ = s.Get(task.CurrentRunID)
					}
				}
			}
			task, _ := s.Task(run.TaskID)
			if task.ServiceQueue == nil || task.ServiceQueue.Priority != "high" {
				t.Fatal("origin missing priority", origin, task)
			}
			plan := failServiceAttempt(t, s, run.ID)
			c = v.Catalog
			c.Config.Units[0].Concurrency.Priority = "low"
			if _, err = e.ConfigureServices(c, v.Catalog.Revision); err != nil {
				t.Fatal(err)
			}
			if err = e.reconcileServiceRetries(plan.DueAt); err != nil {
				t.Fatal(err)
			}
			retried, _ := s.Task(run.TaskID)
			if retried.CurrentRunID == run.ID || !reflect.DeepEqual(retried.ServiceQueue, task.ServiceQueue) {
				t.Fatal("retry rebound priority")
			}
		})
	}
}
func TestServicePriorityPendingEventFreezesAllocationPreference(t *testing.T) {
	e, s, v := eventEngine(t)
	c := v.Catalog
	c.Config.Units[1].Concurrency.Priority = "high"
	v, err := e.ConfigureServices(c, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	finishEventService(t, e, "producer", "one")
	c = v.Catalog
	c.Config.Units[1].Concurrency.Priority = "low"
	edited, err := e.ConfigureServices(c, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	for _, task := range s.Tasks() {
		if task.Source == "trigger:consumer" {
			if task.ServiceQueue == nil || task.ServiceQueue.Priority != "high" || task.ServiceQueue.Revision != v.Catalog.Revision {
				t.Fatal("pending event adopted newer preference")
			}
			run, _ := s.Get(task.CurrentRunID)
			var change change
			_ = json.Unmarshal(run.Events[0].Data, &change)
			if change.ServiceCall.Revision != edited.Catalog.Revision || change.ServiceCall.Queue.Revision != v.Catalog.Revision {
				t.Fatal("intake/allocation revision evidence lost")
			}
			return
		}
	}
	t.Fatal("missing event consumer")
}
func TestServicePriorityDoesNotChangeFIFOOrDependencyReadiness(t *testing.T) {
	e, s, v := intervalEngine(t, "queue")
	c := v.Catalog
	c.Config.Units[0].Trigger = "manual"
	c.Config.Units[0].Schedule = nil
	b := c.Work["inventory"]
	b.Scheduling.Pool = "batch"
	c.Work["inventory"] = b
	v, err := e.ConfigureServices(c, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	first, err := e.TriggerService("inventory", "first")
	if err != nil {
		t.Fatal(err)
	}
	c = v.Catalog
	c.Config.Units[0].Concurrency.Priority = "critical"
	if _, err = e.ConfigureServices(c, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	second, err := e.TriggerService("inventory", "second")
	if err != nil {
		t.Fatal(err)
	}
	ready, err := s.ReadyRuns(nil)
	if err != nil || ready[0].Run.ID != first.ID {
		t.Fatal("service priority replaced FIFO", err)
	}
	blockedBrief := testBrief(e.Root)
	blockedBrief.Scheduling = workpool.Request{Priority: 1000, Deps: []workpool.Dependency{{From: second.TaskID, Guard: "done"}}}
	blocked, err := s.Create(blockedBrief, "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	ready, err = s.ReadyRuns(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range ready {
		if candidate.Run.ID == blocked.ID {
			t.Fatal("priority bypassed dependencies")
		}
	}
}

func TestServicePriorityLegacyPendingDeliveryRetainsZeroAdjustment(t *testing.T) {
	e, s, v := eventEngine(t)
	finishEventService(t, e, "producer", "one")
	directory := s.Dir()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "events.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte("\n"))
	var rewritten bytes.Buffer
	previous := ""
	for _, line := range lines {
		var event Event
		var c change
		if err = json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(event.Data, &c); err != nil {
			t.Fatal(err)
		}
		if c.ServiceCall != nil {
			c.ServiceCall.Queue = nil
		}
		if c.Task != nil {
			c.Task.ServiceQueue = nil
		}
		if c.ServiceEmission != nil {
			for i := range c.ServiceEmission.Recipients {
				c.ServiceEmission.Recipients[i].Queue = nil
			}
		}
		event.Data, _ = json.Marshal(c)
		event.Previous = previous
		event.Hash = eventHash(event)
		previous = event.Hash
		encoded, _ := json.Marshal(event)
		rewritten.Write(encoded)
		rewritten.WriteByte('\n')
	}
	if err = os.WriteFile(path, rewritten.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	e.Store = replay
	c := v.Catalog
	c.Config.Units[1].Concurrency.Priority = "critical"
	if _, err = e.ConfigureServices(c, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	for _, task := range replay.Tasks() {
		if task.Source == "trigger:consumer" {
			if task.ServiceQueue == nil || task.ServiceQueue.Priority != "normal" || task.ServiceQueue.Adjustment != 0 {
				t.Fatal("legacy event acquired new priority", task.ServiceQueue)
			}
			if _, err = AuditAutonomyLogs(directory); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatal("no legacy consumer")
}
