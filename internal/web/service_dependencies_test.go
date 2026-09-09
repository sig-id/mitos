package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestServiceDependenciesHTTPAndUI(t *testing.T) {
	s, e := testServer(t)
	brief := studio.Brief{Title: "Cycle inventory", Workspace: e.Root, AppID: e.Apps[0].ID, TimeoutMinutes: 1}
	c := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "upstream", Scope: "project", Trigger: "manual"}, {ID: "downstream", Scope: "project", Trigger: "manual", DependsOn: []string{"upstream"}}}, Intent: service.Intent{Enable: []string{"upstream", "downstream"}}}, Work: map[string]studio.Brief{"upstream": brief, "downstream": brief}}
	body, _ := json.Marshal(map[string]any{"expected_revision": 0, "catalog": c})
	if response := request(s, "PUT", "/api/services", string(body), false); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if response := request(s, "POST", "/api/services/downstream/trigger", `{"idempotency_key":"missing"}`, false); response.Code != 409 {
		t.Fatal("missing cycle accepted")
	}
	response := request(s, "POST", "/api/services/downstream/trigger", `{"idempotency_key":"down","cycle":"batch"}`, false)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	var down studio.Run
	_ = json.Unmarshal(response.Body.Bytes(), &down)
	response = request(s, "POST", "/api/services/upstream/trigger", `{"idempotency_key":"up","cycle":"batch"}`, false)
	if response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		down, _ = e.Store.Get(down.ID)
		if down.Terminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if down.Status != "done" {
		t.Fatal(down.Status)
	}
	task, _ := e.Store.Task(down.TaskID)
	if len(task.ServiceDependencyEvidence) != 1 {
		t.Fatal("missing HTTP dependency evidence")
	}
	for route, needle := range map[string]string{"/runs/" + down.ID: "Service cycle", "/services": "Cycle key", "/api/tasks/" + down.TaskID: `"service_dependency_evidence"`} {
		page := request(s, "GET", route, "", false)
		if page.Code != 200 || !strings.Contains(page.Body.String(), needle) {
			t.Fatal(route, page.Code, page.Body.String())
		}
	}
}
