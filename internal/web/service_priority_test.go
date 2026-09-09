package web

import (
	"encoding/json"
	"strings"
	"testing"

	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestServicePrioritySharedCatalogTaskAndUI(t *testing.T) {
	s, e := testServer(t)
	e.Close()
	c := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "urgent", Scope: "project", Trigger: "manual", Concurrency: service.Concurrency{Priority: "critical"}}}, Intent: service.Intent{Enable: []string{"urgent"}}}, Work: map[string]studio.Brief{"urgent": {Title: "Urgent inventory", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}}}
	data, _ := json.Marshal(map[string]any{"expected_revision": 0, "catalog": c})
	saved := request(s, "PUT", "/api/services", string(data), false)
	if saved.Code != 200 {
		t.Fatal(saved.Body.String())
	}
	run, err := e.TriggerService("urgent", "one")
	if err != nil {
		t.Fatal(err)
	}
	response := request(s, "GET", "/api/tasks/"+run.TaskID, "", false)
	var task studio.Task
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &task) != nil || task.ServiceQueue == nil || task.ServiceQueue.Priority != "critical" || task.ServiceQueue.Adjustment != 200 {
		t.Fatal(response.Body.String())
	}
	page := request(s, "GET", "/services", "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Queue priority: critical") {
		t.Fatal(page.Body.String())
	}
	page = request(s, "GET", "/tasks", "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "service priority critical") {
		t.Fatal(page.Body.String())
	}
	c.Config.Units[0].Concurrency.Priority = "urgent"
	data, _ = json.Marshal(c)
	if invalid := request(s, "POST", "/api/services/preview", string(data), false); invalid.Code != 422 {
		t.Fatal("unregistered priority accepted", invalid.Body.String())
	}
}
