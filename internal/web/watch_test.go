package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"mitos/internal/studio"
)

func TestWatchAPIEscalationReachesNotifications(t *testing.T) {
	server, engine := testServer(t)
	run := seedPendingRun(t, engine)
	body := fmt.Sprintf(`{"version":"mitos.watch/v1","owner":{"kind":"operator"},"description":"inventory escalations",
		"condition":{"scope":{"run":%q},"kinds":["note"]},
		"lease":{"expires_at":%q,"max_renewals":2},
		"grouping":{"by":["run_id"],"wait_ms":0,"interval_ms":1000,"max_events":16,"repeat_interval_ms":60000},
		"default_level":"escalate"}`, run.ID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	created := request(server, "POST", "/api/watches", body, false)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var w studio.WatchView
	if err := json.Unmarshal(created.Body.Bytes(), &w); err != nil {
		t.Fatal(err)
	}
	if unknown := request(server, "POST", "/api/watches", strings.Replace(body, `"default_level"`, `"surprise":1,"default_level"`, 1), false); unknown.Code != 400 {
		t.Fatalf("unknown field accepted: %d", unknown.Code)
	}
	if err := engine.Store.RecordEvidence(run.ID, "note", "observed", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Store.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var home struct {
		Watches []studio.WatchItem `json:"watches"`
	}
	if err := json.Unmarshal(request(server, "GET", "/api/workspace-home", "", false).Body.Bytes(), &home); err != nil {
		t.Fatal(err)
	}
	if len(home.Watches) != 1 || home.Watches[0].Description != "inventory escalations" {
		t.Fatalf("escalation missing from Notifications: %+v", home.Watches)
	}
	if res := request(server, "POST", "/api/deliveries/"+home.Watches[0].ID+"/resolve", "", false); res.Code != 200 {
		t.Fatalf("resolve: %d %s", res.Code, res.Body.String())
	}
	if res := request(server, "POST", "/api/deliveries/"+home.Watches[0].ID+"/resolve", "", false); res.Code != 409 {
		t.Fatalf("second resolve: %d", res.Code)
	}
	deliveries := request(server, "GET", "/api/watches/"+w.ID+"/deliveries", "", false)
	if deliveries.Code != 200 || !strings.Contains(deliveries.Body.String(), `"item_state":"resolved"`) {
		t.Fatalf("deliveries: %d %s", deliveries.Code, deliveries.Body.String())
	}
	if res := request(server, "POST", "/api/watches/"+w.ID+"/cancel", "", false); res.Code != 200 {
		t.Fatalf("cancel: %d", res.Code)
	}
	if res := request(server, "GET", "/api/watches/missing", "", false); res.Code != 404 {
		t.Fatalf("missing watch: %d", res.Code)
	}
}
