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

func conditionEngine(t *testing.T, c service.Condition, command string) (*Engine, *Store, ServiceView) {
	t.Helper()
	root := t.TempDir()
	app := executionApp(t, command)
	e, s := testEngine(t, root, []Application{app})
	catalog := serviceCatalog(root)
	catalog.Config.Units[0].Condition = &c
	catalog.Config.Units[0].RestartPolicy.OnFailure = "retry"
	b := catalog.Work["inventory"]
	b.AppID = app.ID
	catalog.Work["inventory"] = b
	v, err := e.ConfigureServices(catalog, 0)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func TestServiceConditionSkipsWithoutApplicationOrAcceptance(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "must-not-run")
	e, s, _ := conditionEngine(t, service.Condition{FileExists: "missing"}, fmt.Sprintf("printf wrong > '%s'", marker))
	e.Apps[0].Acceptance.Cmd = []string{"/bin/sh", "-c", fmt.Sprintf("printf wrong > '%s'", marker)}
	run := replacementRun(t, e, "one")
	run = waitHealth(t, s, run.ID, func(r Run) bool { return r.Terminal() })
	if run.Status != "cancelled" || run.Condition == nil || run.Condition.Status != "skipped" || run.Verified || len(run.Artifacts) != 0 {
		t.Fatal("condition did not withhold execution", run.Status, run.Condition)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("application or acceptance executed")
	}
	view, _ := s.Services()
	if len(view.States[0].Retries) != 0 || view.States[0].Condition.Status != "skipped" {
		t.Fatal("skip became failure retry")
	}
	task, _ := s.Task(run.TaskID)
	if task.Status != "cancelled" {
		t.Fatal(task.Status)
	}
	for _, event := range run.Events {
		if event.Kind == "claims_acquired" || event.Kind == "task_scheduled" {
			var c change
			_ = json.Unmarshal(event.Data, &c)
			if c.ServiceCondition == nil || c.ServiceCondition.Status != "bound" {
				t.Fatal("condition was not bound atomically at admission")
			}
		}
	}
	live, err := ReportAutonomy(s.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(s.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("condition evidence readers disagree", err)
	}
}
func TestServiceConditionEnvironmentAndIgnoredSourceFiles(t *testing.T) {
	t.Setenv("MITOS_TEST_CONDITION_SECRET", "private-test-value")
	e, s, _ := conditionEngine(t, service.Condition{FileExists: ".control/ready-*", FileNotExists: ".control/pause", EnvSet: "MITOS_TEST_CONDITION_SECRET", EnvEquals: map[string]string{"MITOS_TEST_MISSING": ""}, EnvNotEquals: map[string]string{"MITOS_TEST_CONDITION_SECRET": "excluded"}}, "printf result > result.txt")
	t.Setenv("MITOS_TEST_MISSING", "")
	if err := os.MkdirAll(filepath.Join(e.Root, ".control"), 0700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{".gitignore": ".control/\n", ".control/ready-1": "ready"} {
		if err := os.WriteFile(filepath.Join(e.Root, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	run := replacementRun(t, e, "one")
	run = waitHealth(t, s, run.ID, func(r Run) bool { return r.Terminal() })
	if run.Status != "done" || run.Condition.Status != "passed" || len(run.Condition.Facts) != 5 {
		t.Fatal("conjunctive conditions did not pass", run.Status, run.Condition)
	}
	b, _ := json.Marshal(run)
	if strings.Contains(string(b), "private-test-value") {
		t.Fatal("actual environment value entered evidence")
	}
}
func TestServiceConditionEnvironmentSemantics(t *testing.T) {
	cases := []struct {
		c      service.Condition
		values map[string]string
		pass   bool
	}{
		{service.Condition{EnvSet: "TOKEN"}, map[string]string{"TOKEN": ""}, false},
		{service.Condition{EnvEquals: map[string]string{"MISSING": ""}}, nil, true},
		{service.Condition{EnvNotEquals: map[string]string{"MISSING": ""}}, nil, false},
		{service.Condition{EnvEquals: map[string]string{"A": "yes", "B": "yes"}}, map[string]string{"A": "yes", "B": "no"}, false},
	}
	for _, test := range cases {
		h := &ServiceConditionState{Policy: test.c}
		if got := conditionEnvironmentFacts(h, test.values); got != test.pass {
			t.Fatal("wrong environment conjunction", test.c)
		}
	}
}
func TestServiceConditionCommandIsolatedAndBounded(t *testing.T) {
	for _, command := range []string{"printf private-probe > probe-only; test -s input", "sleep 60"} {
		t.Run(command, func(t *testing.T) {
			e, s, _ := conditionEngine(t, service.Condition{Command: command}, "test ! -f probe-only; printf result > result.txt")
			if err := os.WriteFile(filepath.Join(e.Root, "input"), []byte("input"), 0600); err != nil {
				t.Fatal(err)
			}
			run := replacementRun(t, e, "one")
			run = waitHealth(t, s, run.ID, func(r Run) bool { return r.Terminal() })
			expected := "done"
			if command == "sleep 60" {
				expected = "cancelled"
			}
			if run.Status != expected || run.Condition.OutputSHA256 == "" {
				t.Fatal("command condition outcome", run.Status, run.Condition)
			}
			if _, err := os.Stat(filepath.Join(e.Root, "probe-only")); !os.IsNotExist(err) {
				t.Fatal("probe modified source")
			}
			if expected == "done" {
				changes := readChanges(t, s, run)
				if len(changes.Changes) != 1 || changes.Changes[0].Path != "result.txt" {
					t.Fatal("probe mutations leaked into worker result")
				}
			}
		})
	}
}
func TestServiceConditionGlobConfinementAndBounds(t *testing.T) {
	source := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "ready"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "outside")); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(source)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if found, err := conditionGlob(context.Background(), root, "outside/ready"); err == nil || found {
		t.Fatal("escaping glob followed link")
	}
	for i := 0; i < 10001; i++ {
		if err := os.WriteFile(filepath.Join(source, fmt.Sprintf("entry-%05d", i)), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if found, err := conditionGlob(context.Background(), root, "no-match"); err == nil || found {
		t.Fatal("unbounded directory scan")
	}
}
func TestServiceConditionReplayClosesPendingCheck(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	marker := filepath.Join(t.TempDir(), "must-not-run")
	app := executionApp(t, "printf result > result.txt")
	e := &Engine{Store: s, Root: root, Apps: []Application{app}, provider: &compute.Local{}}
	c := serviceCatalog(root)
	c.Config.Units[0].Condition = &service.Condition{Command: fmt.Sprintf("printf wrong > '%s'", marker)}
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
	if _, err := s.beginServiceCondition(run.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
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
	if current.Status != "parked" || current.Condition.Status != "interrupted" {
		t.Fatal("pending condition did not park", current.Status, current.Condition)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("replay executed condition command")
	}
}
func TestServiceConditionBindingAndBuiltin(t *testing.T) {
	apps, _ := LoadApps("")
	e, s := testEngine(t, t.TempDir(), apps)
	e.Close()
	c := serviceCatalog(e.Root)
	c.Config.Units[0].Condition = &service.Condition{EnvSet: "MITOS_CONDITION_PRESENT"}
	v, err := e.ConfigureServices(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	run := replacementRun(t, e, "one")
	v.Catalog.Config.Units[0].Condition = &service.Condition{EnvSet: "MITOS_CONDITION_ABSENT"}
	v, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(run.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	bound, _ := s.Get(run.ID)
	if bound.Condition.Policy.EnvSet != "MITOS_CONDITION_ABSENT" {
		t.Fatal("queued receipt bound stale condition")
	}
	v.Catalog.Config.Units[0].Condition = nil
	if _, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MITOS_CONDITION_ABSENT", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e.execute(ctx, bound)
	run, _ = s.Get(run.ID)
	if run.Status != "cancelled" || run.Condition.Status != "skipped" {
		t.Fatal("admitted binding changed or builtin bypassed gate", run.Status, run.Condition)
	}
}

func TestServiceConditionsAutomaticTriggers(t *testing.T) {
	t.Setenv("MITOS_CONDITION_DISABLED", "")
	apps, _ := LoadApps("")
	e, s := testEngine(t, t.TempDir(), apps)
	c := serviceCatalog(e.Root)
	c.Config.Targets = nil
	c.Config.Units = nil
	c.Config.Intent = service.Intent{}
	c.Work = map[string]Brief{}
	delay := 3600
	schedules := map[string]*service.Schedule{
		"manual":     nil,
		"interval":   {Type: "interval", Interval: 3600, RunOnStartup: true},
		"cron":       {Type: "cron", Cron: "0 0 * * *", Timezone: "UTC", RunOnStartup: true},
		"event":      {Type: "event", Trigger: service.EventPatterns{"service.succeeded:source"}},
		"continuous": {Type: "continuous", RestartDelay: &delay},
	}
	for id, schedule := range schedules {
		c.Config.Units = append(c.Config.Units, service.Unit{ID: id, Scope: "project", Trigger: id, Schedule: schedule, Condition: &service.Condition{EnvSet: "MITOS_CONDITION_DISABLED"}})
		c.Config.Intent.Enable = append(c.Config.Intent.Enable, id)
		c.Work[id] = testBrief(e.Root)
	}
	c.Config.Units = append(c.Config.Units, service.Unit{ID: "source", Scope: "project", Trigger: "manual"})
	c.Config.Intent.Enable = append(c.Config.Intent.Enable, "source")
	c.Work["source"] = testBrief(e.Root)
	if _, err := e.ConfigureServices(c, 0); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"manual", "source"} {
		if _, err := e.TriggerService(id, "one"); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		complete := map[string]bool{}
		for _, run := range s.List() {
			if run.Condition != nil && run.Condition.Status == "skipped" && run.Status == "cancelled" {
				complete[run.Condition.ServiceID] = true
			}
		}
		if len(complete) == 5 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, run := range s.List() {
		t.Log(run.AppName, run.Status, run.Condition)
	}
	t.Fatal("a registered trigger bypassed or did not reach the condition gate")
}
