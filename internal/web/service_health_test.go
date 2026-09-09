package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mitos/internal/acceptance"
	"mitos/internal/service"
	"mitos/internal/studio"
)

func TestServiceHealthHeartbeatHTTPAndUIScopes(t *testing.T) {
	s, e := testServer(t)
	e.Apps = append(e.Apps, studio.Application{ID: "health-worker", Name: "Health worker", Directory: t.TempDir(), Entrypoint: "Run", Command: []string{"/bin/sh", "-c", "sleep 60"}, Acceptance: acceptance.Spec{Cmd: []string{"/bin/sh", "-c", "exit 0"}}})
	c := studio.ServiceCatalog{Config: service.Config{Units: []service.Unit{{ID: "worker", Scope: "project", Trigger: "manual", Health: &service.Health{Type: "heartbeat", Interval: 1, MaxAge: 60}}}, Intent: service.Intent{Enable: []string{"worker"}}}, Work: map[string]studio.Brief{"worker": {Title: "Monitor worker", Workspace: e.Root, AppID: "health-worker", TimeoutMinutes: 1}}}
	body, _ := json.Marshal(map[string]any{"expected_revision": 0, "catalog": c})
	if saved := request(s, "PUT", "/api/services", string(body), false); saved.Code != 200 {
		t.Fatal(saved.Body.String())
	}
	run, err := e.TriggerService("worker", "one")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, _ = e.Store.Get(run.ID)
		if run.Health != nil && run.Health.Status == "watching" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.Health == nil || run.Health.Status != "watching" {
		t.Fatal("no live health monitor")
	}
	body, _ = json.Marshal(map[string]any{"monitor_id": run.Health.MonitorID, "sequence": 1})
	first := request(s, "POST", "/api/runs/"+run.ID+"/heartbeat", string(body), false)
	duplicate := request(s, "POST", "/api/runs/"+run.ID+"/heartbeat", string(body), false)
	var a, b studio.ServiceHealthState
	if first.Code != 200 || duplicate.Code != 200 || json.Unmarshal(first.Body.Bytes(), &a) != nil || json.Unmarshal(duplicate.Body.Bytes(), &b) != nil || a.HeartbeatAt == nil || !a.HeartbeatAt.Equal(*b.HeartbeatAt) {
		t.Fatal("heartbeat is not idempotent", first.Body.String(), duplicate.Body.String())
	}
	if stale := request(s, "POST", "/api/runs/"+run.ID+"/heartbeat", `{"monitor_id":"old","sequence":2}`, false); stale.Code != 409 {
		t.Fatal("stale monitor accepted")
	}
	if invalid := request(s, "POST", "/api/runs/"+run.ID+"/heartbeat", `{"monitor_id":"old","sequence":0}`, false); invalid.Code != 400 {
		t.Fatal("zero sequence accepted")
	}
	page := request(s, "GET", "/runs/"+run.ID, "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Service health") || !strings.Contains(page.Body.String(), "Last heartbeat:") {
		t.Fatal(page.Body.String())
	}
	page = request(s, "GET", "/services", "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Health checks") {
		t.Fatal(page.Body.String())
	}
}
