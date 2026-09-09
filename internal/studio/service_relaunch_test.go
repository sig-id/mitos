package studio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"mitos/internal/service"
)

func TestServiceRelaunchIdempotentStoppedRootAndCycle(t *testing.T) {
	e, s, v := lifecycleEngine(t)
	u := service.Unit{ID: "dependent", Scope: "project", Trigger: "manual", DependsOn: []string{"member"}, Relations: service.Relations{PartOf: []string{"group"}}}
	v.Catalog.Config.Units = append(v.Catalog.Config.Units, u)
	v.Catalog.Config.Intent.Enable = append(v.Catalog.Config.Intent.Enable, u.ID)
	v.Catalog.Work[u.ID] = testBrief(e.Root)
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	old, err := e.TriggerService("member", "old")
	if err != nil {
		t.Fatal(err)
	}
	stop, err := e.StopService("group", "stop")
	if err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	var wg sync.WaitGroup
	results := make(chan ServiceLifecycleView, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, err := e.RestartService("group", "restart"); results <- r; errs <- err }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var op ServiceLifecycleView
	for r := range results {
		if op.Seq != 0 && op.Seq != r.Seq {
			t.Fatal("duplicate restart")
		}
		op = r
	}
	if op.Action != "restart" || op.Replaces != stop.Seq || len(op.Relaunch) != 2 {
		t.Fatal("wrong restart scope", op)
	}
	previous, _ := s.ServiceLifecycle(stop.Seq)
	if previous.Status != "superseded" {
		t.Fatal("old stop not atomically replaced")
	}
	if _, err = e.TriggerService("member", "intrude"); err == nil {
		t.Fatal("restart released admission before shutdown")
	}
	reconcileLifecycle(t, e)
	op, _ = s.ServiceLifecycle(op.Seq)
	if op.Status != "restarted" || len(s.Tasks()) != 3 {
		t.Fatal("restart did not publish group", op)
	}
	var up, down Task
	for _, b := range op.Relaunch {
		run, _ := s.Get(b.RunID)
		task, _ := s.Task(run.TaskID)
		if b.ServiceID == "member" {
			up = task
		} else {
			down = task
		}
		var creation change
		if json.Unmarshal(run.Events[0].Data, &creation) != nil || creation.ServiceCall.Origin != "lifecycle" || !reflect.DeepEqual(task.ServiceLifecycle, creation.ServiceCall.Lifecycle) || task.ServiceLifecycle.Seq != op.Seq {
			t.Fatal("restart cause not atomic")
		}
	}
	if up.ID == old.TaskID || up.ServiceCycle.ID != down.ServiceCycle.ID {
		t.Fatal("restart did not create fresh same-cycle work")
	}
	if ok, err := s.AcquireClaims(down.CurrentRunID); err != nil || ok {
		t.Fatal("restart bypassed dependency", err)
	}
	finishEventRun(t, s, up.CurrentRunID)
	finishEventRun(t, s, down.CurrentRunID)
	down, _ = s.Task(down.ID)
	if len(down.ServiceDependencyEvidence) != 1 || down.ServiceDependencyEvidence[0].TaskID != up.ID {
		t.Fatal("wrong restart prerequisite")
	}
	duplicate, err := e.RestartService("group", "restart")
	if err != nil || duplicate.Seq != op.Seq || duplicate.Status != "restarted" {
		t.Fatal("duplicate relaunched again", err)
	}
	live, err := ReportAutonomy(s.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(s.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("restart metrics mismatch", err)
	}
}
func manyRelaunchEngine(t *testing.T) (*Engine, *Store, ServiceView) {
	units := []service.Unit{}
	for i := 0; i < 65; i++ {
		units = append(units, service.Unit{ID: fmt.Sprintf("member-%02d", i), Scope: "project", Trigger: "manual", Relations: service.Relations{PartOf: []string{"group"}}})
	}
	e, s, v := customEventEngine(t, units...)
	v.Catalog.Config.Targets = []service.Target{{ID: "group", Scope: "project"}}
	v, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func TestServiceRelaunchPublicationBarrierAndPartialReplay(t *testing.T) {
	e, s, _ := manyRelaunchEngine(t)
	op, err := e.RestartService("group", "one")
	if err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	before, _ := s.ServiceLifecycle(op.Seq)
	if before.Status != "starting" || len(s.Tasks()) != 64 {
		t.Fatal("missing bounded preparation barrier", before.Status, len(s.Tasks()))
	}
	for _, task := range s.Tasks() {
		if ok, err := s.AcquireClaims(task.CurrentRunID); err != nil || ok {
			t.Fatal("partially prepared group executed", err)
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
	e.Store = replay
	restored, _ := replay.ServiceLifecycle(op.Seq)
	if !reflect.DeepEqual(before, restored) {
		t.Fatal("partial receipts/bindings changed on replay")
	}
	reconcileLifecycle(t, e)
	done, _ := replay.ServiceLifecycle(op.Seq)
	if done.Status != "restarted" || len(replay.Tasks()) != 65 {
		t.Fatal("partial restart duplicated/lost receipts", done.Status, len(replay.Tasks()))
	}
	for i := 0; i < 64; i++ {
		if done.Relaunch[i].RunID != before.Relaunch[i].RunID {
			t.Fatal("replay replaced a prepared receipt")
		}
	}
	if ok, err := replay.AcquireClaims(done.Relaunch[64].RunID); err != nil || !ok {
		t.Fatal("published group still held", err)
	}
}
func TestServiceRelaunchFailureCancelsPreparedGroup(t *testing.T) {
	e, s, v := manyRelaunchEngine(t)
	op, err := e.RestartService("group", "one")
	if err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	work := v.Catalog.Work["member-64"]
	work.Title = "Changed work"
	v.Catalog.Work["member-64"] = work
	if _, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	reconcileLifecycle(t, e)
	failed, _ := s.ServiceLifecycle(op.Seq)
	if failed.Status != "restart_failed" || failed.Detail == "" {
		t.Fatal("changed plan not failed", failed)
	}
	for _, task := range s.Tasks() {
		if task.Status != "cancelled" || task.AdmittedRunID != "" {
			t.Fatal("partial group survived or executed")
		}
	}
	if _, err = e.TriggerService("member-00", "unheld"); err == nil {
		t.Fatal("failed restart lost its hold")
	}
	if _, err = e.ResumeService("group", "resume"); err != nil {
		t.Fatal(err)
	}
	if _, err = e.TriggerService("member-00", "fresh"); err != nil {
		t.Fatal(err)
	}
}
func TestServiceRelaunchOverlappingHoldAndOperatorCancellation(t *testing.T) {
	e, s, _ := lifecycleEngine(t)
	if _, err := e.StopService("other-group", "hold"); err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	op, err := e.RestartService("group", "restart")
	if err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	state, _ := s.ServiceLifecycle(op.Seq)
	if state.Status != "starting" || len(s.Tasks()) != 0 {
		t.Fatal("independent hold ignored")
	}
	if _, err = e.RestartService("other-group", "overlap"); !errors.Is(err, ErrConflict) {
		t.Fatal("overlapping restarts could deadlock", err)
	}
	cancelled, err := e.StopService("group", "cancel-restart")
	if err != nil || cancelled.Status != "restart_cancelling" {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	state, _ = s.ServiceLifecycle(op.Seq)
	if state.Status != "restart_failed" || len(s.Tasks()) != 0 {
		t.Fatal("cancelled restart launched")
	}
	if same, err := e.StopService("group", "cancel-restart"); err != nil || same.Seq != op.Seq {
		t.Fatal("restart cancel duplicated")
	}
	if _, err = e.ResumeService("other-group", "release"); err != nil {
		t.Fatal(err)
	}
	next, err := e.RestartService("group", "new")
	if err != nil || next.Replaces != op.Seq {
		t.Fatal("failed restart not replaceable", err)
	}
	reconcileLifecycle(t, e)
	state, _ = s.ServiceLifecycle(next.Seq)
	if state.Status != "restarted" || len(s.Tasks()) != 1 {
		t.Fatal("fresh restart did not publish")
	}
}
func TestServiceRelaunchWaitsForOldWorkerAndContinuousOwnership(t *testing.T) {
	e, s, _ := continuousEngine(t)
	reconcileContinuous(t, e, *continuousState(t, s).NextAt)
	old := s.Tasks()[0]
	if ok, err := s.Schedule(old.CurrentRunID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	e.cancel = map[string]context.CancelFunc{old.CurrentRunID: func() {}}
	op, err := e.RestartService("inventory", "restart")
	if err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	if len(s.Tasks()) != 1 {
		t.Fatal("replacement prepared before old worker stopped")
	}
	if err = s.Transition(old.CurrentRunID, "run_cancelled", "cancelled", "Stopped", "Fixture"); err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	state, _ := s.ServiceLifecycle(op.Seq)
	if state.Status != "stopping" {
		t.Fatal("old worker bookkeeping ignored")
	}
	delete(e.cancel, old.CurrentRunID)
	reconcileLifecycle(t, e)
	state, _ = s.ServiceLifecycle(op.Seq)
	controller := continuousState(t, s)
	if state.Status != "restarted" || controller.Generation != 2 || controller.Status != "running" || len(s.Tasks()) != 2 {
		t.Fatal("restart did not retain continuous ownership", state, controller)
	}
	reconcileContinuous(t, e, time.Now().Add(time.Hour))
	if len(s.Tasks()) != 2 {
		t.Fatal("continuous restart created an extra generation")
	}
}
func TestServiceRelaunchRejectsOutsidePrerequisiteBeforeStopping(t *testing.T) {
	e, s, v := lifecycleEngine(t)
	v.Catalog.Config.Units[0].DependsOn = []string{"unrelated"}
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := e.RestartService("group", "invalid"); err == nil {
		t.Fatal("unsatisfiable restart cycle accepted")
	}
	if len(s.ServiceLifecycles()) != 0 || s.serviceLifecycleHeld("member") {
		t.Fatal("invalid restart mutated lifecycle")
	}
}
