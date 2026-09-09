package web

import (
	"encoding/json"
	"strings"
	"testing"

	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestEventCatalogHooksAndRuntimeEvidenceSurfaces(t *testing.T) {
	s, e := testServer(t)
	e.Close()
	catalog := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "producer", Scope: "project", Trigger: "manual"}, {ID: "consumer", Scope: "project", Triggers: &service.Triggers{OnComplete: []string{"producer"}}}}, Intent: service.Intent{Enable: []string{"producer", "consumer"}}}, Work: map[string]studio.Brief{"producer": {Title: "Produce inventory", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}, "consumer": {Title: "Follow inventory", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}}}
	data, _ := json.Marshal(map[string]any{"expected_revision": 0, "catalog": catalog})
	saved := request(s, "PUT", "/api/services", string(data), false)
	if saved.Code != 200 {
		t.Fatal(saved.Body.String())
	}
	run, err := e.TriggerService("producer", "one")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.Store.Schedule(run.ID, nil); err != nil || !ok {
		t.Fatal(err)
	}
	if err = e.Store.Complete(run.ID, "Done", true); err != nil {
		t.Fatal(err)
	}
	response := request(s, "GET", "/api/services", "", false)
	var view studio.ServiceView
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &view) != nil {
		t.Fatal(response.Body.String())
	}
	found := false
	for _, state := range view.States {
		if state.ServiceID == "consumer" {
			found = state.EventsPending == 1 && state.LastEvent != nil && state.LastEvent.Cause.RunID == run.ID
		}
	}
	if !found {
		t.Fatal("event reservation/evidence missing from API")
	}
	page := request(s, "GET", "/services", "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Last event from producer: pending") || !strings.Contains(page.Body.String(), "Source run") {
		t.Fatal("event evidence missing from UI", page.Body.String())
	}
	if result := request(s, "POST", "/api/services/consumer/trigger", `{"idempotency_key":"overlap"}`, false); result.Code != 409 {
		t.Fatal("manual intake ignored event reservation")
	}
	if result := request(s, "POST", "/api/services/preview", `{"config":{"units":[{"id":"consumer","scope":"project","trigger":"event","schedule":{"type":"event","trigger":"worker.completed"}}]}}`, false); result.Code != 422 {
		t.Fatal("unregistered event source accepted")
	}
}
