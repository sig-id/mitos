package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestContinuousCatalogAndRuntimeSurfaces(t *testing.T) {
	s, e := testServer(t)
	e.Close()
	c := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "daemon", Scope: "project", Trigger: "continuous", Schedule: &service.Schedule{Type: "continuous"}}}, Intent: service.Intent{Enable: []string{"daemon"}}}, Work: map[string]studio.Brief{"daemon": {Title: "Maintain inventory", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}}}
	body, _ := json.Marshal(c)
	preview := request(s, "POST", "/api/services/preview", string(body), false)
	if preview.Code != 200 || len(e.Store.Tasks()) != 0 {
		t.Fatal("continuous preview mutated or rejected", preview.Body.String())
	}
	body, _ = json.Marshal(map[string]any{"expected_revision": 0, "catalog": c})
	saved := request(s, "PUT", "/api/services", string(body), false)
	var view studio.ServiceView
	if saved.Code != 200 || json.Unmarshal(saved.Body.Bytes(), &view) != nil {
		t.Fatal(saved.Body.String())
	}
	if len(view.States) != 1 || view.States[0].Continuous == nil || view.States[0].Continuous.Status != "waiting" || view.States[0].Continuous.NextAt == nil || view.States[0].NextAt != nil {
		t.Fatal("missing controller state", view)
	}
	page := request(s, "GET", "/services", "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Continuous service: waiting") || !strings.Contains(page.Body.String(), "Continuous services also stop their managed task.") {
		t.Fatal(page.Body.String())
	}
	c.Config.Units[0].Schedule.Interval = 1
	body, _ = json.Marshal(c)
	if invalid := request(s, "POST", "/api/services/preview", string(body), false); invalid.Code != 422 {
		t.Fatal("mixed interval accepted", invalid.Body.String())
	}
}

func TestContinuousRunExplainsStopAndReplacement(t *testing.T) {
	s, e := testServer(t)
	c := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "daemon", Scope: "project", Trigger: "continuous", Schedule: &service.Schedule{Type: "continuous"}}}, Intent: service.Intent{Enable: []string{"daemon"}}}, Work: map[string]studio.Brief{"daemon": {Title: "Continuous inventory", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}}}
	if _, err := e.ConfigureServices(c, 0); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(e.Store.Tasks()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	e.Close()
	tasks := e.Store.Tasks()
	if len(tasks) != 1 {
		t.Fatal("controller did not create run")
	}
	page := request(s, "GET", "/runs/"+tasks[0].CurrentRunID, "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "While enabled, the service replaces stopped or finished tasks") || strings.Contains(page.Body.String(), "Run again") {
		t.Fatal("run page misrepresented lifecycle", page.Body.String())
	}
}
