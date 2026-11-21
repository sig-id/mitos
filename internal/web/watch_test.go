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
		Watches []studio.WatchItemView `json:"watches"`
	}
	if err := json.Unmarshal(request(server, "GET", "/api/workspace-home", "", false).Body.Bytes(), &home); err != nil {
		t.Fatal(err)
	}
	if len(home.Watches) != 1 || home.Watches[0].Description != "inventory escalations" {
		t.Fatalf("escalation missing from Notifications: %+v", home.Watches)
	}
	if res := request(server, "POST", "/api/watch-items/"+home.Watches[0].ID+"/resolve", "", false); res.Code != 200 {
		t.Fatalf("resolve: %d %s", res.Code, res.Body.String())
	}
	if res := request(server, "POST", "/api/watch-items/"+home.Watches[0].ID+"/resolve", "", false); res.Code != 409 {
		t.Fatalf("second resolve: %d", res.Code)
	}
	items := request(server, "GET", "/api/watches/"+w.ID+"/items", "", false)
	if items.Code != 200 || !strings.Contains(items.Body.String(), `"state":"resolved"`) {
		t.Fatalf("items: %d %s", items.Code, items.Body.String())
	}
	deliveries := request(server, "GET", "/api/watches/"+w.ID+"/deliveries", "", false)
	if deliveries.Code != 200 || !strings.Contains(deliveries.Body.String(), `"item_id":"`+home.Watches[0].ID+`"`) || strings.Contains(deliveries.Body.String(), `"state"`) {
		t.Fatalf("deliveries must reference the item, not carry its state: %d %s", deliveries.Code, deliveries.Body.String())
	}
	if res := request(server, "POST", "/api/watches/"+w.ID+"/cancel", "", false); res.Code != 200 {
		t.Fatalf("cancel: %d", res.Code)
	}
	if res := request(server, "GET", "/api/watches/"+w.ID+"/triage", "", false); res.Code != 200 || strings.TrimSpace(res.Body.String()) != "[]" {
		t.Fatalf("triage records: %d %s", res.Code, res.Body.String())
	}
	if res := request(server, "POST", "/api/watches", strings.Replace(body, `"default_level"`, `"triage":{"policy_ref":"p","instructions":"x"},"default_level"`, 1), false); res.Code != 400 {
		t.Fatalf("an unknown triage field was accepted: %d", res.Code)
	}
	if res := request(server, "GET", "/api/watches/missing", "", false); res.Code != 404 {
		t.Fatalf("missing watch: %d", res.Code)
	}
}

func TestWatchInboxAPI(t *testing.T) {
	server, _ := testServer(t)
	res := request(server, "GET", "/api/watch-inboxes/responsibility/graph_none", "", false)
	if res.Code != 200 || !strings.Contains(res.Body.String(), `"pending":[]`) || !strings.Contains(res.Body.String(), `"capacity":64`) {
		t.Fatalf("empty inbox: %d %s", res.Code, res.Body.String())
	}
	if res = request(server, "GET", "/api/watch-inboxes/session/s1", "", false); res.Code != 400 {
		t.Fatalf("session owner accepted: %d", res.Code)
	}
}

func TestWatchOutcomeAndReportAPI(t *testing.T) {
	server, engine := testServer(t)
	run := seedPendingRun(t, engine)
	body := fmt.Sprintf(`{"version":"mitos.watch/v1","owner":{"kind":"operator"},"description":"outcomes",
		"condition":{"scope":{"run":%q},"kinds":["note"]},
		"lease":{"expires_at":%q,"max_renewals":2},
		"grouping":{"by":["run_id"],"wait_ms":0,"interval_ms":1000,"max_events":16,"repeat_interval_ms":60000},
		"default_level":"notify"}`, run.ID, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	created := request(server, "POST", "/api/watches", body, false)
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var w studio.WatchView
	if err := json.Unmarshal(created.Body.Bytes(), &w); err != nil {
		t.Fatal(err)
	}
	if err := engine.Store.RecordEvidence(run.ID, "note", "observed", map[string]string{}); err != nil {
		t.Fatal(err)
	}
	if err := engine.Store.EvaluateWatches(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	deliveries := request(server, "GET", "/api/watches/"+w.ID+"/deliveries", "", false)
	if deliveries.Code != 200 || !strings.Contains(deliveries.Body.String(), `"id":"delivery_`) {
		t.Fatalf("deliveries: %d %s", deliveries.Code, deliveries.Body.String())
	}
	var ds []studio.WatchDelivery
	if err := json.Unmarshal(deliveries.Body.Bytes(), &ds); err != nil || len(ds) != 1 {
		t.Fatalf("deliveries %v %s", err, deliveries.Body.String())
	}
	id := ds[0].ID

	if res := request(server, "POST", "/api/deliveries/"+id+"/outcome", `{"outcome":"acted","mark":"needless_wake"}`, false); res.Code != 400 {
		t.Fatalf("outcome and mark together accepted: %d", res.Code)
	}
	if res := request(server, "POST", "/api/deliveries/"+id+"/outcome", `{"outcome":"acted","extra":1}`, false); res.Code != 400 {
		t.Fatalf("unknown field accepted: %d", res.Code)
	}
	if res := request(server, "POST", "/api/deliveries/delivery_none/outcome", `{"outcome":"acted"}`, false); res.Code != 404 {
		t.Fatalf("missing delivery: %d", res.Code)
	}
	if res := request(server, "POST", "/api/deliveries/"+id+"/outcome", `{"outcome":"acted"}`, false); res.Code != 200 || !strings.Contains(res.Body.String(), `"outcome":"acted"`) {
		t.Fatalf("outcome: %d %s", res.Code, res.Body.String())
	}
	if res := request(server, "POST", "/api/deliveries/"+id+"/outcome", `{"outcome":"dismissed"}`, false); res.Code != 409 {
		t.Fatalf("second outcome: %d", res.Code)
	}
	// The operator mark is an independent record on the same delivery.
	if res := request(server, "POST", "/api/deliveries/"+id+"/outcome", `{"mark":"needless_wake"}`, false); res.Code != 200 || !strings.Contains(res.Body.String(), `"mark":"needless_wake"`) {
		t.Fatalf("mark beside outcome: %d %s", res.Code, res.Body.String())
	}
	if res := request(server, "POST", "/api/deliveries/"+id+"/outcome", `{"mark":"should_have_woken"}`, false); res.Code != 409 {
		t.Fatalf("second mark: %d", res.Code)
	}

	report := request(server, "GET", "/api/watches/"+w.ID+"/report", "", false)
	if report.Code != 200 || !strings.Contains(report.Body.String(), `"outcomes":{"acted":1}`) || !strings.Contains(report.Body.String(), `"marks":{"needless_wake":1}`) {
		t.Fatalf("report: %d %s", report.Code, report.Body.String())
	}
	if res := request(server, "GET", "/api/watches/watch_none/report", "", false); res.Code != 404 {
		t.Fatalf("missing report: %d", res.Code)
	}
}
