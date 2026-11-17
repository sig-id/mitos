package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mitos/internal/studio"
)

// A queued graph attempt attaches a shared graph-level journal event to its
// run. The autonomy surfaces must stay usable for the whole store.
func TestAutonomySurfacesSurviveQueuedGraphAttempts(t *testing.T) {
	s, e := testServer(t)
	project, err := e.Desk.CreateProject("Graph metrics", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	req := studio.CreateGraph{Profile: studio.GraphProfile, Key: "api-metrics", ProjectID: project.ID, GoalRevision: 1, Activation: "manual", Policy: studio.GraphPolicy{Mode: "adaptive", Applications: []string{"workspace-inventory"}}, Limits: studio.GraphLimits{Tasks: 16, Amendments: 16, Attempts: 16, Retries: 1, Expansions: 8, OrchestratorCalls: 4, Depth: 4, Fanout: 8, Deadline: time.Now().UTC().Add(time.Hour)}, Tasks: []studio.GraphSpec{
		{ID: "goal", Kind: "scope", Required: true, Criteria: "Explore project", Acceptance: "operator"},
		{ID: "work", Parent: "goal", Kind: "application", Required: true, Criteria: "Inventory is independently checked", Acceptance: "application", Brief: studio.Brief{Title: "Inspect workspace", AppID: "workspace-inventory", TimeoutMinutes: 1}},
	}}
	body, _ := json.Marshal(req)
	created := request(s, "POST", "/api/graphs", string(body), false)
	var g studio.WorkGraph
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &g) != nil {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	queued := false
	for until := time.Now().Add(10 * time.Second); time.Now().Before(until) && !queued; {
		for _, r := range e.Store.List() {
			if r.GraphAttempt != nil {
				queued = true
			}
		}
		if !queued {
			time.Sleep(25 * time.Millisecond)
		}
	}
	if !queued {
		t.Fatal("graph attempt was never queued")
	}
	api := request(s, "GET", "/api/autonomy", "", false)
	var report studio.AutonomyReport
	if api.Code != 200 || json.Unmarshal(api.Body.Bytes(), &report) != nil {
		t.Fatalf("metrics API failed on graph store: %d %s", api.Code, api.Body.String())
	}
	if report.Runs == 0 {
		t.Fatal("graph run excluded from metrics")
	}
	projected := false
	for _, note := range report.Limitations {
		if strings.Contains(note, "shared graph journal events") {
			projected = true
		}
	}
	if !projected {
		t.Fatalf("graph projection not declared: %+v", report.Limitations)
	}
	page := request(s, "GET", "/autonomy", "", false)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "Autonomy evidence") {
		t.Fatalf("metrics page failed on graph store: %d", page.Code)
	}
}
