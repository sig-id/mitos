package web

import (
	"encoding/json"
	"testing"

	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestCronPreviewAndSaveExposeBoundedCalendarClock(t *testing.T) {
	s, e := testServer(t)
	e.Close()
	catalog := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "inspect", Scope: "project", Trigger: "cron", Schedule: &service.Schedule{Type: "cron", Cron: "*/15 * * * *", Timezone: "Europe/Zurich"}}}, Intent: service.Intent{Enable: []string{"inspect"}}}, Work: map[string]studio.Brief{"inspect": {Title: "Calendar inventory", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}}}
	body, _ := json.Marshal(catalog)
	preview := request(s, "POST", "/api/services/preview", string(body), false)
	var v studio.ServiceView
	if preview.Code != 200 || json.Unmarshal(preview.Body.Bytes(), &v) != nil || len(v.ClockPreview) != 1 || v.ClockPreview[0].NextAt.Second() != 0 {
		t.Fatal("cron preview failed", preview.Body.String())
	}
	initial, _ := e.Store.Services()
	if initial.Catalog.Revision != 0 {
		t.Fatal("preview wrote catalog")
	}
	body, _ = json.Marshal(map[string]any{"expected_revision": 0, "catalog": catalog})
	saved := request(s, "PUT", "/api/services", string(body), false)
	if saved.Code != 200 || json.Unmarshal(saved.Body.Bytes(), &v) != nil || v.States[0].NextAt == nil {
		t.Fatal("cron save failed", saved.Body.String())
	}
	catalog.Config.Units[0].Schedule.Cron = "* * 30 2 *"
	body, _ = json.Marshal(catalog)
	if result := request(s, "POST", "/api/services/preview", string(body), false); result.Code != 422 {
		t.Fatal("impossible date accepted")
	}
	if result := request(s, "GET", "/services", "", false); result.Code != 200 {
		t.Fatal("cron console failed", result.Body.String())
	}
}
