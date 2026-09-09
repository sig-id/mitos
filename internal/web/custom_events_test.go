package web

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"mitos/internal/acceptance"
	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestCustomEventHTTPWorkerAndEvidence(t *testing.T) {
	s, e := testServer(t)
	e.Apps = append(e.Apps, studio.Application{ID: "event-worker", Name: "Event worker", Directory: t.TempDir(), Entrypoint: "Run", Command: []string{"/bin/sh", "-c", "cat; printf result > result.txt"}, Acceptance: acceptance.Spec{Cmd: []string{"/bin/sh", "-c", "test -s \"$MITOS_WORKSPACE/result.txt\""}}})
	b := studio.Brief{Title: "Custom event work", Workspace: e.Root, AppID: "event-worker", TimeoutMinutes: 1}
	c := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "consumer", Scope: "project", Trigger: "event", Schedule: &service.Schedule{Type: "event", Trigger: service.EventPatterns{"inventory.*"}}}}, Intent: service.Intent{Enable: []string{"consumer"}}}, Work: map[string]studio.Brief{"consumer": b}}
	if _, err := e.ConfigureServices(c, 0); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"inventory.ready","idempotency_key":"one","payload":{"n":9007199254740993,"text":"<script>alert(1)</script>"}}`
	response := deliveryRequest(s, "/api/service-events", "", body)
	var root studio.CustomEventView
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &root) != nil || root.Pending != 1 {
		t.Fatal(response.Code, response.Body.String())
	}
	var task studio.Task
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		for _, candidate := range e.Store.Tasks() {
			if candidate.Source == "trigger:consumer" {
				task = candidate
			}
		}
		if task.Status == "done" || task.Status == "failed" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if task.Status != "done" {
		t.Fatal("event worker did not complete", task.Status)
	}
	output, err := e.Store.Artifact(task.CurrentRunID, "output.txt")
	if err != nil {
		t.Fatal(err)
	}
	var stdin struct {
		Event *studio.ServiceEventCause `json:"service_event"`
	}
	if json.Unmarshal(output, &stdin) != nil || !reflect.DeepEqual(stdin.Event, task.ServiceEvent) || stdin.Event.Custom == nil || stdin.Event.Seq != root.Seq || !strings.Contains(string(stdin.Event.Custom.Payload), "9007199254740993") {
		t.Fatal("worker did not receive exact custom event")
	}
	duplicate := deliveryRequest(s, "/api/service-events", "", body)
	var same studio.CustomEventView
	if duplicate.Code != 200 || json.Unmarshal(duplicate.Body.Bytes(), &same) != nil || same.Seq != root.Seq || same.Enqueued != 1 {
		t.Fatal("duplicate changed source or lost progress")
	}
	for route, needle := range map[string]string{"/services": "Source event", fmt.Sprintf("/services/events/%d", root.Seq): "Task run", "/runs/" + task.CurrentRunID: "Custom event source", fmt.Sprintf("/api/service-events/%d", root.Seq): "9007199254740993", "/api/service-events": "inventory.ready"} {
		page := request(s, "GET", route, "", false)
		if page.Code != 200 || !strings.Contains(page.Body.String(), needle) || strings.Contains(page.Body.String(), "<script>alert(1)</script>") {
			t.Fatal("missing or unsafe event surface", route, page.Code, page.Body.String())
		}
	}
}
func TestCustomEventHTTPValidationLimitsAndForm(t *testing.T) {
	s, e := testServer(t)
	e.Close()
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"name":"custom","idempotency_key":"ok","payload":null}`, 200},
		{`{"name":"custom","idempotency_key":"ok","payload":false}`, 409},
		{`{"name":"service.succeeded:fake","idempotency_key":"bad","payload":{}}`, 400},
		{`{"name":"custom","idempotency_key":"bad","payload":{},"extra":1}`, 400},
		{`{"name":"custom","idempotency_key":"bad","payload":{"a":1,"a":2}}`, 400},
		{`{"name":"custom","idempotency_key":"bad"}`, 400},
		{`{"name":"custom","idempotency_key":"large","payload":"` + strings.Repeat("x", 40<<10) + `"}`, 200},
		{strings.Repeat(" ", studio.MaxIngressBytes+1), 413},
	} {
		response := deliveryRequest(s, "/api/service-events", "", tc.body)
		if response.Code != tc.code {
			t.Fatal(response.Code, tc.code, response.Body.String())
		}
	}
	if response := request(s, "GET", "/api/service-events/0", "", false); response.Code != 404 {
		t.Fatal("invalid sequence accepted")
	}
	r := httptest.NewRequest("POST", "http://localhost/api/service-events", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "text/plain")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 415 {
		t.Fatal("wrong media accepted")
	}
	r = httptest.NewRequest("POST", "http://localhost/api/service-events", strings.NewReader(`{"name":"cross","idempotency_key":"one","payload":{}}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Origin", "https://other.example")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin intake allowed")
	}
	r = httptest.NewRequest("POST", "http://localhost/services/events", strings.NewReader("name=form.event&idempotency_key=form&payload=%7B%7D"))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 303 || !strings.HasPrefix(w.Header().Get("Location"), "/services/events/") {
		t.Fatal("form did not persist and redirect", w.Code, w.Body.String())
	}
}
