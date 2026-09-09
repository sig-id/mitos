package web

import (
	"encoding/json"
	"strings"
	"testing"

	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestServiceReplacementAPIAndEvidenceSurfaces(t *testing.T) {
	s, e := testServer(t)
	e.Close()
	c := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "worker", Scope: "project", Trigger: "manual", Concurrency: service.Concurrency{IfRunning: "kill"}}}, Intent: service.Intent{Enable: []string{"worker"}}}, Work: map[string]studio.Brief{"worker": {Title: "Replace overlapping work", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}}}
	data, _ := json.Marshal(map[string]any{"expected_revision": 0, "catalog": c})
	saved := request(s, "PUT", "/api/services", string(data), false)
	if saved.Code != 200 {
		t.Fatal(saved.Body.String())
	}
	first, err := e.TriggerService("worker", "first")
	if err != nil {
		t.Fatal(err)
	}
	response := request(s, "POST", "/api/services/worker/trigger", `{"idempotency_key":"replacement"}`, false)
	var next studio.Run
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &next) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	task, err := e.Store.Task(next.TaskID)
	if err != nil || task.ServiceReplacement == nil || task.ServiceReplacement.TaskID != first.TaskID {
		t.Fatal("missing replacement evidence", err)
	}
	if extra := request(s, "POST", "/api/services/worker/trigger", `{"idempotency_key":"overflow"}`, false); extra.Code != 409 {
		t.Fatal("replacement queue unbounded", extra.Body.String())
	}
	page := request(s, "GET", "/services", "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "waiting for shutdown and claim release") || !strings.Contains(page.Body.String(), first.ID) {
		t.Fatal(page.Body.String())
	}
	page = request(s, "GET", "/runs/"+next.ID, "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "previous service work") || !strings.Contains(page.Body.String(), first.ID) {
		t.Fatal(page.Body.String())
	}
}
