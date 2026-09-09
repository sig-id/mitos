package web

import (
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestServicesPreviewSaveAndTriggerSurfaces(t *testing.T) {
	s, e := testServer(t)
	e.Close()
	catalog := studio.ServiceCatalog{Config: service.Config{
		Units:  []service.Unit{{ID: "inspect", Scope: "project", Trigger: "manual"}},
		Intent: service.Intent{Enable: []string{"inspect"}},
	}, Work: map[string]studio.Brief{"inspect": {Title: "Service inventory", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1}}}
	data, _ := json.Marshal(catalog)
	preview := request(s, "POST", "/api/services/preview", string(data), false)
	if preview.Code != 200 {
		t.Fatal(preview.Body.String())
	}
	initial, _ := e.Store.Services()
	if initial.Catalog.Revision != 0 || len(e.Store.Tasks()) != 0 {
		t.Fatal("preview changed saved state")
	}
	for _, body := range []string{string(data) + " {}", `{"config":{},"work":{},"unknown":true}`} {
		if r := request(s, "POST", "/api/services/preview", body, false); r.Code != 400 {
			t.Fatal("invalid JSON accepted")
		}
	}
	save, _ := json.Marshal(map[string]any{"expected_revision": 0, "catalog": catalog})
	saved := request(s, "PUT", "/api/services", string(save), false)
	var view studio.ServiceView
	if saved.Code != 200 || json.Unmarshal(saved.Body.Bytes(), &view) != nil {
		t.Fatal(saved.Body.String())
	}
	if r := request(s, "PUT", "/api/services", string(save), false); r.Code != 409 {
		t.Fatal("stale configuration accepted")
	}
	page := request(s, "GET", "/services", "", false)
	for _, want := range []string{"Desired activation", "inspect", "Trigger task", "idempotency_key"} {
		if page.Code != 200 || !strings.Contains(page.Body.String(), want) {
			t.Fatal("service view missing", want, page.Body.String())
		}
	}
	trigger := request(s, "POST", "/api/services/inspect/trigger", `{"idempotency_key":"request-1"}`, false)
	var run studio.Run
	if trigger.Code != 200 || json.Unmarshal(trigger.Body.Bytes(), &run) != nil || run.ID == "" {
		t.Fatal(trigger.Body.String())
	}
	duplicate := request(s, "POST", "/services/inspect/trigger", "idempotency_key=request-1", false)
	if duplicate.Code != 303 || duplicate.Header().Get("Location") != "/runs/"+run.ID || len(e.Store.Tasks()) != 1 {
		t.Fatal("HTML retry did not share idempotency")
	}
	task, _ := e.Store.Task(run.TaskID)
	if task.Source != "trigger:inspect" {
		t.Fatal("missing service task source")
	}
	view.Catalog.Config.Intent.Disable = []string{"inspect"}
	data, _ = json.Marshal(view.Catalog)
	form := url.Values{"catalog": {string(data)}, "expected_revision": {strconv.FormatInt(view.Catalog.Revision, 10)}}
	if r := request(s, "POST", "/services/catalog", form.Encode(), false); r.Code != 303 {
		t.Fatal(r.Body.String())
	}
	if r := request(s, "POST", "/api/services/inspect/trigger", `{"idempotency_key":"request-2"}`, false); r.Code != 409 {
		t.Fatal("disabled service triggered")
	}
	if r := request(s, "GET", "/api/autonomy", "", false); r.Code != 200 {
		t.Fatal("service task broke metrics", r.Body.String())
	}
}
