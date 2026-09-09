package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"mitos/internal/compute"
	"mitos/internal/service"
)

func healthEngine(t *testing.T, h service.Health, command string, policy string) (*Engine, *Store, ServiceView) {
	t.Helper()
	root := t.TempDir()
	app := executionApp(t, command)
	e, s := testEngine(t, root, []Application{app})
	c := serviceCatalog(root)
	c.Config.Units[0].Health = &h
	count := 1
	c.Config.Units[0].RestartPolicy = service.RestartPolicy{OnFailure: policy, MaxRetries: &count, Backoff: service.Backoff{Initial: 1, Multiplier: 2, Max: 2}}
	b := c.Work["inventory"]
	b.AppID = app.ID
	c.Work["inventory"] = b
	v, err := e.ConfigureServices(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func waitHealth(t *testing.T, s *Store, id string, predicate func(Run) bool) Run {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		run, err := s.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if predicate(run) {
			return run
		}
		time.Sleep(20 * time.Millisecond)
	}
	run, _ := s.Get(id)
	t.Fatalf("health condition not reached: status=%s health=%+v", run.Status, run.Health)
	return run
}
func TestServiceHealthFileLogAndKill(t *testing.T) {
	for _, action := range []string{"log", "kill"} {
		t.Run(action, func(t *testing.T) {
			command := "printf result > result.txt; sleep 2.5"
			if action == "kill" {
				command = "printf result > result.txt; sleep 60"
			}
			e, s, _ := healthEngine(t, service.Health{Type: "file", Path: "missing-heartbeat", Interval: 1, OnUnhealthy: action}, command, "retry")
			run := replacementRun(t, e, "one")
			run = waitHealth(t, s, run.ID, func(r Run) bool { return r.Terminal() })
			if run.Health == nil || run.Health.Status != "closed" || run.Health.Check == nil || run.Health.Check.Outcome != "unhealthy" {
				t.Fatal("missing durable health result", run.Health)
			}
			if action == "log" {
				if run.Status != "done" || !run.Verified || run.Health.Action != "" {
					t.Fatal("log changed execution outcome", run.Status)
				}
			} else {
				if run.Status != "failed" || run.Verified || run.Health.Action != "kill" {
					t.Fatal("kill did not fail attempt", run.Status)
				}
				view, _ := s.Services()
				if len(view.States[0].Retries) != 0 {
					t.Fatal("kill scheduled failure retry")
				}
			}
			changes := readChanges(t, s, run)
			if len(changes.Changes) != 1 || changes.Changes[0].Path != "result.txt" {
				t.Fatal("health stop lost harvest")
			}
			if _, err := os.Stat(filepath.Join(e.Root, "result.txt")); !os.IsNotExist(err) {
				t.Fatal("health changed source")
			}
		})
	}
}
func TestServiceHealthRestartUsesBoundedTaskRetries(t *testing.T) {
	e, s, _ := healthEngine(t, service.Health{Type: "command", Command: "printf diagnostic; exit 1", Interval: 1, OnUnhealthy: "restart"}, "printf result > result.txt; sleep 60", "skip")
	first := replacementRun(t, e, "one")
	failed := waitHealth(t, s, first.ID, func(r Run) bool { return r.Terminal() })
	var failure change
	_ = json.Unmarshal(failed.Events[len(failed.Events)-1].Data, &failure)
	if failure.ServiceRetry == nil || failure.ServiceRetry.Cause != "health" || failure.ServiceRetry.Status != "pending" || failure.Task.Status != "blocked" {
		t.Fatal("health restart not atomic with failed attempt")
	}
	deadline := time.Now().Add(12 * time.Second)
	var task Task
	for time.Now().Before(deadline) {
		task, _ = s.Task(first.TaskID)
		if task.Status == "failed" && len(task.Runs) == 2 {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if task.Status != "failed" || len(task.Runs) != 2 || len(s.Tasks()) != 1 {
		t.Fatal("health retries not bounded in original Task", task.Status, len(task.Runs))
	}
	last, _ := s.Get(task.CurrentRunID)
	if last.Brief.Scheduling.Deadline == nil || !last.Brief.Scheduling.Deadline.Equal(failure.ServiceRetry.Binding.Deadline) || last.Health.Check.OutputSHA256 == "" {
		t.Fatal("health retry lost deadline or evidence")
	}
	live, err := ReportAutonomy(s.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(s.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("health metrics disagree", err)
	}
}
func TestServiceHealthCommandTimeoutIsBounded(t *testing.T) {
	e, s, _ := healthEngine(t, service.Health{Type: "command", Command: "sleep 60", Interval: 1, OnUnhealthy: "kill"}, "printf result > result.txt; sleep 60", "skip")
	run := replacementRun(t, e, "one")
	run = waitHealth(t, s, run.ID, func(r Run) bool { return r.Terminal() })
	if run.Status != "failed" || run.Health.Check.Outcome != "unhealthy" || !strings.Contains(run.Health.Check.Detail, "deadline") {
		t.Fatal("command probe not bounded", run.Health)
	}
}
func TestServiceHealthHeartbeatScopeDedupAndExpiry(t *testing.T) {
	e, s, _ := healthEngine(t, service.Health{Type: "heartbeat", MaxAge: 2, Interval: 1, OnUnhealthy: "kill"}, "printf result > result.txt; sleep 60", "skip")
	run := replacementRun(t, e, "one")
	run = waitHealth(t, s, run.ID, func(r Run) bool { return r.Health != nil && r.Health.Status == "watching" })
	monitor := run.Health.MonitorID
	beat, err := s.ServiceHeartbeat(run.ID, monitor, 1)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.ServiceHeartbeat(run.ID, monitor, 1)
	if err != nil || !duplicate.HeartbeatAt.Equal(*beat.HeartbeatAt) {
		t.Fatal("duplicate heartbeat refreshed clock")
	}
	if _, err = s.ServiceHeartbeat(run.ID, "old-monitor", 2); err == nil {
		t.Fatal("old scope heartbeat accepted")
	}
	if _, err = s.ServiceHeartbeat(run.ID, monitor, 0); err == nil {
		t.Fatal("zero sequence accepted")
	}
	run = waitHealth(t, s, run.ID, func(r Run) bool { return r.Terminal() })
	if run.Status != "failed" || run.Health.HeartbeatSequence != 1 || run.Health.Check.Outcome != "unhealthy" {
		t.Fatal("heartbeat did not expire", run.Health)
	}
	if _, err = s.ServiceHeartbeat(run.ID, monitor, 2); err == nil {
		t.Fatal("terminal heartbeat revived health")
	}
}

func TestServiceHealthHeartbeatWithdrawsObsoleteExpiry(t *testing.T) {
	for _, phase := range []string{"before-result", "before-action"} {
		t.Run(phase, func(t *testing.T) {
			s := testStore(t)
			root := t.TempDir()
			app := executionApp(t, "printf result > result.txt")
			e := &Engine{Store: s, Root: root, Apps: []Application{app}}
			c := serviceCatalog(root)
			c.Config.Units[0].Health = &service.Health{Type: "heartbeat", MaxAge: 1, Interval: 1, OnUnhealthy: "kill"}
			b := c.Work["inventory"]
			b.AppID = app.ID
			c.Work["inventory"] = b
			if _, err := e.ConfigureServices(c, 0); err != nil {
				t.Fatal(err)
			}
			run := replacementRun(t, e, "one")
			if ok, err := s.Schedule(run.ID, nil); err != nil || !ok {
				t.Fatal(err)
			}
			scope, err := s.startHealth(run.ID, "test-segment")
			if err != nil {
				t.Fatal(err)
			}
			// Persist an expired scope without a wall-clock wait or a live worker.
			scope.StartedAt = time.Now().UTC().Add(-time.Minute)
			due := scope.StartedAt.Add(time.Second)
			scope.NextAt = &due
			s.mu.Lock()
			err = s.commit(run.ID, "effect", "service_health_started", change{ServiceHealth: scope})
			s.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			pending, err := s.healthCheckStart(run.ID, scope.MonitorID, time.Now().UTC())
			if err != nil || pending == nil {
				t.Fatal("check did not start", err)
			}
			check := *pending.Check
			evaluateHeartbeat(pending, &check, time.Now().UTC())
			if check.Outcome != "unhealthy" {
				t.Fatal("fixture has not expired")
			}
			beat := func() {
				t.Helper()
				if _, err := s.ServiceHeartbeat(run.ID, scope.MonitorID, 1); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "before-result" {
				beat()
			}
			stop, err := s.healthCheckFinish(run.ID, scope.MonitorID, check)
			if err != nil || stop != (phase == "before-action") {
				t.Fatal("result ignored current heartbeat", stop, err)
			}
			if phase == "before-action" {
				beat()
				action, resume, err := s.healthAction(run.ID, scope.MonitorID)
				if err != nil || action != "" || !resume {
					t.Fatal("new heartbeat did not resume monitoring", action, resume, err)
				}
			}
			current, _ := s.Get(run.ID)
			if current.Health.Status != "watching" || current.Health.Action != "" || current.Health.HeartbeatAt == nil {
				t.Fatal("obsolete expiry stopped or closed monitoring", current.Health)
			}
		})
	}
}
func TestServiceHealthStaleProbeCannotStopChangedPolicy(t *testing.T) {
	control := t.TempDir()
	ready := filepath.Join(control, "probe-ready")
	release := filepath.Join(control, "release")
	e, s, v := healthEngine(t, service.Health{Type: "command", Command: fmt.Sprintf("printf ready > '%s'; sleep 60", ready), Interval: 1, OnUnhealthy: "kill"}, fmt.Sprintf("printf result > result.txt; while test ! -f '%s'; do sleep .05; done", release), "skip")
	run := replacementRun(t, e, "one")
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		t.Fatal("probe did not start", err)
	}
	c := v.Catalog
	c.Config.Units[0].Health = nil
	if _, err := e.ConfigureServices(c, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	current := waitHealth(t, s, run.ID, func(r Run) bool { return r.Health != nil && r.Health.Status == "closed" })
	if current.Status != "running" || current.Health.Action != "" || current.Health.Check.Outcome != "superseded" {
		t.Fatal("stale probe stopped execution", current.Status, current.Health)
	}
	if err := os.WriteFile(release, []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	current = waitHealth(t, s, run.ID, func(r Run) bool { return r.Terminal() })
	if current.Status != "done" {
		t.Fatal("worker failed after policy withdrawal", current.Status)
	}
}
func TestServiceHealthReplayDoesNotRerunPendingProbe(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "probe-must-not-run")
	app := executionApp(t, "printf result > result.txt")
	e := &Engine{Store: s, Root: root, Apps: []Application{app}, provider: &compute.Local{}}
	c := serviceCatalog(root)
	c.Config.Units[0].Health = &service.Health{Type: "command", Command: fmt.Sprintf("printf unexpected > '%s'", marker), Interval: 1, OnUnhealthy: "restart"}
	b := c.Work["inventory"]
	b.AppID = app.ID
	c.Work["inventory"] = b
	if _, err := e.ConfigureServices(c, 0); err != nil {
		t.Fatal(err)
	}
	run := replacementRun(t, e, "one")
	if ok, err := s.Schedule(run.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	scope, err := s.startHealth(run.ID, "test-segment")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.healthCheckStart(run.ID, scope.MonitorID, *scope.NextAt); err != nil {
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
	restarted, err := NewEngine(replay, []Application{app}, root)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	current, _ := replay.Get(run.ID)
	if current.Status != "parked" || current.Health.Status != "closed" || current.Health.Check.Outcome != "interrupted" {
		t.Fatal("pending probe not closed on restart", current.Status, current.Health)
	}
	if _, err = os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("replay executed probe")
	}
	if len(replay.Tasks()) != 1 {
		t.Fatal("replay created unattended health retry")
	}
}
func TestServiceHealthFileConfinesLinks(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "fresh")
	if err := os.WriteFile(outside, []byte("fresh"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "beat")); err != nil {
		t.Fatal(err)
	}
	app := executionApp(t, "printf result > result.txt")
	e := &Engine{Store: s, Root: root, Apps: []Application{app}}
	c := serviceCatalog(root)
	c.Config.Units[0].Health = &service.Health{Type: "file", Path: "beat", Interval: 1, OnUnhealthy: "kill"}
	b := c.Work["inventory"]
	b.AppID = app.ID
	c.Work["inventory"] = b
	if _, err := e.ConfigureServices(c, 0); err != nil {
		t.Fatal(err)
	}
	run := replacementRun(t, e, "one")
	if ok, err := s.Schedule(run.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	state, err := s.startHealth(run.ID, "test-segment")
	if err != nil {
		t.Fatal(err)
	}
	launch := compute.LaunchSpec{Workspace: compute.Workspace{Directory: root}}
	monitor := serviceHealthMonitor{store: s, runID: run.ID, scope: state, launch: launch}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err = monitor.watch(ctx); err != errHealthKill {
		t.Fatal("escaping link passed health check", err)
	}
	current, _ := s.Get(run.ID)
	if current.Health.Check.EvidenceAt != nil {
		t.Fatal("health followed escaping link")
	}
}

func TestServiceHealthHealthyEvidenceDoesNotGrantAcceptance(t *testing.T) {
	release := filepath.Join(t.TempDir(), "release")
	e, s, _ := healthEngine(t, service.Health{Type: "command", Command: "test -s result.txt", Interval: 1, OnUnhealthy: "kill"}, fmt.Sprintf("printf result > result.txt; while test ! -f '%s'; do sleep .05; done", release), "skip")
	// The installed verifier is deliberately failing. Health only reports liveness.
	e.Apps[0].Acceptance.Cmd = []string{"/bin/sh", "-c", "exit 1"}
	run := replacementRun(t, e, "one")
	waitHealth(t, s, run.ID, func(r Run) bool {
		return r.Health != nil && r.Health.Check != nil && r.Health.Check.Outcome == "healthy"
	})
	if err := os.WriteFile(release, []byte("finish"), 0600); err != nil {
		t.Fatal(err)
	}
	current := waitHealth(t, s, run.ID, func(r Run) bool { return r.Terminal() })
	if current.Status != "failed" || current.Verified || current.Health.Action != "" {
		t.Fatal("healthy check became acceptance verdict", current.Status)
	}
}
func TestServiceHealthPolicyChangeSuppressesPendingRestart(t *testing.T) {
	e, s, v := healthEngine(t, service.Health{Type: "file", Path: "missing", Interval: 1, OnUnhealthy: "restart"}, "printf result > result.txt; sleep 60", "skip")
	// Widen backoff so the edit deterministically precedes retry consumption.
	c := v.Catalog
	c.Config.Units[0].RestartPolicy.Backoff.Initial = 30
	c.Config.Units[0].RestartPolicy.Backoff.Max = 30
	v, err := e.ConfigureServices(c, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	run := replacementRun(t, e, "one")
	waitHealth(t, s, run.ID, func(r Run) bool { return r.Terminal() })
	c = v.Catalog
	c.Config.Units[0].Health = nil
	if _, err = e.ConfigureServices(c, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, _ := s.Task(run.TaskID)
		if task.Status == "failed" {
			if len(s.List()) != 1 {
				t.Fatal("stale health restart created another attempt")
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("changed health policy retained retry plan")
}
