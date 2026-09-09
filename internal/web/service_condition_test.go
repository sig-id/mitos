package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestServiceConditionHTTPAndUI(t *testing.T) {
	s, e := testServer(t)
	c := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "inventory", Scope: "project", Trigger: "manual", Condition: &service.Condition{FileExists: "missing-input"}}}, Intent: service.Intent{Enable: []string{"inventory"}}}, Work: map[string]studio.Brief{"inventory": {Title: "Conditional inventory", Workspace: e.Root, AppID: e.Apps[0].ID, TimeoutMinutes: 1}}}
	body, _ := json.Marshal(map[string]any{"expected_revision": 0, "catalog": c})
	if response := request(s, "PUT", "/api/services", string(body), false); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	response := request(s, "POST", "/api/services/inventory/trigger", `{"idempotency_key":"conditional"}`, false)
	if response.Code != 201 && response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	var run studio.Run
	if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		run, _ = e.Store.Get(run.ID)
		if run.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.Status != "cancelled" || run.Condition == nil || run.Condition.Status != "skipped" {
		t.Fatal(run.Status, run.Condition)
	}
	for route, expected := range map[string]string{"/runs/" + run.ID: "Service condition", "/services": "Execution condition: skipped", "/api/runs/" + run.ID: `"condition"`} {
		page := request(s, "GET", route, "", false)
		if page.Code != 200 || !strings.Contains(page.Body.String(), expected) {
			t.Fatal(route, page.Code, page.Body.String())
		}
	}
}
