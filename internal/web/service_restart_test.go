package web

import (
	"encoding/json"
	"strings"
	"testing"

	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestServiceRetryConsoleUsesSharedCancellation(t *testing.T) {
	s, e := testServer(t)
	e.Close()
	catalog := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "inspect", Scope: "project", Trigger: "manual", RestartPolicy: service.RestartPolicy{OnFailure: "retry", Backoff: service.Backoff{Initial: 30}}}}, Intent: service.Intent{Enable: []string{"inspect"}}}, Work: map[string]studio.Brief{"inspect": {Title: "Retry inventory", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}}}
	body, _ := json.Marshal(map[string]any{"expected_revision": 0, "catalog": catalog})
	if response := request(s, "PUT", "/api/services", string(body), false); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	run, err := e.TriggerService("inspect", "first")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.Store.Schedule(run.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	if err = e.Store.Transition(run.ID, "run_failed", "failed", "Failed", "Fixture failure"); err != nil {
		t.Fatal(err)
	}
	page := request(s, "GET", "/services", "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Cancel retry") || !strings.Contains(page.Body.String(), "Retry 1 due") {
		t.Fatal("retry controls missing", page.Body.String())
	}
	for i := 0; i < 2; i++ {
		if response := request(s, "POST", "/runs/"+run.ID+"/cancel", "", false); response.Code != 303 {
			t.Fatal("pending retry cancellation failed", response.Body.String())
		}
	}
	task, _ := e.Store.Task(run.TaskID)
	if task.Status != "cancelled" {
		t.Fatal("retry cancellation did not close Task")
	}
	view := request(s, "GET", "/api/services", "", false)
	var state studio.ServiceView
	if view.Code != 200 || json.Unmarshal(view.Body.Bytes(), &state) != nil || len(state.States[0].Retries) != 0 {
		t.Fatal("cancelled retry still pending")
	}
	if response := request(s, "POST", "/api/services/preview", `{"config":{"units":[{"id":"inspect","scope":"project","trigger":"manual","restart_policy":{"on_failure":"retry","unknown":true}}]}}`, false); response.Code != 400 {
		t.Fatal("unknown restart setting ignored")
	}
}
