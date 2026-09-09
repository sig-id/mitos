package studio

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"mitos/internal/service"
)

func lifecycleEngine(t *testing.T) (*Engine, *Store, ServiceView) {
	e, s, v := customEventEngine(t, eventUnit("member", "seed"), service.Unit{ID: "unrelated", Scope: "project", Trigger: "manual"})
	v.Catalog.Config.Units[0].PartOf = []string{"group", "other-group"}
	v.Catalog.Config.Targets = []service.Target{{ID: "group", Scope: "project"}, {ID: "other-group", Scope: "project"}}
	v, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func reconcileLifecycle(t *testing.T, e *Engine) {
	t.Helper()
	if err := e.reconcileServiceLifecycle(); err != nil {
		t.Fatal(err)
	}
}
func TestServiceLifecycleAtomicHoldReplayAndOverlappingRoots(t *testing.T) {
	e, s, v := lifecycleEngine(t)
	r, err := e.TriggerService("member", "before")
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := e.TriggerService("unrelated", "outside")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	views := make(chan ServiceLifecycleView, 8)
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); op, err := e.StopService("group", "one"); views <- op; errs <- err }()
	}
	wg.Wait()
	close(views)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var op ServiceLifecycleView
	for got := range views {
		if op.Seq != 0 && op.Seq != got.Seq {
			t.Fatal("duplicate stop")
		}
		op = got
	}
	if op.TaskCount != 1 || op.Tasks[0].TaskID != r.TaskID || op.Tasks[0].Head.Seq == 0 || op.Status != "stopping" || len(op.Members) != 2 {
		t.Fatal("wrong frozen scope", op)
	}
	if ok, err := s.AcquireClaims(r.ID); err != nil || ok {
		t.Fatal("direct claim bypassed stop", err)
	}
	if _, err = e.TriggerService("member", "after"); err == nil {
		t.Fatal("new trigger bypassed stop")
	}
	if same, err := e.TriggerService("member", "before"); err != nil || same.ID != r.ID {
		t.Fatal("saved receipt unreadable during stop")
	}
	if _, err = e.ResumeService("group", "early"); !errors.Is(err, ErrConflict) {
		t.Fatal("premature resume", err)
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
	if !reflect.DeepEqual(op, restored) {
		t.Fatal("request/witness replay changed")
	}
	reconcileLifecycle(t, e)
	old, _ := replay.Get(r.ID)
	outside, _ := replay.Get(unrelated.ID)
	if old.Status != "cancelled" || outside.Status != "queued" {
		t.Fatal("wrong cancellation scope")
	}
	second, err := e.StopService("other-group", "two")
	if err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	if _, err = e.ResumeService("group", "resume"); err != nil {
		t.Fatal(err)
	}
	if _, err = e.TriggerService("member", "held"); err == nil {
		t.Fatal("overlapping hold released")
	}
	// Removing membership or the roots cannot lose an existing stop or its release.
	v.Catalog.Config.Targets = nil
	v.Catalog.Config.Units[0].PartOf = nil
	if _, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = e.ResumeService("other-group", "resume"); err != nil {
		t.Fatal(err)
	}
	if _, err = e.TriggerService("member", "fresh"); err != nil {
		t.Fatal(err)
	}
	same, err := e.StopService("other-group", "two")
	if err != nil || same.Seq != second.Seq || same.Status != "resumed" {
		t.Fatal("duplicate restopped removed root", err)
	}
	reconcileLifecycle(t, e)
	live, err := ReportAutonomy(replay.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(replay.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("lifecycle metric mismatch", err)
	}
}
func TestServiceLifecycleOutboxAndFutureEmission(t *testing.T) {
	e, s, _ := lifecycleEngine(t)
	root, err := e.EmitServiceEvent("seed", "one", []byte(`{}`))
	if err != nil || root.Pending != 1 {
		t.Fatal(err)
	}
	op, err := e.StopService("group", "stop")
	if err != nil {
		t.Fatal(err)
	}
	newRoot, err := e.EmitServiceEvent("seed", "two", []byte(`{}`))
	if err != nil || newRoot.Skipped != 1 || newRoot.Pending != 0 {
		t.Fatal("stopped service allocated new event", err)
	}
	reconcileLifecycle(t, e)
	closed, _ := s.CustomEvent(root.Seq)
	state, _ := s.ServiceLifecycle(op.Seq)
	if closed.Recipients[0].Status != "suppressed" || state.Status != "stopped" || s.pendingServiceEvents("member") != 0 {
		t.Fatal("outbox not drained", closed, state)
	}
	if _, err = e.ResumeService("group", "resume"); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileServiceEvents(); err != nil || len(s.Tasks()) != 0 {
		t.Fatal("resume restored suppressed event", err)
	}
	if _, err = e.EmitServiceEvent("seed", "three", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileServiceEvents(); err != nil || len(s.Tasks()) != 1 {
		t.Fatal("new events did not resume", err)
	}
}
func TestServiceLifecycleWaitsForWorkerAndClaims(t *testing.T) {
	e, s, v := lifecycleEngine(t)
	b := v.Catalog.Work["member"]
	b.Ownership.Paths = []string{"."}
	v.Catalog.Work["member"] = b
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	r, err := e.TriggerService("member", "running")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(r.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	stops := 0
	e.cancel = map[string]context.CancelFunc{r.ID: func() { stops++ }}
	op, err := e.StopService("group", "stop")
	if err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	reconcileLifecycle(t, e)
	if stops != 1 {
		t.Fatal("stop signal duplicated", stops)
	}
	if err = s.Transition(r.ID, "run_cancelled", "cancelled", "Stopped", "Fixture worker stopped"); err != nil {
		t.Fatal(err)
	}
	if err = s.ReleaseClaims(r.ID, "fixture"); err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	state, _ := s.ServiceLifecycle(op.Seq)
	if state.Status != "stopping" {
		t.Fatal("settled before worker exit")
	}
	delete(e.cancel, r.ID)
	reconcileLifecycle(t, e)
	state, _ = s.ServiceLifecycle(op.Seq)
	if state.Status != "stopped" {
		t.Fatal("did not settle after exit")
	}
	if _, err = e.ResumeService("group", "resume"); err != nil {
		t.Fatal(err)
	}
	// A replay with claims cannot use cancellation alone as proof of shutdown.
	next, err := e.TriggerService("member", "orphan")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(next.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err = e.StopService("group", "crash"); err != nil {
		t.Fatal(err)
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
	reconcileLifecycle(t, e)
	if _, err = e.ResumeService("group", "still-held"); !errors.Is(err, ErrConflict) {
		t.Fatal("uninspected claims allowed resume", err)
	}
	if err = e.ReleaseInspectedClaims(next.ID); err != nil {
		t.Fatal(err)
	}
	reconcileLifecycle(t, e)
	if _, err = e.ResumeService("group", "inspected"); err != nil {
		t.Fatal(err)
	}
}
func TestServiceLifecycleContinuousAndRetryStops(t *testing.T) {
	t.Run("continuous", func(t *testing.T) {
		e, s, _ := continuousEngine(t)
		reconcileContinuous(t, e, *continuousState(t, s).NextAt)
		if _, err := e.StopService("inventory", "stop"); err != nil {
			t.Fatal(err)
		}
		reconcileLifecycle(t, e)
		due := *continuousState(t, s).NextAt
		reconcileContinuous(t, e, due.Add(time.Minute))
		if len(s.Tasks()) != 1 {
			t.Fatal("continuous stop restarted")
		}
		if _, err := e.ResumeService("inventory", "resume"); err != nil {
			t.Fatal(err)
		}
		reconcileContinuous(t, e, due.Add(time.Minute))
		if len(s.Tasks()) != 2 {
			t.Fatal("continuous resume did not restore trigger")
		}
	})
	t.Run("retry", func(t *testing.T) {
		e, s, v := lifecycleEngine(t)
		count := 2
		v.Catalog.Config.Units[0].RestartPolicy = service.RestartPolicy{OnFailure: "retry", MaxRetries: &count}
		if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
			t.Fatal(err)
		}
		r, err := e.TriggerService("member", "retry")
		if err != nil {
			t.Fatal(err)
		}
		plan := failServiceAttempt(t, s, r.ID)
		if _, err = e.StopService("group", "stop"); err != nil {
			t.Fatal(err)
		}
		reconcileLifecycle(t, e)
		if err = e.reconcileServiceRetries(plan.DueAt); err != nil {
			t.Fatal(err)
		}
		task, _ := s.Task(r.TaskID)
		if task.Status != "cancelled" || len(s.List()) != 1 {
			t.Fatal("retry resurrected stopped work")
		}
	})
}

func TestServiceLifecycleTimedAndIngressGates(t *testing.T) {
	t.Run("timed", func(t *testing.T) {
		e, s, _ := intervalEngine(t, "queue")
		before, _ := s.Services()
		due := *before.States[0].NextAt
		if _, err := e.StopService("inventory", "stop"); err != nil {
			t.Fatal(err)
		}
		if err := e.scheduleServices(due, false); err != nil {
			t.Fatal(err)
		}
		after, _ := s.Services()
		if len(s.Tasks()) != 0 || after.States[0].LastTick == nil || after.States[0].LastTick.Outcome != "skipped" || !after.States[0].NextAt.After(due) {
			t.Fatal("stopped timer did not skip and advance")
		}
		reconcileLifecycle(t, e)
		if _, err := e.ResumeService("inventory", "resume"); err != nil {
			t.Fatal(err)
		}
		if err := e.scheduleServices(*after.States[0].NextAt, false); err != nil || len(s.Tasks()) != 1 {
			t.Fatal("next timer did not resume", err)
		}
	})
	t.Run("ingress", func(t *testing.T) {
		e, s, _ := ingressServices(t)
		old, err := e.DeliverWebhook("/hooks/jobs", "old", "", []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"hook", "signal"} {
			if _, err = e.StopService(id, "stop"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = e.DeliverWebhook("/hooks/jobs", "new", "", []byte(`{}`)); err == nil {
			t.Fatal("stopped webhook admitted")
		}
		if _, err = e.DeliverSignal("signal", "queue.jobs", "new", "", []byte(`{}`)); err == nil {
			t.Fatal("stopped signal admitted")
		}
		if same, err := e.DeliverWebhook("/hooks/jobs", "old", "", []byte(`{}`)); err != nil || same.ID != old.ID {
			t.Fatal("old delivery key unavailable")
		}
		reconcileLifecycle(t, e)
		if len(s.Tasks()) != 1 {
			t.Fatal("refusal created Tasks")
		}
		for _, id := range []string{"hook", "signal"} {
			if _, err = e.ResumeService(id, "resume"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err = e.DeliverWebhook("/hooks/jobs", "new", "", []byte(`{}`)); err != nil {
			t.Fatal("refusal consumed webhook key", err)
		}
		if _, err = e.DeliverSignal("signal", "queue.jobs", "new", "", []byte(`{}`)); err != nil {
			t.Fatal("refusal consumed signal key", err)
		}
	})
}
