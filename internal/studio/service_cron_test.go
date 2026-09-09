package studio

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"mitos/internal/service"
)

func cronEngine(t *testing.T) (*Engine, *Store, ServiceView) {
	t.Helper()
	s := testStore(t)
	apps, _ := LoadApps("")
	e := &Engine{Store: s, Apps: apps, Root: t.TempDir()}
	c := serviceCatalog(e.Root)
	c.Config.Units[0].Trigger = "cron"
	c.Config.Units[0].Schedule = &service.Schedule{Type: "cron", Cron: "*/15 * * * *", Timezone: "Europe/Zurich"}
	c.Config.Units[0].Concurrency = service.Concurrency{IfRunning: "queue", QueueMax: 1}
	preview, err := e.PreviewServices(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.ClockPreview) != 1 || preview.ClockPreview[0].NextAt.IsZero() || s.seq != 0 {
		t.Fatal("clock preview missing or mutated state")
	}
	v, err := e.ConfigureServices(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func TestCronDurableClockOccurrenceAndCalendarCatchup(t *testing.T) {
	e, s, v := cronEngine(t)
	due := *v.States[0].NextAt
	if due.Second() != 0 || due.Minute()%15 != 0 {
		t.Fatal("clock not on calendar boundary")
	}
	if err := e.scheduleServices(due.Add(-time.Nanosecond), false); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Fatal("cron fired early")
	}
	if err := e.scheduleServices(due, false); err != nil {
		t.Fatal(err)
	}
	first := s.List()[0]
	var creation change
	_ = json.Unmarshal(first.Events[0].Data, &creation)
	if creation.ServiceCall.Origin != "cron" || !creation.ServiceCall.Tick.DueAt.Equal(due) {
		t.Fatal("cron origin/occurrence not saved")
	}
	if err := e.scheduleServices(due, false); err != nil {
		t.Fatal(err)
	}
	before, _ := s.Services()
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
		t.Fatal("clock changed on restart")
	}
	if err = e.scheduleServices(due, false); err != nil {
		t.Fatal(err)
	}
	if len(reopened.List()) != 1 {
		t.Fatal("committed cron occurrence duplicated after replay")
	}
	late := due.Add(5*time.Hour + 7*time.Minute + 12*time.Second)
	if err = e.scheduleServices(late, false); err != nil {
		t.Fatal(err)
	}
	after, _ := reopened.Services()
	if len(reopened.List()) != 2 || !after.States[0].NextAt.Equal(due.Add(5*time.Hour+15*time.Minute)) {
		t.Fatal("cron catchup drifted or replayed backlog")
	}
	if err = e.scheduleServices(*after.States[0].NextAt, false); err != nil {
		t.Fatal(err)
	}
	after, _ = reopened.Services()
	if after.States[0].Skipped != 1 || len(reopened.List()) != 2 {
		t.Fatal("cron bypassed bounded queue")
	}
	live, err := ReportAutonomy(reopened.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(reopened.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("cron broke metric audit", err)
	}
}
func TestCronReplayUsesSavedInitialClockAndPreservesUnchangedSchedule(t *testing.T) {
	e, s, v := cronEngine(t)
	// Model an initial instant computed by an earlier timezone database. The
	// recorded instant, rather than today's recomputation, is replay authority.
	chosen := v.States[0].NextAt.Add(time.Hour)
	v.Catalog.Revision = s.seq + 1
	s.mu.Lock()
	err := s.commit("", "effect", "services_configured", change{Services: &v.Catalog, ServiceClocks: []ServiceClock{{ServiceID: "inventory", Epoch: v.Catalog.Revision, NextAt: chosen}}})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	saved, _ := s.Services()
	saved.Catalog.Config.Units[0].Description = "Unrelated edit"
	same, err := e.ConfigureServices(saved.Catalog, saved.Catalog.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !same.States[0].NextAt.Equal(chosen) || same.States[0].Epoch != saved.States[0].Epoch {
		t.Fatal("unchanged schedule recalculated clock")
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenStore(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	view, _ := replay.Services()
	if !reflect.DeepEqual(same, view) {
		t.Fatal("replay recomputed saved clock")
	}
}
func TestCronStartupAndUnsatisfiablePreview(t *testing.T) {
	e, s, v := cronEngine(t)
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
	if !c.ServiceCall.Tick.Startup || c.ServiceCall.Origin != "cron" {
		t.Fatal("cron startup not bound")
	}
	current, _ := s.Services()
	if current.States[0].NextAt.Second() != 0 || current.States[0].NextAt.Minute()%15 != 0 {
		t.Fatal("startup shifted subsequent cron occurrences")
	}
	v.Catalog.Config.Units[0].Schedule = &service.Schedule{Type: "cron", Cron: "30 2 31 3 0", Timezone: "Europe/Zurich"}
	seq := s.seq
	if _, err = e.PreviewServices(v.Catalog); err == nil {
		t.Fatal("impossible cron preview accepted")
	}
	if _, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision); err == nil {
		t.Fatal("impossible cron configured")
	}
	if s.seq != seq {
		t.Fatal("rejected schedule wrote state")
	}
}

func TestCronFoldOccurrencesHaveDistinctDurableKeys(t *testing.T) {
	e, s, v := cronEngine(t)
	v.Catalog.Config.Units[0].Schedule = &service.Schedule{Type: "cron", Cron: "30 2 * * *", Timezone: "Europe/Zurich"}
	first, err := time.Parse(time.RFC3339, "2026-10-25T00:30:00Z")
	if err != nil {
		t.Fatal(err)
	}
	v.Catalog.Revision = s.seq + 1
	s.mu.Lock()
	err = s.commit("", "effect", "services_configured", change{Services: &v.Catalog, ServiceClocks: []ServiceClock{{ServiceID: "inventory", Epoch: v.Catalog.Revision, NextAt: first}}})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.scheduleServices(first, false); err != nil {
		t.Fatal(err)
	}
	view, _ := s.Services()
	second := first.Add(time.Hour)
	if !view.States[0].NextAt.Equal(second) {
		t.Fatal("second occurrence of repeated local minute was lost")
	}
	if err = e.scheduleServices(second, false); err != nil {
		t.Fatal(err)
	}
	runs := s.List()
	if len(runs) != 2 {
		t.Fatal("fold did not create both occurrences")
	}
	var a, b change
	_ = json.Unmarshal(runs[0].Events[0].Data, &a)
	_ = json.Unmarshal(runs[1].Events[0].Data, &b)
	if a.ServiceCall.Key == b.ServiceCall.Key || a.ServiceCall.Tick.DueAt.Equal(b.ServiceCall.Tick.DueAt) {
		t.Fatal("fold occurrences shared a deduplication key")
	}
	if err = e.scheduleServices(second, false); err != nil {
		t.Fatal(err)
	}
	if len(s.List()) != 2 {
		t.Fatal("fold replay duplicated occurrence")
	}
}
func TestCronUnavailableFutureCalendarPausesWithEvidence(t *testing.T) {
	e, s, v := cronEngine(t)
	// Simulate a saved catalog from a timezone database where this wall time
	// existed, followed by a database update making all future matches gaps.
	v.Catalog.Config.Units[0].Schedule = &service.Schedule{Type: "cron", Cron: "30 2 31 3 0", Timezone: "Europe/Zurich"}
	due := time.Now().UTC()
	v.Catalog.Revision = s.seq + 1
	s.mu.Lock()
	err := s.commit("", "effect", "services_configured", change{Services: &v.Catalog, ServiceClocks: []ServiceClock{{ServiceID: "inventory", Epoch: v.Catalog.Revision, NextAt: due}}})
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.scheduleServices(due, false); err != nil {
		t.Fatal(err)
	}
	view, _ := s.Services()
	if view.States[0].NextAt != nil || view.States[0].Rejected != 1 || view.States[0].LastTick.Detail == "" || len(s.List()) != 0 {
		t.Fatal("unschedulable clock did not pause with evidence")
	}
	seq := s.seq
	if err = e.scheduleServices(due.Add(time.Hour), false); err != nil {
		t.Fatal(err)
	}
	if s.seq != seq {
		t.Fatal("paused schedule rewrote its error each tick")
	}
	if _, err = AuditAutonomyLogs(s.Dir()); err != nil {
		t.Fatal("paused clock broke audit", err)
	}
}
