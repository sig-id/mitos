package studio

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	"mitos/internal/service"
	"mitos/internal/workpool"
)

func restartEngine(t *testing.T, mode string, maxRetries int) (*Engine, *Store) {
	t.Helper()
	s := testStore(t)
	apps, _ := LoadApps("")
	e := &Engine{Store: s, Apps: apps, Root: t.TempDir()}
	c := serviceCatalog(e.Root)
	c.Config.Units[0].RestartPolicy = service.RestartPolicy{OnFailure: mode, MaxRetries: &maxRetries, Backoff: service.Backoff{Initial: 1, Multiplier: 2, Max: 4}}
	if _, err := e.ConfigureServices(c, 0); err != nil {
		t.Fatal(err)
	}
	return e, s
}
func failServiceAttempt(t *testing.T, s *Store, id string) ServiceRetryPlan {
	t.Helper()
	if ok, err := s.Schedule(id, nil); err != nil || !ok {
		t.Fatal("admit attempt", err)
	}
	if err := s.Transition(id, "run_failed", "failed", "Failed", "Fixture failure"); err != nil {
		t.Fatal(err)
	}
	run, _ := s.Get(id)
	var c change
	_ = json.Unmarshal(run.Events[len(run.Events)-1].Data, &c)
	if c.ServiceRetry == nil {
		t.Fatal("failure has no atomic retry policy result")
	}
	return *c.ServiceRetry
}
func TestServiceRetryBackoffTaskDependenciesAndAtomicReplay(t *testing.T) {
	e, s := restartEngine(t, "retry", 2)
	first := breakerRun(t, e, "root", false)
	plan := failServiceAttempt(t, s, first.ID)
	task, _ := s.Task(first.TaskID)
	if task.Status != "blocked" || !reflect.DeepEqual(task.WaitingFor, []string{"service-retry:" + first.ID}) || plan.Status != "pending" || plan.Binding.Attempt != 1 {
		t.Fatal("retrying Task became terminal")
	}
	b := testBrief(e.Root)
	b.Scheduling.Deps = []workpool.Dependency{{From: first.TaskID, Guard: "failed"}}
	failureBranch, err := s.Create(b, "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(failureBranch.ID, nil); err != nil || ok {
		t.Fatal("failure branch started before retry exhaustion")
	}
	if _, err = e.TriggerService("inventory", "overlap"); err == nil {
		t.Fatal("backoff failed to reserve service capacity")
	}
	if err = e.reconcileServiceRetries(plan.DueAt.Add(-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 2 {
		t.Fatal("retry started before backoff")
	}
	before, _ := s.Services()
	if err = s.Close(); err != nil {
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
		t.Fatal("backoff/deadline changed on replay")
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- e.reconcileServiceRetries(plan.DueAt) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	latest, _ := reopened.Task(first.TaskID)
	child, _ := reopened.Get(latest.CurrentRunID)
	if child.ParentID != first.ID || child.TaskID != first.TaskID || len(reopened.List()) != 3 || child.Brief.Scheduling.Deadline == nil || !child.Brief.Scheduling.Deadline.Equal(plan.Binding.Deadline) {
		t.Fatal("retry identity/deadline/dedup failed")
	}
	var created change
	_ = json.Unmarshal(child.Events[0].Data, &created)
	if created.ServiceRetry == nil || created.ServiceRetry.Status != "started" || created.ServiceRetry.ChildRunID != child.ID {
		t.Fatal("child creation did not consume plan atomically")
	}
	if ok, err := reopened.Schedule(child.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	if err = reopened.Complete(child.ID, "Recovered", true); err != nil {
		t.Fatal(err)
	}
	completed, _ := reopened.Task(first.TaskID)
	if completed.Status != "done" || len(completed.Runs) != 2 {
		t.Fatal("retry did not complete original Task")
	}
	if ok, err := reopened.Schedule(failureBranch.ID, nil); err != nil || ok {
		t.Fatal("eventually successful task satisfied failure guard")
	}
	live, err := ReportAutonomy(reopened.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(reopened.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("retry events broke metrics", err)
	}
}
func TestServiceRetryExhaustionPreservesRootDeadlineAndReleasesFailureGuard(t *testing.T) {
	e, s := restartEngine(t, "retry", 2)
	root := breakerRun(t, e, "root", false)
	first := failServiceAttempt(t, s, root.ID)
	if err := e.reconcileServiceRetries(first.DueAt); err != nil {
		t.Fatal(err)
	}
	task, _ := s.Task(root.TaskID)
	second := failServiceAttempt(t, s, task.CurrentRunID)
	if second.Binding.RootRunID != root.ID || second.Binding.Attempt != 2 || !second.Binding.Deadline.Equal(first.Binding.Deadline) {
		t.Fatal("retry reset lineage/deadline")
	}
	run, _ := s.Get(task.CurrentRunID)
	if second.DueAt.Sub(run.Events[len(run.Events)-1].At) != 2*time.Second {
		t.Fatal("second backoff did not grow")
	}
	if err := e.reconcileServiceRetries(second.DueAt); err != nil {
		t.Fatal(err)
	}
	task, _ = s.Task(root.TaskID)
	exhausted := failServiceAttempt(t, s, task.CurrentRunID)
	task, _ = s.Task(root.TaskID)
	if exhausted.Status != "exhausted" || task.Status != "failed" || len(task.Runs) != 3 {
		t.Fatal("retry limit failed")
	}
	if err := e.reconcileServiceRetries(second.DueAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 3 {
		t.Fatal("exhausted lineage restarted")
	}
	b := testBrief(e.Root)
	b.Scheduling.Deps = []workpool.Dependency{{From: root.TaskID, Guard: "failed"}}
	branch, err := s.Create(b, "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(branch.ID, nil); err != nil || !ok {
		t.Fatal("failure guard not released after exhaustion", err)
	}
}
func TestServiceRetryCancellationPolicyChangeDeadlineAndRejectedBinding(t *testing.T) {
	for _, scenario := range []string{"cancel", "disable", "policy", "expired", "missing-app", "manual-retry"} {
		t.Run(scenario, func(t *testing.T) {
			e, s := restartEngine(t, "retry", 1)
			root := breakerRun(t, e, "root", false)
			plan := failServiceAttempt(t, s, root.ID)
			switch scenario {
			case "cancel":
				if err := e.Cancel(root.ID); err != nil {
					t.Fatal(err)
				}
				if err := e.Cancel(root.ID); err != nil {
					t.Fatal("cancel not idempotent", err)
				}
			case "disable", "policy":
				v, _ := s.Services()
				if scenario == "disable" {
					v.Catalog.Config.Intent.Disable = []string{"inventory"}
				} else {
					v.Catalog.Config.Units[0].RestartPolicy.OnFailure = "skip"
				}
				if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
					t.Fatal(err)
				}
			case "expired":
				plan.DueAt = plan.Binding.Deadline
			case "missing-app":
				e.Apps = nil
			case "manual-retry":
				if _, err := e.Retry(root.ID); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.reconcileServiceRetries(plan.DueAt); err != nil {
				t.Fatal(err)
			}
			count := 1
			if scenario == "manual-retry" {
				count = 2
			}
			if len(s.List()) != count || s.serviceRetryPlans[root.ID].Status == "pending" {
				t.Fatal("closed retry was restarted or left pending")
			}
			task, _ := s.Task(root.TaskID)
			if scenario == "cancel" && task.Status != "cancelled" {
				t.Fatal("cancelled pending retry left active Task")
			}
			if scenario != "cancel" && scenario != "manual-retry" && task.Status != "failed" {
				t.Fatal("closed retry left nonterminal Task")
			}
			if _, err := AuditAutonomyLogs(s.Dir()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestServiceAbortIsLocalAndReactivationResetsIt(t *testing.T) {
	e, s := restartEngine(t, "abort", 0)
	v, _ := s.Services()
	v.Catalog.Config.Units = append(v.Catalog.Config.Units, service.Unit{ID: "other", Scope: "project", Trigger: "manual"})
	v.Catalog.Work["other"] = testBrief(e.Root)
	v.Catalog.Config.Intent.Enable = []string{"other"}
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	root := breakerRun(t, e, "root", false)
	plan := failServiceAttempt(t, s, root.ID)
	if plan.Status != "aborted" {
		t.Fatal("abort policy did not latch")
	}
	if _, err := e.TriggerService("inventory", "blocked"); err == nil {
		t.Fatal("aborted service accepted new work")
	}
	if _, err := e.TriggerService("other", "allowed"); err != nil {
		t.Fatal("abort leaked across services", err)
	}
	duplicate, err := e.TriggerService("inventory", "root")
	if err != nil || duplicate.ID != root.ID {
		t.Fatal("abort hid original receipt")
	}
	v, _ = s.Services()
	v.Catalog.Config.Intent.Disable = []string{"inventory"}
	v, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	v.Catalog.Config.Intent.Disable = nil
	if _, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err = e.TriggerService("inventory", "resumed"); err != nil {
		t.Fatal("reactivation did not reset abort", err)
	}
}
func TestServiceRetryStillObeysOpenCircuitBeforeClaims(t *testing.T) {
	e, s := restartEngine(t, "retry", 1)
	v, _ := s.Services()
	v.Catalog.Config.Units[0].CircuitBreaker = service.BreakerConfig{Enabled: true, Threshold: 1, Cooldown: 300}
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	root := breakerRun(t, e, "root", false)
	plan := failServiceAttempt(t, s, root.ID)
	if err := e.reconcileServiceRetries(plan.DueAt); err != nil {
		t.Fatal(err)
	}
	task, _ := s.Task(root.TaskID)
	if ok, err := s.Schedule(task.CurrentRunID, nil); err != nil || ok {
		t.Fatal("service retry bypassed circuit", err)
	}
	child, _ := s.Get(task.CurrentRunID)
	if child.Claim != nil || child.Execution != nil {
		t.Fatal("breaker gate applied after execution")
	}
}

func TestServiceRetryBackoffCannotExtendOriginalTaskBudget(t *testing.T) {
	e, s := restartEngine(t, "retry", 2)
	v, _ := s.Services()
	v.Catalog.Config.Units[0].RestartPolicy.Backoff = service.Backoff{Initial: 60, Multiplier: 2, Max: 60}
	brief := v.Catalog.Work["inventory"]
	deadline := time.Now().Add(30 * time.Second)
	brief.Scheduling.Deadline = &deadline
	v.Catalog.Work["inventory"] = brief
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	root := breakerRun(t, e, "root", false)
	plan := failServiceAttempt(t, s, root.ID)
	task, _ := s.Task(root.TaskID)
	if plan.Status != "expired" || task.Status != "failed" || !plan.Binding.Deadline.Equal(deadline) {
		t.Fatal("backoff extended original budget")
	}
	if err := e.reconcileServiceRetries(plan.DueAt); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 1 {
		t.Fatal("out-of-budget retry created work")
	}
}
func TestServiceRestartSkipAndZeroRetriesDoNotCreateNewAttempts(t *testing.T) {
	for _, mode := range []string{"skip", "retry"} {
		t.Run(mode, func(t *testing.T) {
			e, s := restartEngine(t, mode, 0)
			root := breakerRun(t, e, "root", true)
			if err := s.Transition(root.ID, "run_failed", "failed", "Failed", "Fixture failure"); err != nil {
				t.Fatal(err)
			}
			if err := e.reconcileServiceRetries(time.Now().Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			task, _ := s.Task(root.TaskID)
			if len(s.List()) != 1 || task.Status != "failed" {
				t.Fatal("skip/zero produced a retry")
			}
		})
	}
}
