package studio

import (
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

func continuousEngine(t *testing.T) (*Engine, *Store, ServiceView) {
	t.Helper()
	e, s, v := intervalEngine(t, "skip")
	c := v.Catalog
	c.Config.Units[0].Trigger = "continuous"
	c.Config.Units[0].Schedule = &service.Schedule{Type: "continuous"}
	v, err := e.ConfigureServices(c, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func continuousState(t *testing.T, s *Store) *ServiceContinuous {
	t.Helper()
	v, err := s.Services()
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range v.States {
		if state.ServiceID == "inventory" {
			return state.Continuous
		}
	}
	t.Fatal("missing continuous state")
	return nil
}
func reconcileContinuous(t *testing.T, e *Engine, at time.Time) {
	t.Helper()
	if err := e.reconcileContinuousServices(at); err != nil {
		t.Fatal(err)
	}
}
func TestContinuousAtomicLifecycleDelayAndReplay(t *testing.T) {
	e, s, v := continuousEngine(t)
	state := continuousState(t, s)
	if state.Status != "waiting" || state.Epoch != v.Catalog.Revision || state.NextAt == nil || v.States[0].NextAt != nil {
		t.Fatal("activation did not initialize controller", state)
	}
	due := *state.NextAt
	reconcileContinuous(t, e, due.Add(-time.Nanosecond))
	if len(s.Tasks()) != 0 {
		t.Fatal("early initial start")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- e.reconcileContinuousServices(due) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(s.Tasks()) != 1 {
		t.Fatal("duplicate initial generation")
	}
	first := s.Tasks()[0]
	run, _ := s.Get(first.CurrentRunID)
	var c change
	_ = json.Unmarshal(run.Events[0].Data, &c)
	if c.ServiceContinuous == nil || c.ServiceContinuous.TaskID != first.ID || c.ServiceCall.ContinuousEpoch != state.Epoch || first.ContinuousEpoch != state.Epoch {
		t.Fatal("receipt and ownership not atomic")
	}
	reconcileContinuous(t, e, due.Add(24*time.Hour))
	if len(s.Tasks()) != 1 {
		t.Fatal("queued task did not retain generation")
	}
	finishEventRun(t, s, run.ID)
	run, _ = s.Get(run.ID)
	terminal := run.Events[len(run.Events)-1]
	_ = json.Unmarshal(terminal.Data, &c)
	state = continuousState(t, s)
	if state.Status != "waiting" || state.NextAt == nil || !state.NextAt.Equal(terminal.At.Add(5*time.Second)) || c.ServiceContinuous == nil {
		t.Fatal("terminal did not bind restart delay", state)
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
	after, _ := replay.Services()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("replay changed saved delay")
	}
	reconcileContinuous(t, e, state.NextAt.Add(-time.Nanosecond))
	if len(replay.Tasks()) != 1 {
		t.Fatal("restarted before delay")
	}
	reconcileContinuous(t, e, state.NextAt.Add(48*time.Hour))
	reconcileContinuous(t, e, state.NextAt.Add(48*time.Hour))
	if len(replay.Tasks()) != 2 {
		t.Fatal("downtime backfilled generations")
	}
	next := continuousState(t, replay)
	if next.Generation != 2 || next.TaskID == first.ID {
		t.Fatal("restart did not create fresh Task")
	}
	if _, err = e.Retry(run.ID); err == nil {
		t.Fatal("old generation was resurrected")
	}
	live, err := ReportAutonomy(replay.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(replay.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live, offline) {
		t.Fatal("metrics readers disagree")
	}
}
func TestContinuousDisableChangeAndPreClaimFencing(t *testing.T) {
	for _, mode := range []string{"disable", "remove", "work", "schedule", "manual"} {
		t.Run(mode, func(t *testing.T) {
			e, s, v := continuousEngine(t)
			reconcileContinuous(t, e, time.Now())
			first := s.Tasks()[0]
			c := v.Catalog
			switch mode {
			case "disable":
				c.Config.Intent.Disable = []string{"inventory"}
				c.Config.Intent.Targets = nil
				c.Config.Intent.Enable = nil
			case "remove":
				c = ServiceCatalog{Config: service.Config{}, Work: map[string]Brief{}}
			case "work":
				b := c.Work["inventory"]
				b.Title = "New continuous binding"
				c.Work["inventory"] = b
			case "schedule":
				delay := 1
				c.Config.Units[0].Schedule.RestartDelay = &delay
			case "manual":
				c.Config.Units[0].Trigger = "manual"
				c.Config.Units[0].Schedule = nil
			}
			if _, err := e.ConfigureServices(c, v.Catalog.Revision); err != nil {
				t.Fatal(err)
			}
			if ok, err := s.Schedule(first.CurrentRunID, nil); err != nil || ok {
				t.Fatal("retiring task acquired execution", err)
			}
			reconcileContinuous(t, e, time.Now())
			run, _ := s.Get(first.CurrentRunID)
			if run.Status != "cancelled" || run.Claim != nil {
				t.Fatal("queued generation not stopped before claims", run.Status)
			}
			if mode == "work" || mode == "schedule" {
				next := continuousState(t, s)
				if next.Status != "waiting" {
					t.Fatal("replacement did not wait", next)
				}
				reconcileContinuous(t, e, *next.NextAt)
				if len(s.Tasks()) != 2 {
					t.Fatal("replacement not started")
				}
			} else {
				reconcileContinuous(t, e, time.Now().Add(24*time.Hour))
				if len(s.Tasks()) != 1 {
					t.Fatal("disabled generation restarted")
				}
			}
		})
	}
}
func TestContinuousUnrelatedEditDoesNotRestartAndCancellationRestarts(t *testing.T) {
	e, s, v := continuousEngine(t)
	reconcileContinuous(t, e, time.Now())
	first := s.Tasks()[0]
	before := continuousState(t, s)
	c := v.Catalog
	c.Config.Units[0].Description = "An unrelated edit"
	if _, err := e.ConfigureServices(c, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, continuousState(t, s)) {
		t.Fatal("unrelated edit changed generation")
	}
	if err := e.Cancel(first.CurrentRunID); err != nil {
		t.Fatal(err)
	}
	next := continuousState(t, s)
	if next.Status != "waiting" {
		t.Fatal("active desired state lost after cancellation")
	}
	reconcileContinuous(t, e, *next.NextAt)
	if len(s.Tasks()) != 2 {
		t.Fatal("active service did not replace cancelled work")
	}
}
func TestContinuousCapacityPauseAndAppRejection(t *testing.T) {
	e, s, v := continuousEngine(t)
	manual, err := e.TriggerService("inventory", "occupy")
	if err != nil {
		t.Fatal(err)
	}
	reconcileContinuous(t, e, time.Now())
	state := continuousState(t, s)
	if state.Status != "blocked" || len(s.Tasks()) != 1 {
		t.Fatal("controller bypassed capacity")
	}
	seq := s.seq
	reconcileContinuous(t, e, time.Now())
	if s.seq != seq {
		t.Fatal("unchanged blocker generated log churn")
	}
	finishEventRun(t, s, manual.ID)
	e.Apps = nil
	reconcileContinuous(t, e, time.Now())
	state = continuousState(t, s)
	if state.Status != "rejected" || state.NextAt != nil {
		t.Fatal("unavailable app not paused")
	}
	seq = s.seq
	reconcileContinuous(t, e, time.Now().Add(24*time.Hour))
	if s.seq != seq {
		t.Fatal("rejected generation retried intake")
	}
	e.Apps, _ = LoadApps("")
	c := v.Catalog
	c.Config.Intent.Disable = []string{"inventory"}
	c.Config.Intent.Targets = nil
	c.Config.Intent.Enable = nil
	disabled, err := e.ConfigureServices(c, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	c.Config.Intent.Disable = nil
	c.Config.Intent.Enable = []string{"inventory"}
	if _, err = e.ConfigureServices(c, disabled.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	reconcileContinuous(t, e, time.Now())
	if len(s.Tasks()) != 2 {
		t.Fatal("reactivation did not retry intake")
	}
	if _, err = AuditAutonomyLogs(s.Dir()); err != nil {
		t.Fatal(err)
	}
}

func TestContinuousRetrySettlementAbortAndParkedRecovery(t *testing.T) {
	for _, mode := range []string{"retry", "abort", "parked"} {
		t.Run(mode, func(t *testing.T) {
			e, s, v := continuousEngine(t)
			count := 1
			c := v.Catalog
			if mode != "parked" {
				c.Config.Units[0].RestartPolicy = service.RestartPolicy{OnFailure: mode, MaxRetries: &count, Backoff: service.Backoff{Initial: 1, Multiplier: 2, Max: 4}}
				var err error
				v, err = e.ConfigureServices(c, v.Catalog.Revision)
				if err != nil {
					t.Fatal(err)
				}
			}
			reconcileContinuous(t, e, time.Now())
			first := s.Tasks()[0]
			if mode == "parked" {
				if ok, err := s.Schedule(first.CurrentRunID, nil); err != nil || !ok {
					t.Fatal(err)
				}
				if err := s.Transition(first.CurrentRunID, "run_started", "running", "Running", "Fixture"); err != nil {
					t.Fatal(err)
				}
				// Engine restart must park interrupted work, retaining its generation.
				engine, err := NewEngine(s, e.Apps, e.Root)
				if err != nil {
					t.Fatal(err)
				}
				engine.Close()
				e = engine
				run, _ := s.Get(first.CurrentRunID)
				if run.Status != "parked" || len(s.Tasks()) != 1 || continuousState(t, s).Status != "running" {
					t.Fatal("restart replaced interrupted generation", run.Status)
				}
				child, err := e.Resolve(first.CurrentRunID, run.Decision.ID, "retry")
				if err != nil {
					t.Fatal(err)
				}
				current, _ := s.Task(first.ID)
				if current.CurrentRunID != child || current.ContinuousEpoch != first.ContinuousEpoch || len(s.Tasks()) != 1 {
					t.Fatal("human recovery escaped managed Task")
				}
				return
			}
			plan := failServiceAttempt(t, s, first.CurrentRunID)
			if mode == "retry" {
				if continuousState(t, s).Status != "running" {
					t.Fatal("retryable failure started continuous delay")
				}
				reconcileContinuous(t, e, time.Now().Add(time.Hour))
				if len(s.Tasks()) != 1 {
					t.Fatal("continuous bypassed retry plan")
				}
				if err := e.reconcileServiceRetries(plan.DueAt); err != nil {
					t.Fatal(err)
				}
				child, _ := s.Task(first.ID)
				if child.ContinuousEpoch != first.ContinuousEpoch || child.CurrentRunID == first.CurrentRunID {
					t.Fatal("retry lost lifecycle identity")
				}
				plan = failServiceAttempt(t, s, child.CurrentRunID)
				if plan.Status != "exhausted" {
					t.Fatal(plan)
				}
			}
			next := continuousState(t, s)
			if next.Status != "waiting" || next.NextAt == nil {
				t.Fatal("final Task did not start delay")
			}
			reconcileContinuous(t, e, *next.NextAt)
			if mode == "abort" {
				if len(s.Tasks()) != 1 || continuousState(t, s).Status != "blocked" {
					t.Fatal("continuous bypassed abort")
				}
			} else if len(s.Tasks()) != 2 {
				t.Fatal("exhausted Task did not start next continuous generation")
			}
		})
	}
}
func TestContinuousDisablePendingRetry(t *testing.T) {
	e, s, v := continuousEngine(t)
	count := 2
	c := v.Catalog
	c.Config.Units[0].RestartPolicy = service.RestartPolicy{OnFailure: "retry", MaxRetries: &count}
	v, err := e.ConfigureServices(c, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	reconcileContinuous(t, e, time.Now())
	first := s.Tasks()[0]
	failServiceAttempt(t, s, first.CurrentRunID)
	c = v.Catalog
	c.Config.Intent.Targets = nil
	c.Config.Intent.Disable = []string{"inventory"}
	if _, err = e.ConfigureServices(c, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	reconcileContinuous(t, e, time.Now())
	if state := continuousState(t, s); state.Status != "disabled" {
		t.Fatal("pending retry retained retired controller", state)
	}
	if err = e.reconcileServiceRetries(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 1 {
		t.Fatal("disabled pending retry ran")
	}
}

func TestContinuousRealCommandDisableStopsAndHarvests(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "ready")
	app := executionApp(t, fmt.Sprintf("printf result > result.txt; printf ready > '%s'; sleep 60", marker))
	e, s := testEngine(t, root, []Application{app})
	c := serviceCatalog(root)
	c.Config.Units[0].Trigger = "continuous"
	c.Config.Units[0].Schedule = &service.Schedule{Type: "continuous"}
	b := c.Work["inventory"]
	b.AppID = app.ID
	c.Work["inventory"] = b
	v, err := e.ConfigureServices(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err = os.Stat(marker); err == nil {
			break
		}
		time.Sleep(15 * time.Millisecond)
	}
	if _, err = os.Stat(marker); err != nil {
		t.Fatal("daemon did not execute", err)
	}
	tasks := s.Tasks()
	if len(tasks) != 1 {
		t.Fatal("multiple managed tasks")
	}
	c = v.Catalog
	c.Config.Intent.Targets = nil
	c.Config.Intent.Disable = []string{"inventory"}
	if _, err = e.ConfigureServices(c, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	run := waitRun(t, s, tasks[0].CurrentRunID, func(r Run) bool { return r.Terminal() })
	if run.Status != "cancelled" {
		t.Fatal("daemon not cancelled", run.Status)
	}
	changes := readChanges(t, s, run)
	if len(changes.Changes) != 1 || changes.Changes[0].Path != "result.txt" {
		t.Fatal("shutdown lost harvested result", changes)
	}
	if continuousState(t, s).Status != "disabled" {
		t.Fatal("shutdown lost desired state")
	}
	if _, err = os.Stat(filepath.Join(root, "result.txt")); !os.IsNotExist(err) {
		t.Fatal("source changed")
	}
}

func TestContinuousBreakerCooldownAndCrashClaims(t *testing.T) {
	for _, mode := range []string{"breaker", "claims"} {
		t.Run(mode, func(t *testing.T) {
			e, s, v := continuousEngine(t)
			c := v.Catalog
			if mode == "breaker" {
				delay := 0
				c.Config.Units[0].Schedule.RestartDelay = &delay
				c.Config.Units[0].CircuitBreaker = service.BreakerConfig{Enabled: true, Threshold: 1, Cooldown: 60, HalfOpenRequests: 1}
			} else {
				b := c.Work["inventory"]
				b.Ownership.Paths = []string{"."}
				c.Work["inventory"] = b
			}
			v, err := e.ConfigureServices(c, v.Catalog.Revision)
			if err != nil {
				t.Fatal(err)
			}
			reconcileContinuous(t, e, time.Now())
			first := s.Tasks()[0]
			if ok, err := s.Schedule(first.CurrentRunID, nil); err != nil || !ok {
				t.Fatal(err)
			}
			if mode == "breaker" {
				if err := s.Transition(first.CurrentRunID, "run_failed", "failed", "Failed", "Fixture"); err != nil {
					t.Fatal(err)
				}
				reconcileContinuous(t, e, *continuousState(t, s).NextAt)
				if continuousState(t, s).Status != "blocked" || len(s.Tasks()) != 1 {
					t.Fatal("controller bypassed open circuit")
				}
				view, _ := s.Services()
				until := view.States[0].Breaker.OpenUntil
				if until == nil {
					t.Fatal("no cooldown")
				}
				reconcileContinuous(t, e, *until)
				if len(s.Tasks()) != 2 {
					t.Fatal("cooldown did not release due generation")
				}
			} else {
				if err = s.Park(first.CurrentRunID, recoveryDecision()); err != nil {
					t.Fatal(err)
				}
				c = v.Catalog
				c.Config.Intent.Targets = nil
				c.Config.Intent.Disable = []string{"inventory"}
				if _, err = e.ConfigureServices(c, v.Catalog.Revision); err != nil {
					t.Fatal(err)
				}
				reconcileContinuous(t, e, time.Now())
				run, _ := s.Get(first.CurrentRunID)
				if run.Status != "cancelled" || run.Claim == nil || run.Claim.ReleasedAt != nil {
					t.Fatal("automatic retirement released uninspected crash claim")
				}
			}
		})
	}
}
