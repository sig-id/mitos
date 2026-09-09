package studio

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"mitos/internal/service"
)

func ingressServices(t *testing.T) (*Engine, *Store, ServiceView) {
	t.Helper()
	apps, _ := LoadApps("")
	e, s := testEngine(t, t.TempDir(), apps)
	e.Close()
	c := serviceCatalog(e.Root)
	c.Config.Targets = nil
	c.Config.Intent = service.Intent{Enable: []string{"hook", "signal"}}
	c.Config.Units = []service.Unit{{ID: "hook", Scope: "project", Trigger: "webhook", Schedule: &service.Schedule{Type: "webhook", Path: "/hooks/jobs"}}, {ID: "signal", Scope: "project", Trigger: "external_signal", Schedule: &service.Schedule{Type: "external_signal", Source: "queue.jobs"}}}
	c.Work = map[string]Brief{"hook": testBrief(e.Root), "signal": testBrief(e.Root)}
	v, err := e.ConfigureServices(c, 0)
	if err != nil {
		t.Fatal(err)
	}
	return e, s, v
}
func TestIngressPayloadNormalization(t *testing.T) {
	a, err := NormalizeIngressPayload([]byte(` { "z": 9007199254740993, "a": [true,null,"text"] } `))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != `{"a":[true,null,"text"],"z":9007199254740993}` {
		t.Fatal("numeric precision or ordering changed", string(a))
	}
	for _, b := range [][]byte{[]byte(`{"a":1,"a":2}`), []byte(`{"nested":{"a":1,"a":1}}`), []byte(`{} {}`), []byte(``), []byte{0xff}, []byte(strings.Repeat("[", 40) + "0" + strings.Repeat("]", 40)), bytes.Repeat([]byte(" "), MaxIngressBytes+1)} {
		if _, err := NormalizeIngressPayload(b); err == nil {
			t.Fatal("invalid payload accepted")
		}
	}
}
func TestServiceIngressAtomicConcurrentIdempotency(t *testing.T) {
	e, s, _ := ingressServices(t)
	var wg sync.WaitGroup
	ids := make(chan string, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.DeliverWebhook("/hooks/jobs", "delivery-1", "", []byte(`{"b":2,"a":1}`))
			ids <- r.ID
			errs <- err
		}()
	}
	wg.Wait()
	close(ids)
	close(errs)
	first := ""
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for id := range ids {
		if first == "" {
			first = id
		}
		if first != id {
			t.Fatal("duplicate delivery created another run")
		}
	}
	if len(s.Tasks()) != 1 {
		t.Fatal("duplicate Task")
	}
	same, err := e.DeliverWebhook("/hooks/jobs", "delivery-1", "", []byte(" { \"a\": 1, \"b\":2 } "))
	if err != nil || same.ID != first {
		t.Fatal("canonical duplicate not recognized", err)
	}
	if _, err = e.DeliverWebhook("/hooks/jobs", "delivery-1", "", []byte(`{"a":2}`)); err == nil {
		t.Fatal("conflicting delivery overwrote receipt")
	}
	run, _ := s.Get(first)
	var c change
	if err := json.Unmarshal(run.Events[0].Data, &c); err != nil {
		t.Fatal(err)
	}
	if c.ServiceCall.Ingress == nil || c.Task.ServiceIngress == nil || !reflect.DeepEqual(c.ServiceCall.Ingress, c.Task.ServiceIngress) || c.Task.Source != "trigger:hook" {
		t.Fatal("ingress not atomic with fixed work binding")
	}
	if c.Task.Brief.AppID != run.Brief.AppID || c.Task.ServiceIngress.Bytes != 13 {
		t.Fatal("payload replaced work or size is wrong")
	}
}
func TestServiceIngressRefusalAndReplayedReceipt(t *testing.T) {
	e, s, v := ingressServices(t)
	if _, err := e.DeliverSignal("signal", "wrong", "id", "", []byte(`null`)); err == nil {
		t.Fatal("wrong source admitted")
	}
	first, err := e.DeliverSignal("signal", "queue.jobs", "one", "", []byte(`{"command":"untrusted payload"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.DeliverSignal("signal", "queue.jobs", "two", "", []byte(`null`)); err == nil {
		t.Fatal("capacity bypassed")
	}
	if err = e.Cancel(first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := e.DeliverSignal("signal", "queue.jobs", "two", "", []byte(`null`))
	if err != nil {
		t.Fatal("refusal consumed delivery key", err)
	}
	hook, err := e.DeliverWebhook("/hooks/jobs", "hook-one", "", []byte(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	v.Catalog.Config.Intent = service.Intent{}
	v.Catalog.Config.Units = nil
	v.Catalog.Work = map[string]Brief{}
	if _, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	again, err := e.DeliverWebhook("/hooks/jobs", "hook-one", "", []byte(`{"x":1}`))
	if err != nil || again.ID != hook.ID {
		t.Fatal("removed route lost receipt", err)
	}
	if _, err = e.DeliverWebhook("/hooks/jobs", "new", "", []byte(`{"x":1}`)); err == nil {
		t.Fatal("removed route admitted new delivery")
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
	again, err = e.DeliverSignal("signal", "queue.jobs", "two", "", []byte(`null`))
	if err != nil || again.ID != second.ID {
		t.Fatal("signal replay lost original receipt", err)
	}
	again, err = e.DeliverWebhook("/hooks/jobs", "hook-one", "", []byte(`{"x":1}`))
	if err != nil || again.ID != hook.ID {
		t.Fatal("webhook replay lost original receipt", err)
	}
}
func TestServiceIngressCyclesAndRetryProvenance(t *testing.T) {
	e, s, v := ingressServices(t)
	v.Catalog.Config.Units[1].DependsOn = []string{"hook"}
	if _, err := e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := e.DeliverSignal("signal", "queue.jobs", "down", "", []byte(`{}`)); err == nil {
		t.Fatal("dependent delivery lacks cycle")
	}
	down, err := e.DeliverSignal("signal", "queue.jobs", "down", "batch", []byte(`{"request":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.Schedule(down.ID, nil); err != nil || ok {
		t.Fatal("ingress bypassed dependency", err)
	}
	up, err := e.DeliverWebhook("/hooks/jobs", "up", "batch", []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	finishCycleTask(t, s, up)
	finishCycleTask(t, s, down)
	before, _ := s.Task(down.TaskID)
	retry, err := e.Retry(down.ID)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := s.Task(retry.TaskID)
	if !reflect.DeepEqual(before.ServiceIngress, after.ServiceIngress) || !reflect.DeepEqual(before.ServiceCycle, after.ServiceCycle) {
		t.Fatal("retry lost delivery payload or cycle")
	}
	live, err := ReportAutonomy(s.List())
	if err != nil {
		t.Fatal(err)
	}
	offline, err := AuditAutonomyLogs(s.Dir())
	if err != nil || !reflect.DeepEqual(live, offline) {
		t.Fatal("ingress metrics disagree", err)
	}
}
func TestServiceIngressDuplicateActiveRoutesRejected(t *testing.T) {
	e, _, v := ingressServices(t)
	v.Catalog.Config.Units[1].Trigger = "webhook"
	v.Catalog.Config.Units[1].Schedule = &service.Schedule{Type: "webhook", Path: "/hooks/jobs"}
	if _, err := e.PreviewServices(v.Catalog); err == nil {
		t.Fatal("ambiguous active routes accepted")
	}
}
