package studio

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"mitos/internal/service"
	"mitos/internal/workpool"
)

func dependencyServices(t *testing.T) (*Engine, *Store, ServiceView) {
	t.Helper()
	apps, _ := LoadApps("")
	e, s := testEngine(t, t.TempDir(), apps)
	e.Close()
	c := serviceCatalog(e.Root)
	c.Config.Targets = nil
	c.Config.Intent = service.Intent{Enable: []string{"upstream", "downstream"}}
	c.Config.Units = []service.Unit{{ID: "upstream", Scope: "project", Trigger: "manual", Concurrency: service.Concurrency{IfRunning: "queue", QueueMax: 10}}, {ID: "downstream", Scope: "project", Trigger: "manual", DependsOn: []string{"upstream"}, Concurrency: service.Concurrency{IfRunning: "queue", QueueMax: 10}}}
	c.Work = map[string]Brief{"upstream": testBrief(e.Root), "downstream": testBrief(e.Root)}
	v, err := e.ConfigureServices(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func cycleTrigger(t *testing.T, e *Engine, id, key, cycle string) Run {
	t.Helper()
	r, err := e.TriggerService(id, key, cycle)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func finishCycleTask(t *testing.T, s *Store, r Run) {
	t.Helper()
	if ok, err := s.Schedule(r.ID, nil); err != nil || !ok {
		t.Fatal("could not schedule", r.ID, ok, err)
	}
	if err := s.Complete(r.ID, "Cycle success", true); err != nil {
		t.Fatal(err)
	}
}
func TestServiceDependenciesUseExactCycleAndAtomicWitness(t *testing.T) {
	e, s, _ := dependencyServices(t)
	old := cycleTrigger(t, e, "upstream", "old", "old")
	finishCycleTask(t, s, old)
	downstream := cycleTrigger(t, e, "downstream", "down", "current")
	if ok, err := s.Schedule(downstream.ID, nil); err != nil || ok {
		t.Fatal("historical success satisfied current cycle", ok, err)
	}
	if ok, err := s.AcquireClaims(downstream.ID); err != nil || ok {
		t.Fatal("direct claims bypassed cycle gate", ok, err)
	}
	if _, err := s.ReadyRuns(nil); err != nil {
		t.Fatal(err)
	}
	blocked, _ := s.Task(downstream.TaskID)
	if !reflect.DeepEqual(blocked.WaitingFor, []string{"service-dependency:upstream"}) || blocked.Scheduling.Deadline == nil {
		t.Fatal("missing bounded cycle wait", blocked.WaitingFor)
	}
	upstream := cycleTrigger(t, e, "upstream", "up", "current")
	finishCycleTask(t, s, upstream)
	source, _ := s.Task(upstream.TaskID)
	if ok, err := s.Schedule(downstream.ID, nil); err != nil || !ok {
		t.Fatal("same-cycle success did not satisfy gate", ok, err)
	}
	admitted, _ := s.Task(downstream.TaskID)
	if len(admitted.ServiceDependencyEvidence) != 1 {
		t.Fatal("missing service evidence")
	}
	w := admitted.ServiceDependencyEvidence[0]
	if w.TaskID != source.ID || w.RunID != upstream.ID || w.Seq != source.Head.Seq || w.Hash != source.Head.Hash {
		t.Fatal("wrong exact success witness", w, source.Head)
	}
	r, _ := s.Get(downstream.ID)
	var c change
	_ = json.Unmarshal(r.Events[len(r.Events)-1].Data, &c)
	if c.Task == nil || len(c.Task.ServiceDependencyEvidence) != 1 {
		t.Fatal("evidence was not atomic with admission")
	}
	if _, err := e.TriggerService("upstream", "duplicate", "current"); err == nil {
		t.Fatal("duplicate service in same cycle accepted")
	}
	duplicate, err := e.TriggerService("upstream", "up", "different")
	if err != nil || duplicate.ID != upstream.ID {
		t.Fatal("original trigger idempotency changed", err)
	}
	// An admitted invocation retains its witness if its prerequisite is later retried.
	retry, err := e.Retry(upstream.ID)
	if err != nil {
		t.Fatal(err)
	}
	_ = retry
	s.mu.RLock()
	waiting, evidence := s.serviceCycleReadiness(s.tasks[downstream.TaskID])
	s.mu.RUnlock()
	if len(waiting) != 0 || !reflect.DeepEqual(evidence, admitted.ServiceDependencyEvidence) {
		t.Fatal("admitted cycle witness changed")
	}
	live, err := ReportAutonomy(s.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(s.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("cycle readers disagree", err)
	}
}
func TestServiceDependenciesDoNotWeakenTaskJoin(t *testing.T) {
	e, s, v := dependencyServices(t)
	control, err := e.Launch(testBrief(e.Root), "")
	if err != nil {
		t.Fatal(err)
	}
	finishCycleTask(t, s, control)
	b := v.Catalog.Work["downstream"]
	b.Scheduling.Deps = []workpool.Dependency{{From: control.TaskID, Guard: "done"}}
	b.Scheduling.Join = workpool.Join{Rule: "any"}
	v.Catalog.Work["downstream"] = b
	if _, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	r := cycleTrigger(t, e, "downstream", "one", "batch")
	if ok, err := s.Schedule(r.ID, nil); err != nil || ok {
		t.Fatal("Task any join bypassed service dependencies", ok, err)
	}
}
func TestServiceDependenciesFailureAndRetryStayInCycle(t *testing.T) {
	e, s, _ := dependencyServices(t)
	down := cycleTrigger(t, e, "downstream", "down", "batch")
	up := cycleTrigger(t, e, "upstream", "up", "batch")
	if ok, err := s.Schedule(up.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	if err := s.Transition(up.ID, "run_failed", "failed", "Failed", "failure"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(down.ID, nil); err != nil || ok {
		t.Fatal("failed prerequisite satisfied dependency", ok, err)
	}
	retry, err := e.Retry(up.ID)
	if err != nil {
		t.Fatal(err)
	}
	finishCycleTask(t, s, retry)
	if ok, err := s.Schedule(down.ID, nil); err != nil || !ok {
		t.Fatal("successful retry did not satisfy original cycle", ok, err)
	}
}
func TestServiceDependenciesReplayAndCatalogGeneration(t *testing.T) {
	e, s, v := dependencyServices(t)
	down := cycleTrigger(t, e, "downstream", "down", "batch")
	up := cycleTrigger(t, e, "upstream", "up", "batch")
	finishCycleTask(t, s, up)
	v.Catalog.Config.Units[0].Description = "new catalog generation"
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	newDown := cycleTrigger(t, e, "downstream", "new", "batch")
	if ok, err := s.Schedule(newDown.ID, nil); err != nil || ok {
		t.Fatal("catalog generations joined", ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	if ok, err := replay.Schedule(down.ID, nil); err != nil || !ok {
		t.Fatal("replay lost cycle membership", ok, err)
	}
	if ok, err := replay.Schedule(newDown.ID, nil); err != nil || ok {
		t.Fatal("replay mixed cycle generations", ok, err)
	}
}
func TestServiceDependenciesTimedAndContinuousCohort(t *testing.T) {
	e, s, v := dependencyServices(t)
	delay := 3600
	for i := range v.Catalog.Config.Units {
		u := &v.Catalog.Config.Units[i]
		if u.ID == "upstream" {
			u.Trigger = "continuous"
			u.Schedule = &service.Schedule{Type: "continuous", RestartDelay: &delay}
		} else {
			u.Trigger = "cron"
			u.Schedule = &service.Schedule{Type: "cron", Cron: "0 0 * * *", Timezone: "UTC", RunOnStartup: true}
		}
	}
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	// These reconciliations intentionally run without an active dispatcher.
	e.ctx = nil
	now := time.Now().UTC()
	if err := e.reconcileContinuousServices(now); err != nil {
		t.Fatal(err)
	}
	if err := e.scheduleServices(now, false); err != nil {
		t.Fatal(err)
	}
	var up, down Run
	for _, r := range s.List() {
		task, _ := s.Task(r.TaskID)
		if task.Source == "trigger:upstream" {
			up = r
		} else {
			down = r
		}
	}
	if up.ID == "" || down.ID == "" {
		t.Fatal("missing cohort receipts")
	}
	if ok, err := s.Schedule(down.ID, nil); err != nil || ok {
		t.Fatal("dependent ran before cohort prerequisite", err)
	}
	finishCycleTask(t, s, up)
	if ok, err := s.Schedule(down.ID, nil); err != nil || !ok {
		t.Fatal("continuous and cron did not share cycle", ok, err)
	}
}
func TestServiceDependenciesRequireManualCycle(t *testing.T) {
	e, s, _ := dependencyServices(t)
	if _, err := e.TriggerService("downstream", "one"); err == nil {
		t.Fatal("missing manual cycle accepted")
	}
	if len(s.List()) != 0 {
		t.Fatal("rejected intake wrote receipt")
	}
}

func TestServiceDependenciesEventSiblingCycle(t *testing.T) {
	apps, _ := LoadApps("")
	e, s := testEngine(t, t.TempDir(), apps)
	c := serviceCatalog(e.Root)
	c.Config.Targets = nil
	c.Config.Intent = service.Intent{Enable: []string{"source", "a-down", "z-up"}}
	c.Config.Units = []service.Unit{{ID: "source", Scope: "project", Trigger: "manual"}, {ID: "a-down", Scope: "project", Trigger: "event", DependsOn: []string{"z-up"}, Schedule: &service.Schedule{Type: "event", Trigger: service.EventPatterns{"service.succeeded:source"}}}, {ID: "z-up", Scope: "project", Trigger: "event", Schedule: &service.Schedule{Type: "event", Trigger: service.EventPatterns{"service.succeeded:source"}}}}
	c.Work = map[string]Brief{"source": testBrief(e.Root), "a-down": testBrief(e.Root), "z-up": testBrief(e.Root)}
	if _, err := e.ConfigureServices(c, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := e.TriggerService("source", "one"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var down, up Task
		for _, task := range s.Tasks() {
			if task.Source == "trigger:a-down" {
				down = task
			}
			if task.Source == "trigger:z-up" {
				up = task
			}
		}
		if down.Status == "done" {
			if up.Status != "done" || down.ServiceCycle.ID != up.ServiceCycle.ID || len(down.ServiceDependencyEvidence) != 1 || down.ServiceDependencyEvidence[0].TaskID != up.ID {
				t.Fatal("wrong event sibling prerequisite", down.ServiceDependencyEvidence)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("event dependency did not complete")
}

func TestServiceDependenciesChangedEventAllocationSuppressed(t *testing.T) {
	e, s, v := dependencyServices(t)
	v.Catalog.Config.Units = append(v.Catalog.Config.Units, service.Unit{ID: "source", Scope: "project", Trigger: "manual"})
	v.Catalog.Config.Intent.Enable = append(v.Catalog.Config.Intent.Enable, "source")
	v.Catalog.Work["source"] = testBrief(e.Root)
	for i := 0; i < 2; i++ {
		u := &v.Catalog.Config.Units[i]
		u.Trigger = "event"
		u.Schedule = &service.Schedule{Type: "event", Trigger: service.EventPatterns{"service.succeeded:source"}}
	}
	v, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	source, err := e.TriggerService("source", "one")
	if err != nil {
		t.Fatal(err)
	}
	finishCycleTask(t, s, source)
	v.Catalog.Config.Units[1].DependsOn = nil
	if _, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	e.ctx = nil
	if err = e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, delivery := range s.serviceEventDeliveries {
		if delivery.Recipient.ServiceID == "downstream" {
			found = true
			if delivery.Recipient.Status != "suppressed" {
				t.Fatal("changed dependency allocation consumed", delivery.Recipient.Status)
			}
		}
	}
	if !found {
		t.Fatal("missing pending allocation")
	}
}
