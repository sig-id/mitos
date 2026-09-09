package studio

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"mitos/internal/service"
)

func breakerEngine(t *testing.T) (*Engine, *Store) {
	t.Helper()
	s := testStore(t)
	apps, _ := LoadApps("")
	e := &Engine{Store: s, Apps: apps, Root: t.TempDir()}
	c := serviceCatalog(e.Root)
	c.Config.Units[0].CircuitBreaker = service.BreakerConfig{Enabled: true, Threshold: 1, Cooldown: 1, HalfOpenRequests: 2}
	c.Config.Units[0].Concurrency = service.Concurrency{MaxInstances: 2, IfRunning: "queue", QueueMax: 5}
	if _, err := e.ConfigureServices(c, 0); err != nil {
		t.Fatal(err)
	}
	return e, s
}
func breakerRun(t *testing.T, e *Engine, key string, admit bool) Run {
	t.Helper()
	r, err := e.TriggerService("inventory", key)
	if err != nil {
		t.Fatal(err)
	}
	if admit {
		if ok, err := e.Store.Schedule(r.ID, nil); err != nil || !ok {
			t.Fatal("service admission", err)
		}
	}
	return r
}
func circuitView(t *testing.T, s *Store) service.Circuit {
	t.Helper()
	v, err := s.Services()
	if err != nil || v.States[0].Breaker == nil {
		t.Fatal("missing breaker", err)
	}
	return *v.States[0].Breaker
}
func TestServiceBreakerAtomicOutcomesQueueGatingAndReplay(t *testing.T) {
	e, s := breakerEngine(t)
	failed := breakerRun(t, e, "failed", true)
	late := breakerRun(t, e, "late", true)
	queued := breakerRun(t, e, "queued", false)
	if err := s.Transition(failed.ID, "run_failed", "failed", "Failed", "Test failure"); err != nil {
		t.Fatal(err)
	}
	opened := circuitView(t, s)
	if opened.State != "open" || opened.Failures != 1 {
		t.Fatal("failure did not open circuit")
	}
	terminal, _ := s.Get(failed.ID)
	var effect change
	_ = json.Unmarshal(terminal.Events[len(terminal.Events)-1].Data, &effect)
	if effect.ServiceBreaker == nil || effect.ServiceBreaker.State.State != "open" || effect.Task.Status != "failed" {
		t.Fatal("terminal result and breaker transition were not atomic")
	}
	if _, err := e.TriggerService("inventory", "blocked-new"); err == nil {
		t.Fatal("open circuit admitted manual request")
	}
	duplicate, err := e.TriggerService("inventory", "failed")
	if err != nil || duplicate.ID != failed.ID {
		t.Fatal("open circuit lost prior receipt")
	}
	if ok, err := s.Schedule(queued.ID, nil); err != nil || ok {
		t.Fatal("queued work bypassed open circuit", err)
	}
	if _, err := s.ReadyRuns(nil); err != nil {
		t.Fatal(err)
	}
	task, _ := s.Task(queued.TaskID)
	if !reflect.DeepEqual(task.WaitingFor, []string{"service-breaker:inventory"}) {
		t.Fatal("breaker wait not visible")
	}
	if err = s.Complete(late.ID, "Late success", true); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opened, circuitView(t, s)) {
		t.Fatal("late success closed open circuit")
	}
	live, _ := s.Services()
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	e.Store = replay
	restored, _ := replay.Services()
	if !reflect.DeepEqual(live, restored) {
		t.Fatal("open state/deadline changed on replay")
	}
	time.Sleep(max(time.Until(*opened.OpenUntil), 0) + 5*time.Millisecond)
	if ok, err := replay.Schedule(queued.ID, nil); err != nil || !ok {
		t.Fatal("recovery probe did not admit", err)
	}
	half := circuitView(t, replay)
	if half.State != "half-open" || half.Issued != 1 {
		t.Fatal("probe was not reserved")
	}
	second := breakerRun(t, e, "second-probe", true)
	if err = replay.Complete(queued.ID, "Probe one", true); err != nil {
		t.Fatal(err)
	}
	if circuitView(t, replay).State != "half-open" {
		t.Fatal("partial probe closed circuit")
	}
	if _, err = e.TriggerService("inventory", "excess-probe"); err == nil {
		t.Fatal("probe quota bypassed")
	}
	if err = replay.Complete(second.ID, "Probe two", true); err != nil {
		t.Fatal(err)
	}
	if circuitView(t, replay).State != "closed" {
		t.Fatal("probe cohort failed to close circuit")
	}
	liveMetrics, err := ReportAutonomy(replay.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(replay.Dir())
	if err != nil || !reflect.DeepEqual(liveMetrics, offline) {
		t.Fatal("breaker effects broke metric equivalence", err)
	}
}
func TestServiceBreakerProbeReplayCancellationAndConfigurationEpoch(t *testing.T) {
	e, s := breakerEngine(t)
	run := breakerRun(t, e, "first", true)
	if err := s.Transition(run.ID, "run_failed", "failed", "Failed", "Test failure"); err != nil {
		t.Fatal(err)
	}
	opened := circuitView(t, s)
	time.Sleep(max(time.Until(*opened.OpenUntil), 0) + 5*time.Millisecond)
	probe := breakerRun(t, e, "probe", true)
	if err := s.Park(probe.ID, recoveryDecision()); err != nil {
		t.Fatal(err)
	}
	before := circuitView(t, s)
	if before.Issued != 1 {
		t.Fatal("parked probe lost reservation")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	e.Store = reopened
	if !reflect.DeepEqual(before, circuitView(t, reopened)) {
		t.Fatal("probe reservation lost on replay")
	}
	if err = reopened.Transition(probe.ID, "run_cancelled", "cancelled", "Cancelled", "Operator cancelled probe"); err != nil {
		t.Fatal(err)
	}
	if circuitView(t, reopened).Issued != 0 {
		t.Fatal("cancelled probe consumed quota")
	}
	next := breakerRun(t, e, "replacement", true)
	v, _ := reopened.Services()
	v.Catalog.Config.Units[0].Description = "Unrelated edit"
	v, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if v.States[0].Breaker.Epoch != before.Epoch || v.States[0].Breaker.State != "half-open" {
		t.Fatal("description edit reset protection")
	}
	v.Catalog.Config.Units[0].CircuitBreaker.Threshold = 3
	v, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	newEpoch := v.States[0].Breaker.Epoch
	if newEpoch == before.Epoch {
		t.Fatal("policy edit did not create new epoch")
	}
	if err = reopened.Transition(next.ID, "run_failed", "failed", "Failed", "Old policy result"); err != nil {
		t.Fatal(err)
	}
	current := circuitView(t, reopened)
	if current.Epoch != newEpoch || current.Failures != 0 {
		t.Fatal("old policy result altered new policy")
	}
}
func TestServiceBreakerScheduledOccurrencesSkipWhileOpen(t *testing.T) {
	e, s, v := intervalEngine(t, "queue")
	v.Catalog.Config.Units[0].CircuitBreaker = service.BreakerConfig{Enabled: true, Threshold: 1, Cooldown: 300}
	v, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	due := *v.States[0].NextAt
	if err = e.scheduleServices(due, false); err != nil {
		t.Fatal(err)
	}
	run := s.List()[0]
	if ok, err := s.Schedule(run.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	if err = s.Transition(run.ID, "run_failed", "failed", "Failed", "Test failure"); err != nil {
		t.Fatal(err)
	}
	if err = e.scheduleServices(due.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	view, _ := s.Services()
	if len(s.Tasks()) != 1 || view.States[0].Skipped != 1 || view.States[0].LastTick.Detail != "Service circuit breaker is withholding new execution." {
		t.Fatal("open circuit did not skip automatic occurrence")
	}
}

func TestServiceBreakerParkedRetryReleasesOldProbeAtomically(t *testing.T) {
	e, s := breakerEngine(t)
	first := breakerRun(t, e, "first", true)
	if err := s.Transition(first.ID, "run_failed", "failed", "Failed", "Test failure"); err != nil {
		t.Fatal(err)
	}
	opened := circuitView(t, s)
	time.Sleep(max(time.Until(*opened.OpenUntil), 0) + 5*time.Millisecond)
	probe := breakerRun(t, e, "probe", true)
	if err := s.Park(probe.ID, recoveryDecision()); err != nil {
		t.Fatal(err)
	}
	parked, _ := s.Get(probe.ID)
	child, err := e.Resolve(probe.ID, parked.Decision.ID, "retry")
	if err != nil {
		t.Fatal(err)
	}
	closed, _ := s.Get(probe.ID)
	if closed.Status != "cancelled" || circuitView(t, s).Issued != 0 {
		t.Fatal("old probe reservation leaked after retry")
	}
	last := closed.Events[len(closed.Events)-1]
	var c change
	_ = json.Unmarshal(last.Data, &c)
	if c.ServiceBreaker == nil || c.ServiceBreaker.Kind != "cancelled" || c.Task != nil {
		t.Fatal("superseded attempt release was not atomic without overwriting latest Task")
	}
	if ok, err := s.Schedule(child, nil); err != nil || !ok {
		t.Fatal("retry could not reserve replacement probe", err)
	}
	if _, err := ReportAutonomy(s.List()); err != nil {
		t.Fatal("per-run fold failed on superseded probe", err)
	}
	if _, err := AuditAutonomyLogs(s.Dir()); err != nil {
		t.Fatal(err)
	}
}

func TestServiceBreakerConcurrentProbeAdmissionDoesNotUsePoolLimit(t *testing.T) {
	e, s := breakerEngine(t)
	view, _ := s.Services()
	view.Catalog.Config.Units[0].CircuitBreaker.HalfOpenRequests = 1
	if _, err := e.ConfigureServices(view.Catalog, view.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	first := breakerRun(t, e, "first", true)
	queued := []Run{}
	for _, key := range []string{"a", "b", "c", "d"} {
		queued = append(queued, breakerRun(t, e, key, false))
	}
	if err := s.Transition(first.ID, "run_failed", "failed", "Failed", "Test failure"); err != nil {
		t.Fatal(err)
	}
	opened := circuitView(t, s)
	time.Sleep(max(time.Until(*opened.OpenUntil), 0) + 5*time.Millisecond)
	var wg sync.WaitGroup
	admitted := make(chan bool, len(queued))
	errs := make(chan error, len(queued))
	for _, run := range queued {
		wg.Add(1)
		go func() { defer wg.Done(); ok, err := s.Schedule(run.ID, nil); admitted <- ok; errs <- err }()
	}
	wg.Wait()
	close(admitted)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	count := 0
	for ok := range admitted {
		if ok {
			count++
		}
	}
	if count != 1 || circuitView(t, s).Issued != 1 {
		t.Fatal("concurrent admission exceeded single probe quota")
	}
}
