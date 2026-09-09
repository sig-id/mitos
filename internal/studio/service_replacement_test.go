package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"mitos/internal/service"
)

func replacementEngine(t *testing.T) (*Engine, *Store, ServiceView) {
	t.Helper()
	e, s, v := intervalEngine(t, "skip")
	c := v.Catalog
	c.Config.Units[0].Trigger = "manual"
	c.Config.Units[0].Schedule = nil
	c.Config.Units[0].Concurrency.IfRunning = "kill"
	v, err := e.ConfigureServices(c, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func replacementRun(t *testing.T, e *Engine, key string) Run {
	t.Helper()
	r, err := e.TriggerService("inventory", key)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func reconcileReplacement(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.reconcileServiceReplacements(); err != nil {
		t.Fatal(err)
	}
}
func TestServiceReplacementAtomicWitnessAndShutdownGate(t *testing.T) {
	e, s, _ := replacementEngine(t)
	first := replacementRun(t, e, "first")
	if ok, err := s.Schedule(first.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	prior, _ := s.Task(first.TaskID)
	stops := 0
	e.cancel = map[string]context.CancelFunc{first.ID: func() { stops++ }}
	next := replacementRun(t, e, "next")
	task, _ := s.Task(next.TaskID)
	if task.ServiceReplacement == nil || task.ServiceReplacement.TaskID != first.TaskID || task.ServiceReplacement.RunID != first.ID || task.ServiceReplacement.Head != prior.Head {
		t.Fatal("replacement missing exact victim witness")
	}
	var created change
	_ = json.Unmarshal(next.Events[0].Data, &created)
	if !reflect.DeepEqual(created.ServiceCall.Replacement, created.Task.ServiceReplacement) {
		t.Fatal("replacement not atomic with receipt")
	}
	if _, err := e.TriggerService("inventory", "overflow"); err == nil || len(s.Tasks()) != 2 {
		t.Fatal("unbounded replacement backlog")
	}
	duplicate, err := e.TriggerService("inventory", "next")
	if err != nil || duplicate.ID != next.ID {
		t.Fatal("duplicate stop intent")
	}
	if ok, err := s.Schedule(next.ID, nil); err != nil || ok {
		t.Fatal("replacement started before stop")
	}
	reconcileReplacement(t, e)
	reconcileReplacement(t, e)
	old, _ := s.Get(first.ID)
	if old.Status != "stopping" || stops != 1 {
		t.Fatal("stop request duplicated", old.Status, stops)
	}
	if ok, err := s.Schedule(next.ID, nil); err != nil || ok {
		t.Fatal("replacement overlapped shutdown")
	}
	if err := s.Transition(first.ID, "run_cancelled", "cancelled", "Stopped", "Fixture stop complete"); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(next.ID, []string{first.ID}); err != nil || ok {
		t.Fatal("replacement raced worker tail")
	}
	delete(e.cancel, first.ID)
	if ok, err := s.Schedule(next.ID, nil); err != nil || !ok {
		t.Fatal("settled replacement did not admit", err)
	}
	if _, err := e.Retry(first.ID); err == nil {
		t.Fatal("superseded Task resurrected while replacement lives")
	}
	if _, err := e.TriggerService("inventory", "overflow"); err != nil {
		t.Fatal("refused key was incorrectly reserved", err)
	}
}
func TestServiceReplacementPendingReplayFencingAndConcurrentStop(t *testing.T) {
	e, s, _ := replacementEngine(t)
	first := replacementRun(t, e, "first")
	next := replacementRun(t, e, "next")
	if ok, err := s.Schedule(first.ID, nil); err != nil || ok {
		t.Fatal("queued victim acquired claims after replacement")
	}
	ready, err := s.ReadyRuns(nil)
	if err != nil || len(ready) != 0 {
		t.Fatal("pending replacement readiness", err)
	}
	victim, _ := s.Task(first.TaskID)
	successor, _ := s.Task(next.TaskID)
	if !reflect.DeepEqual(victim.WaitingFor, []string{"service-superseded:inventory"}) || !reflect.DeepEqual(successor.WaitingFor, []string{"service-replacement:inventory"}) {
		t.Fatal("missing replacement wait evidence")
	}
	before, _ := s.Services()
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
	if !reflect.DeepEqual(before, restored) {
		t.Fatal("replacement did not replay")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- e.reconcileServiceReplacements() }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	old, _ := replay.Get(first.ID)
	aborts := 0
	for _, ev := range old.Events {
		if ev.Type == "decision" && ev.Kind == "abort" {
			aborts++
		}
	}
	if old.Status != "cancelled" || aborts != 1 {
		t.Fatal("replay/concurrency duplicated stop", old.Status, aborts)
	}
	if ok, err := replay.Schedule(next.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	live, err := ReportAutonomy(replay.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(replay.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("replacement metrics", err)
	}
}
func TestServiceReplacementCancelledReceiptWithdrawsPendingStop(t *testing.T) {
	e, s, _ := replacementEngine(t)
	first := replacementRun(t, e, "first")
	next := replacementRun(t, e, "next")
	if err := e.Cancel(next.ID); err != nil {
		t.Fatal(err)
	}
	reconcileReplacement(t, e)
	old, _ := s.Get(first.ID)
	if old.Status != "queued" {
		t.Fatal("cancelled receipt still stopped victim")
	}
	if ok, err := s.Schedule(first.ID, nil); err != nil || !ok {
		t.Fatal("withdrawn replacement retained fence", err)
	}
}
func TestServiceReplacementPreservesUninspectedClaims(t *testing.T) {
	e, s, v := replacementEngine(t)
	c := v.Catalog
	b := c.Work["inventory"]
	b.Ownership.Paths = []string{"."}
	c.Work["inventory"] = b
	if _, err := e.ConfigureServices(c, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	first := replacementRun(t, e, "first")
	if ok, err := s.Schedule(first.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	if err := s.Park(first.ID, recoveryDecision()); err != nil {
		t.Fatal(err)
	}
	next := replacementRun(t, e, "next")
	reconcileReplacement(t, e)
	old, _ := s.Get(first.ID)
	if old.Status != "cancelled" || old.Claim == nil || old.Claim.ReleasedAt != nil {
		t.Fatal("replacement released unknown process claims")
	}
	if ok, err := s.Schedule(next.ID, nil); err != nil || ok {
		t.Fatal("replacement bypassed inspection")
	}
	if err := e.ReleaseInspectedClaims(first.ID); err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(next.ID, nil); err != nil || !ok {
		t.Fatal("inspected replacement did not admit", err)
	}
}
func TestServiceReplacementCancelsSavedRetry(t *testing.T) {
	e, s, v := replacementEngine(t)
	c := v.Catalog
	max := 1
	c.Config.Units[0].RestartPolicy = service.RestartPolicy{OnFailure: "retry", MaxRetries: &max}
	if _, err := e.ConfigureServices(c, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	first := replacementRun(t, e, "first")
	plan := failServiceAttempt(t, s, first.ID)
	next := replacementRun(t, e, "next")
	reconcileReplacement(t, e)
	if err := e.reconcileServiceRetries(plan.DueAt); err != nil {
		t.Fatal(err)
	}
	victim, _ := s.Task(first.TaskID)
	if victim.Status != "cancelled" || len(s.List()) != 2 {
		t.Fatal("victim retry survived replacement")
	}
	if ok, err := s.Schedule(next.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
}
func TestServiceReplacementEventAllocationAndSuppression(t *testing.T) {
	for _, suppress := range []bool{false, true} {
		t.Run(map[bool]string{false: "consume", true: "suppress"}[suppress], func(t *testing.T) {
			e, s, v := eventEngine(t)
			c := v.Catalog
			c.Config.Units[1].Concurrency.IfRunning = "kill"
			v, err := e.ConfigureServices(c, v.Catalog.Revision)
			if err != nil {
				t.Fatal(err)
			}
			victim, err := e.TriggerService("consumer", "occupy")
			if err != nil {
				t.Fatal(err)
			}
			source := finishEventService(t, e, "producer", "one")
			terminal := source.Events[len(source.Events)-1]
			var emitted change
			_ = json.Unmarshal(terminal.Data, &emitted)
			if emitted.ServiceEmission.Recipients[0].Replacement == nil {
				t.Fatal("full event service did not reserve replacement")
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
			s = replay

			if ok, err := s.Schedule(victim.ID, nil); err != nil || ok {
				t.Fatal("pending event did not fence victim")
			}
			reconcileReplacement(t, e)
			old, _ := s.Get(victim.ID)
			if old.Status != "queued" {
				t.Fatal("unvalidated outbox stopped victim")
			}
			if _, err = e.TriggerService("consumer", "overflow"); err == nil {
				t.Fatal("pending event lost extra-slot reservation")
			}
			if suppress {
				c = v.Catalog
				c.Config.Units[1].Concurrency.IfRunning = "queue"
				if _, err = e.ConfigureServices(c, v.Catalog.Revision); err != nil {
					t.Fatal(err)
				}
			}
			if err = e.reconcileServiceEvents(); err != nil {
				t.Fatal(err)
			}
			reconcileReplacement(t, e)
			old, _ = s.Get(victim.ID)
			if suppress {
				if old.Status != "queued" || len(s.Tasks()) != 2 {
					t.Fatal("suppressed event stopped work")
				}
				if ok, err := s.Schedule(victim.ID, nil); err != nil || !ok {
					t.Fatal("suppression kept victim fence")
				}
			} else if old.Status != "cancelled" || len(s.Tasks()) != 3 {
				t.Fatal("event replacement not consumed", old.Status)
			}
		})
	}
}
func TestServiceReplacementTimedAndContinuousTriggers(t *testing.T) {
	for _, origin := range []string{"interval", "cron", "continuous"} {
		t.Run(origin, func(t *testing.T) {
			e, s, v := replacementEngine(t)
			c := v.Catalog
			c.Config.Units[0].Trigger = origin
			switch origin {
			case "interval":
				c.Config.Units[0].Schedule = &service.Schedule{Type: origin, Interval: 60}
			case "cron":
				c.Config.Units[0].Schedule = &service.Schedule{Type: origin, Cron: "* * * * *"}
			case "continuous":
				c.Config.Units[0].Schedule = &service.Schedule{Type: origin}
			}
			v, err := e.ConfigureServices(c, v.Catalog.Revision)
			if err != nil {
				t.Fatal(err)
			}
			victim := replacementRun(t, e, "occupy")
			if origin == "continuous" {
				err = e.reconcileContinuousServices(time.Now())
			} else {
				err = e.scheduleServices(*v.States[0].NextAt, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Tasks()) != 2 {
				t.Fatal("automatic trigger did not replace")
			}
			reconcileReplacement(t, e)
			old, _ := s.Get(victim.ID)
			if old.Status != "cancelled" {
				t.Fatal("automatic stop missing")
			}
		})
	}
}

func TestServiceReplacementSelectsAdmittedWorkAndRejectsInvalidIntake(t *testing.T) {
	e, s, v := replacementEngine(t)
	c := v.Catalog
	c.Config.Units[0].Concurrency.MaxInstances = 2
	if _, err := e.ConfigureServices(c, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	queued := replacementRun(t, e, "older-queued")
	running := replacementRun(t, e, "admitted")
	if ok, err := s.Schedule(running.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	apps := e.Apps
	e.Apps = nil
	if _, err := e.TriggerService("inventory", "invalid"); err == nil {
		t.Fatal("invalid app accepted")
	}
	if len(s.Tasks()) != 2 {
		t.Fatal("invalid intake reserved stop")
	}
	e.Apps = apps
	next := replacementRun(t, e, "next")
	task, _ := s.Task(next.TaskID)
	if task.ServiceReplacement.TaskID != running.TaskID {
		t.Fatal("replacement chose queued work before running instance")
	}
	reconcileReplacement(t, e)
	old, _ := s.Get(queued.ID)
	if old.Status != "queued" {
		t.Fatal("replacement stopped extra tasks")
	}
}

func TestServiceReplacementRealWorkerStopsHarvestsThenStarts(t *testing.T) {
	root := t.TempDir()
	control := t.TempDir()
	marker := filepath.Join(control, "pid")
	release := filepath.Join(control, "release")
	app := executionApp(t, fmt.Sprintf("printf result > result.txt; printf %%s $$ > '%s'; while test ! -f '%s'; do sleep .05; done", marker, release))
	e, s := testEngine(t, root, []Application{app})
	c := serviceCatalog(root)
	c.Config.Units[0].Concurrency.IfRunning = "kill"
	b := c.Work["inventory"]
	b.AppID = app.ID
	c.Work["inventory"] = b
	if _, err := e.ConfigureServices(c, 0); err != nil {
		t.Fatal(err)
	}
	first := replacementRun(t, e, "first")
	waitPID := func(previous string) string {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(marker); err == nil && len(data) > 0 && string(data) != previous {
				return string(data)
			}
			time.Sleep(15 * time.Millisecond)
		}
		t.Fatal("replacement worker did not reach command")
		return ""
	}
	oldPID := waitPID("")
	next := replacementRun(t, e, "next")
	waitPID(oldPID)
	old := waitRun(t, s, first.ID, func(r Run) bool { return r.Terminal() })
	if old.Status != "cancelled" || old.Claim == nil || old.Claim.ReleasedAt == nil {
		t.Fatal("old worker not stopped with claims released", old.Status)
	}
	changes := readChanges(t, s, old)
	if len(changes.Changes) != 1 || changes.Changes[0].Path != "result.txt" {
		t.Fatal("replacement lost old harvest")
	}
	replacement, _ := s.Get(next.ID)
	var stopped, admitted int64
	for _, ev := range old.Events {
		if ev.Kind == "run_cancelled" {
			stopped = ev.Seq
		}
	}
	for _, ev := range replacement.Events {
		if ev.Kind == "claims_acquired" {
			admitted = ev.Seq
		}
	}
	if stopped == 0 || admitted <= stopped {
		t.Fatal("replacement admitted before shutdown Effect")
	}
	if err := os.WriteFile(release, []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	replacement = waitRun(t, s, next.ID, func(r Run) bool { return r.Terminal() })
	if replacement.Status != "done" || !replacement.Verified {
		t.Fatal("replacement did not complete", replacement.Status)
	}
	if _, err := os.Stat(filepath.Join(root, "result.txt")); !os.IsNotExist(err) {
		t.Fatal("source workspace changed")
	}
}

func TestServiceReplacementRetiredContinuousOwnerWithdrawsStop(t *testing.T) {
	e, s, v := replacementEngine(t)
	c := v.Catalog
	c.Config.Units[0].Trigger = "continuous"
	c.Config.Units[0].Schedule = &service.Schedule{Type: "continuous"}
	v, err := e.ConfigureServices(c, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	victim := replacementRun(t, e, "occupy")
	reconcileContinuous(t, e, time.Now())
	c = v.Catalog
	c.Config.Intent.Targets = nil
	c.Config.Intent.Disable = []string{"inventory"}
	if _, err = e.ConfigureServices(c, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	reconcileReplacement(t, e)
	reconcileContinuous(t, e, time.Now())
	old, _ := s.Get(victim.ID)
	if old.Status != "queued" {
		t.Fatal("retiring continuous replacement stopped independent manual work")
	}
	if ok, err := s.Schedule(victim.ID, nil); err != nil || !ok {
		t.Fatal("retirement retained victim fence", err)
	}
}
