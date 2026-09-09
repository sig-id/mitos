package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mitos/internal/acceptance"
	"mitos/internal/service"
	"mitos/internal/studio"
)

func deliveryRequest(s *Server, path, key, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "http://localhost"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestServiceIngressHTTPPayloadAndReplay(t *testing.T) {
	s, e := testServer(t)
	e.Apps = append(e.Apps, studio.Application{ID: "ingress-worker", Name: "Ingress worker", Directory: t.TempDir(), Entrypoint: "Run", Command: []string{"/bin/sh", "-c", "cat; printf result > result.txt"}, Acceptance: acceptance.Spec{Cmd: []string{"/bin/sh", "-c", "test -s \"$MITOS_WORKSPACE/result.txt\""}}})
	b := studio.Brief{Title: "Delivered work", Workspace: e.Root, AppID: "ingress-worker", TimeoutMinutes: 1}
	c := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "hook", Scope: "project", Trigger: "webhook", Schedule: &service.Schedule{Type: "webhook", Path: "/capabilities/test/runs"}}, {ID: "signal", Scope: "project", Trigger: "external_signal", Schedule: &service.Schedule{Type: "external_signal", Source: "queue.jobs"}}}, Intent: service.Intent{Enable: []string{"hook", "signal"}}}, Work: map[string]studio.Brief{"hook": b, "signal": b}}
	body, _ := json.Marshal(map[string]any{"expected_revision": 0, "catalog": c})
	if response := request(s, "PUT", "/api/services", string(body), false); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	first := deliveryRequest(s, "/capabilities/test/runs", "delivery-1", `{"value":9007199254740993}`)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body.String())
	}
	var run studio.Run
	_ = json.Unmarshal(first.Body.Bytes(), &run)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		run, _ = e.Store.Get(run.ID)
		if run.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.Status != "done" {
		t.Fatal(run.Status)
	}
	output, err := e.Store.Artifact(run.ID, "output.txt")
	if err != nil {
		t.Fatal(err)
	}
	var input struct {
		Ingress studio.ServiceIngress `json:"service_ingress"`
	}
	if err = json.Unmarshal(output, &input); err != nil {
		t.Fatal(err)
	}
	if input.Ingress.Source != "/capabilities/test/runs" || string(input.Ingress.Payload) != `{"value":9007199254740993}` {
		t.Fatal("worker payload changed", input.Ingress)
	}
	duplicate := deliveryRequest(s, "/capabilities/test/runs", "delivery-1", `{ "value":9007199254740993 }`)
	var same studio.Run
	_ = json.Unmarshal(duplicate.Body.Bytes(), &same)
	if duplicate.Code != 200 || same.ID != run.ID {
		t.Fatal("HTTP duplicate created new work")
	}
	if response := deliveryRequest(s, "/capabilities/test/runs", "delivery-1", `{"value":1}`); response.Code != 409 {
		t.Fatal("changed delivery accepted")
	}
	if response := deliveryRequest(s, "/capabilities/test/runs", "large-valid", `"`+strings.Repeat("x", 40<<10)+`"`); response.Code != 200 {
		t.Fatal("valid delivery hit the ordinary form limit", response.Code, response.Body.String())
	}
	signal := deliveryRequest(s, "/api/services/signal/signals", "", `{"source":"queue.jobs","delivery_id":"message-1","payload":{"topic":"jobs"}}`)
	if signal.Code != 200 {
		t.Fatal(signal.Body.String())
	}
	for route, needle := range map[string]string{"/runs/" + run.ID: "Service delivery", "/services": "Delivery source: /capabilities/test/runs"} {
		page := request(s, "GET", route, "", false)
		if page.Code != 200 || !strings.Contains(page.Body.String(), needle) {
			t.Fatal(route, page.Body.String())
		}
	}
	for _, test := range []struct {
		path, key, body string
		code            int
	}{
		{"/capabilities/test/runs", "bad", `{"a":1,"a":2}`, 400},
		{"/capabilities/test/runs", "", `{}`, 400},
		{"/capabilities/test/runs?x=1", "query", `{}`, 400},
		{"/capabilities/test/runs", "huge", strings.Repeat(" ", studio.MaxIngressBytes+1), 413},
		{"/api/services/signal/signals", "", `{"source":"queue.jobs","delivery_id":"wrong","payload":{},"extra":1}`, 400},
	} {
		w := deliveryRequest(s, test.path, test.key, test.body)
		if w.Code != test.code {
			t.Fatal(test.path, w.Code, w.Body.String())
		}
	}
	r := httptest.NewRequest("POST", "http://localhost/capabilities/test/runs", strings.NewReader(`{}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Idempotency-Key", "cross")
	r.Header.Set("Origin", "https://other.example")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("cross-origin intake bypassed protection", w.Code)
	}
}
