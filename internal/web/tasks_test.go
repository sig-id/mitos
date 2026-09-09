package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"mitos/internal/studio"
	"mitos/internal/workpool"
)

func TestTaskAndPoolSurfacesUseDurableScheduling(t *testing.T) {
	s, e := testServer(t)
	e.Close()
	body, _ := json.Marshal(studio.Brief{Title: "Schedule the inventory", Workspace: e.Root, AppID: "workspace-inventory", TimeoutMinutes: 1, Scheduling: workpool.Request{Pool: "batch", Priority: 7}})
	submitted := request(s, "POST", "/api/tasks", string(body), false)
	var task studio.Task
	if submitted.Code != 201 || json.Unmarshal(submitted.Body.Bytes(), &task) != nil || task.ID == "" || task.Status != "queued" || task.Scheduling.Pool != "batch" {
		t.Fatalf("task intake failed: %s", submitted.Body.String())
	}
	got := request(s, "GET", "/api/tasks/"+task.ID, "", false)
	if got.Code != 200 {
		t.Fatal("task unavailable")
	}
	var read studio.Task
	_ = json.Unmarshal(got.Body.Bytes(), &read)
	if read.CurrentRunID != task.CurrentRunID || read.Scheduling.Priority != 7 {
		t.Fatal("task API changed payload")
	}
	for _, path := range []string{"/tasks", "/runs/" + task.CurrentRunID, "/new"} {
		page := request(s, "GET", path, "", false)
		if page.Code != 200 {
			t.Fatalf("%s failed: %s", path, page.Body.String())
		}
	}
	configure := func(body string) int {
		r := httptest.NewRequest("PUT", "http://localhost/api/pools/batch", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		return w.Code
	}
	if code := configure(`{"id":"batch","discipline":"lifo","concurrency":1,"reserved":0}`); code != 200 {
		t.Fatalf("configure: %d", code)
	}
	if code := configure(`{"id":"other","discipline":"fifo","concurrency":1,"reserved":0}`); code != 400 {
		t.Fatal("path/ID mismatch accepted")
	}
	if code := configure(`{"id":"batch","discipline":"weighted_fair","concurrency":1,"reserved":0}`); code != 422 {
		t.Fatal("unsupported scheduler admitted")
	}
	if code := configure(`{"id":"batch","discipline":"fifo","concurrency":1,"reserved":0,"preempts":["interactive"]}`); code != 400 {
		t.Fatal("unsupported field ignored")
	}
	pools := request(s, "GET", "/api/pools", "", false)
	var policies []workpool.Pool
	if pools.Code != 200 || json.Unmarshal(pools.Body.Bytes(), &policies) != nil {
		t.Fatal("pools API unavailable")
	}
	found := false
	for _, p := range policies {
		if p.ID == "batch" {
			found = p.Discipline == "lifo" && p.Concurrency == 1
		}
	}
	if !found {
		t.Fatal("policy not persisted")
	}
	trailing := request(s, "POST", "/api/tasks", string(body)+` {}`, false)
	if trailing.Code != 400 {
		t.Fatal("multiple task JSON objects accepted")
	}
	form := request(s, "POST", "/pools/batch", "discipline=deadline_edf&concurrency=1&reserved=0", false)
	if form.Code != 303 {
		t.Fatal("console configure did not use shared operation")
	}
	page := request(s, "GET", "/tasks", "", false)
	if !strings.Contains(page.Body.String(), "Schedule the inventory") || !strings.Contains(page.Body.String(), "Pool policies") {
		t.Fatal("task/pool view incomplete")
	}
}

func TestDataSelectionFormPreservesExplicitArtifactNames(t *testing.T) {
	formRequest := httptest.NewRequest("POST", "http://localhost/runs", strings.NewReader("after_tasks=task_one&dependency_inputs=task_one%3Ainventory.json%0Atask_one%3Areport.md"))
	formRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := formRequest.ParseForm(); err != nil {
		t.Fatal(err)
	}
	scheduling, err := formScheduling(formRequest)
	if err != nil {
		t.Fatal(err)
	}
	if len(scheduling.Deps) != 1 || scheduling.Deps[0].Payload != "data" || len(scheduling.Deps[0].Artifacts) != 2 {
		t.Fatal("form lost input selections")
	}
	s, _ := testServer(t)
	page := request(s, "GET", "/new", "", false).Body.String()
	if !strings.Contains(page, "dependency_inputs") {
		t.Fatal("data input control unavailable")
	}
}
