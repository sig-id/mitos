package studio

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"mitos/internal/service"
)

func intervalEngine(t *testing.T, policy string) (*Engine, *Store, ServiceView) {
	t.Helper()
	s := testStore(t)
	apps, _ := LoadApps("")
	e := &Engine{Store: s, Apps: apps, Root: t.TempDir()}
	c := serviceCatalog(e.Root)
	c.Config.Units[0].Trigger = "interval"
	c.Config.Units[0].Schedule = &service.Schedule{Type: "interval", Interval: 60}
	c.Config.Units[0].Concurrency = service.Concurrency{MaxInstances: 1, IfRunning: policy, QueueMax: 1}
	v, err := e.ConfigureServices(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func TestIntervalOccurrenceAtomicReplayAndDowntimeCoalescing(t *testing.T) {
	e, s, v := intervalEngine(t, "queue")
	due := *v.States[0].NextAt
	if err := e.scheduleServices(due.Add(-time.Nanosecond), false); err != nil {
		t.Fatal(err)
	}
	if len(s.Tasks()) != 0 {
		t.Fatal("interval fired early")
	}
	if err := e.scheduleServices(due, false); err != nil {
		t.Fatal(err)
	}
	if len(s.Tasks()) != 1 {
		t.Fatal("due interval did not create a task")
	}
	created := s.List()[0]
	var c change
	_ = json.Unmarshal(created.Events[0].Data, &c)
	if c.ServiceCall == nil || c.ServiceCall.Origin != "interval" || c.ServiceCall.Tick == nil || c.Task.Source != "trigger:inventory" || !c.ServiceCall.Tick.DueAt.Equal(due) {
		t.Fatal("occurrence/cursor not atomic with Task")
	}
	before, _ := s.Services()
	if err := e.scheduleServices(due, false); err != nil {
		t.Fatal(err)
	}
	if len(s.Tasks()) != 1 {
		t.Fatal("duplicate tick created work")
	}
	if _, err := ReportAutonomy(s.List()); err != nil {
		t.Fatal(err)
	}
	if _, err := AuditAutonomyLogs(s.Dir()); err != nil {
		t.Fatal(err)
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
	replay, _ := reopened.Services()
	if !reflect.DeepEqual(before, replay) {
		t.Fatal("runtime cursor changed on replay")
	}
	if err = e.scheduleServices(due, false); err != nil {
		t.Fatal(err)
	}
	if len(reopened.Tasks()) != 1 {
		t.Fatal("restart duplicated committed occurrence")
	}
	late := due.Add(24 * time.Hour)
	if err = e.scheduleServices(late, false); err != nil {
		t.Fatal(err)
	}
	after, _ := reopened.Services()
	if len(reopened.Tasks()) != 2 || !after.States[0].NextAt.Equal(late.Add(time.Minute)) {
		t.Fatal("downtime caused backlog or clock drift")
	}
	if err = e.scheduleServices(late.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	after, _ = reopened.Services()
	if len(reopened.Tasks()) != 2 || after.States[0].Skipped != 1 || after.States[0].LastTick.Outcome != "skipped" {
		t.Fatal("full queue did not record durable skip")
	}
	if _, err = AuditAutonomyLogs(reopened.Dir()); err != nil {
		t.Fatal("skip broke offline audit", err)
	}
}
func TestServiceConcurrencyGatesActualAdmissionAndRetainsParkedSlot(t *testing.T) {
	e, s, v := intervalEngine(t, "queue")
	due := *v.States[0].NextAt
	for i := 0; i < 2; i++ {
		if err := e.scheduleServices(due.Add(time.Duration(i)*time.Minute), false); err != nil {
			t.Fatal(err)
		}
	}
	tasks := s.Tasks()
	a, b := tasks[0], tasks[1]
	ok, err := s.Schedule(a.CurrentRunID, nil)
	if err != nil || !ok {
		t.Fatal("first service task failed admission", err)
	}
	ok, err = s.Schedule(b.CurrentRunID, nil)
	if err != nil || ok {
		t.Fatal("second service task bypassed max_instances", err)
	}
	r, _ := s.Get(b.CurrentRunID)
	if r.Claim != nil || len(r.Segments) != 0 {
		t.Fatal("service backpressure applied after claims")
	}
	ready, err := s.ReadyRuns(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range ready {
		if candidate.Run.ID == b.CurrentRunID {
			t.Fatal("saturated service appeared ready")
		}
	}
	blocked, _ := s.Task(b.ID)
	if blocked.Status != "blocked" || !reflect.DeepEqual(blocked.WaitingFor, []string{"service:inventory"}) {
		t.Fatal("service wait reason is not visible on the Task")
	}
	if err = s.Park(a.CurrentRunID, recoveryDecision()); err != nil {
		t.Fatal(err)
	}
	ok, err = s.Schedule(b.CurrentRunID, nil)
	if err != nil || ok {
		t.Fatal("parked work lost its service slot")
	}
	if err = s.Complete(a.CurrentRunID, "Complete", true); err != nil {
		t.Fatal(err)
	}
	ok, err = s.Schedule(b.CurrentRunID, nil)
	if err != nil || !ok {
		t.Fatal("completion failed to release service capacity", err)
	}
}
func TestIntervalSkipRejectionActivationAndClockPreservation(t *testing.T) {
	e, s, v := intervalEngine(t, "skip")
	due := *v.States[0].NextAt
	if err := e.scheduleServices(due, false); err != nil {
		t.Fatal(err)
	}
	if err := e.scheduleServices(due.Add(time.Minute), false); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Services()
	if v.States[0].Skipped != 1 || len(s.Tasks()) != 1 {
		t.Fatal("skip policy queued overlapping work")
	}
	v.Catalog.Config.Units[0].Description = "Changed description"
	oldNext := *v.States[0].NextAt
	oldEpoch := v.States[0].Epoch
	updated, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.States[0].NextAt.Equal(oldNext) || updated.States[0].Epoch != oldEpoch {
		t.Fatal("unrelated configuration restarted the clock")
	}
	if err = s.Complete(s.Tasks()[0].CurrentRunID, "Done", true); err != nil {
		t.Fatal(err)
	}
	e.Apps = nil
	if err = e.scheduleServices(oldNext, false); err != nil {
		t.Fatal(err)
	}
	v, _ = s.Services()
	if v.States[0].Rejected != 1 || v.States[0].LastTick.Detail == "" || len(s.Tasks()) != 1 {
		t.Fatal("failed admission was not recorded")
	}
	v.Catalog.Config.Intent.Disable = []string{"inventory"}
	v, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if v.States[0].NextAt != nil {
		t.Fatal("disabled service retained active clock")
	}
	if err = e.scheduleServices(oldNext.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if len(s.Tasks()) != 1 {
		t.Fatal("disabled service fired")
	}
	e.Apps, _ = LoadApps("")
	v.Catalog.Config.Intent.Disable = nil
	v, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if v.States[0].Epoch == oldEpoch || v.States[0].NextAt == nil {
		t.Fatal("reactivation did not create fresh schedule epoch")
	}
}
func TestIntervalStartupAndManualKeyNamespace(t *testing.T) {
	e, s, v := intervalEngine(t, "queue")
	v.Catalog.Config.Units[0].Schedule.RunOnStartup = true
	v, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	now := *v.States[0].NextAt
	if err = e.scheduleServices(now, true); err != nil {
		t.Fatal(err)
	}
	run := s.List()[0]
	var c change
	_ = json.Unmarshal(run.Events[0].Data, &c)
	if !c.ServiceCall.Tick.Startup {
		t.Fatal("startup trigger missing")
	}
	// A client key equal to a scheduled key must still be an independent receipt.
	manual, err := e.TriggerService("inventory", c.ServiceCall.Key)
	if err != nil || manual.ID == run.ID {
		t.Fatal("scheduled key collided with manual namespace", err)
	}
	if err = e.scheduleServices(now.Add(time.Second), true); err != nil {
		t.Fatal(err)
	}
	state, _ := s.Services()
	if len(s.Tasks()) != 2 || state.States[0].Skipped != 1 {
		t.Fatal("startup bypassed bounded queue")
	}
}

func TestConcurrentIntervalTicksCreateOneOccurrence(t *testing.T) {
	e, s, v := intervalEngine(t, "queue")
	due := *v.States[0].NextAt
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- e.scheduleServices(due, false) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(s.Tasks()) != 1 {
		t.Fatal("concurrent ticks duplicated occurrence")
	}
	state, _ := s.Services()
	if state.States[0].Enqueued != 1 || state.States[0].Skipped != 0 {
		t.Fatal("concurrent ticks changed cursor twice")
	}
}
