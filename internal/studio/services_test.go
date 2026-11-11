package studio

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"

	"mitos/internal/service"
)

func serviceCatalog(root string) ServiceCatalog {
	return ServiceCatalog{Config: service.Config{
		Units:   []service.Unit{{ID: "inventory", Scope: "project", Trigger: "manual"}},
		Targets: []service.Target{{ID: "inspect.target", Scope: "project", Relations: service.Relations{Requires: []string{"inventory"}}}},
		Intent:  service.Intent{Targets: []string{"inspect.target"}},
	}, Work: map[string]Brief{"inventory": testBrief(root)}}
}
func TestServiceCatalogPreviewRevisionAndAtomicTriggerReplay(t *testing.T) {
	apps, _ := LoadApps("")
	e, s := testEngine(t, t.TempDir(), apps)
	e.Close() // Keep queued receipts stable while testing concurrent trigger intake.
	initialSeq := s.seq
	c := serviceCatalog(e.Root)
	if _, err := e.PreviewServices(c); err != nil {
		t.Fatal(err)
	}
	if s.seq != initialSeq || len(s.Tasks()) != 0 {
		t.Fatal("preview wrote state")
	}
	view, err := e.ConfigureServices(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	revision := view.Catalog.Revision
	if revision != initialSeq+1 {
		t.Fatal("catalog revision is not its event sequence")
	}
	c.Work["inventory"] = Brief{}
	if _, err = e.ConfigureServices(view.Catalog, 0); !errors.Is(err, ErrConflict) {
		t.Fatal("stale save accepted")
	}
	same, err := e.ConfigureServices(view.Catalog, revision)
	if err != nil || same.Catalog.Revision != revision || s.seq != revision {
		t.Fatal("unchanged save wrote event", err)
	}
	var wg sync.WaitGroup
	runs := make(chan Run, 16)
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); r, err := e.TriggerService("inventory", "delivery-1"); runs <- r; errs <- err }()
	}
	wg.Wait()
	close(runs)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var first Run
	for r := range runs {
		if first.ID == "" {
			first = r
		}
		if r.ID != first.ID {
			t.Fatal("duplicate created another receipt")
		}
	}
	if len(s.Tasks()) != 1 || s.seq != revision+1 {
		t.Fatal("trigger was not atomic")
	}
	task, _ := s.Task(first.TaskID)
	if task.Source != "trigger:inventory" || len(task.Runs) != 0 {
		t.Fatal("service bypassed task admission")
	}
	var creation change
	if err = json.Unmarshal(first.Events[0].Data, &creation); err != nil {
		t.Fatal(err)
	}
	if creation.ServiceCall == nil || creation.ServiceCall.Revision != revision || creation.Task.Source != task.Source {
		t.Fatal("call binding not saved with task")
	}
	view.Catalog.Config.Intent.Disable = []string{"inventory"}
	view, err = e.ConfigureServices(view.Catalog, revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.TriggerService("inventory", "delivery-2"); err == nil {
		t.Fatal("disabled service triggered")
	}
	duplicate, err := e.TriggerService("inventory", "delivery-1")
	if err != nil || duplicate.ID != first.ID {
		t.Fatal("disabled service lost original receipt")
	}
	if _, err = ReportAutonomy(s.List()); err != nil {
		t.Fatal(err)
	}
	if _, err = AuditAutonomyLogs(s.Dir()); err != nil {
		t.Fatal("catalog broke offline audit", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replay, err := reopened.Services()
	if err != nil || !reflect.DeepEqual(view, replay) {
		t.Fatal("catalog/reasons changed after replay", err)
	}
	// The persisted call index must work without a dispatcher or installed app lookup.
	reader := &Engine{Store: reopened}
	duplicate, err = reader.TriggerService("inventory", "delivery-1")
	if err != nil || duplicate.ID != first.ID {
		t.Fatal("trigger key lost on restart", err)
	}
	replayTask, _ := reopened.Task(first.TaskID)
	if !reflect.DeepEqual(task, replayTask) {
		t.Fatal("task source changed on replay")
	}
}
func TestServicesRejectInvalidBindingsWithoutEvents(t *testing.T) {
	apps, _ := LoadApps("")
	e, s := testEngine(t, t.TempDir(), apps)
	e.Close()
	cases := []func(*ServiceCatalog){
		func(c *ServiceCatalog) { delete(c.Work, "inventory") },
		func(c *ServiceCatalog) { b := c.Work["inventory"]; b.AppID = "missing"; c.Work["inventory"] = b },
		func(c *ServiceCatalog) {
			b := c.Work["inventory"]
			b.Scheduling.Pool = "missing"
			c.Work["inventory"] = b
		},
		func(c *ServiceCatalog) { c.Config.Units[0].Trigger = "interval" },
		func(c *ServiceCatalog) { c.Config.Units[0].ConflictsWith = []string{"inventory"} },
	}
	initialSeq := s.seq
	for _, mutate := range cases {
		c := serviceCatalog(e.Root)
		mutate(&c)
		if _, err := e.ConfigureServices(c, 0); err == nil {
			t.Fatal("invalid catalog accepted")
		}
		if s.seq != initialSeq {
			t.Fatal("refused catalog wrote event")
		}
	}
	if _, err := e.ConfigureServices(serviceCatalog(e.Root), 0); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", " leading", "a\nb", "a\x00b"} {
		if _, err := e.TriggerService("inventory", key); err == nil {
			t.Fatal("invalid trigger key accepted")
		}
	}
	if s.seq != initialSeq+1 {
		t.Fatal("refused call wrote event")
	}
}
