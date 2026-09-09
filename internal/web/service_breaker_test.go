package web

import (
	"encoding/json"
	"strings"
	"testing"

	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestServiceCircuitAPIAndConsoleExposeFailureGate(t *testing.T) {
	s, e := testServer(t)
	e.Close()
	catalog := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "inspect", Scope: "project", Trigger: "manual", CircuitBreaker: service.BreakerConfig{Enabled: true, Threshold: 1, Cooldown: 300}}}, Intent: service.Intent{Enable: []string{"inspect"}}}, Work: map[string]studio.Brief{"inspect": {Title: "Service inventory", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}}}
	body, _ := json.Marshal(map[string]any{"expected_revision": 0, "catalog": catalog})
	if result := request(s, "PUT", "/api/services", string(body), false); result.Code != 200 {
		t.Fatal(result.Body.String())
	}
	submitted := request(s, "POST", "/api/services/inspect/trigger", `{"idempotency_key":"first"}`, false)
	var run studio.Run
	if submitted.Code != 200 || json.Unmarshal(submitted.Body.Bytes(), &run) != nil {
		t.Fatal(submitted.Body.String())
	}
	if ok, err := e.Store.Schedule(run.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	if err := e.Store.Transition(run.ID, "run_failed", "failed", "Failed", "Fixture failure"); err != nil {
		t.Fatal(err)
	}
	view := request(s, "GET", "/api/services", "", false)
	var state studio.ServiceView
	if view.Code != 200 || json.Unmarshal(view.Body.Bytes(), &state) != nil || state.States[0].Breaker.State != "open" {
		t.Fatal("circuit missing from API")
	}
	page := request(s, "GET", "/services", "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Circuit open") || !strings.Contains(page.Body.String(), "recovery available after") {
		t.Fatal("circuit missing from console", page.Body.String())
	}
	if result := request(s, "POST", "/api/services/inspect/trigger", `{"idempotency_key":"second"}`, false); result.Code != 409 {
		t.Fatal("API bypassed breaker")
	}
	if result := request(s, "POST", "/api/services/inspect/trigger", `{"idempotency_key":"first"}`, false); result.Code != 200 {
		t.Fatal("breaker hid previous receipt")
	}
	if result := request(s, "POST", "/api/services/preview", `{"config":{"units":[{"id":"inspect","scope":"project","trigger":"manual","circuit_breaker":{"enabled":true,"unknown":true}}]}}`, false); result.Code != 400 {
		t.Fatal("unknown breaker field ignored")
	}
}
