package web

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"mitos/internal/desk"
	"mitos/internal/studio"
)

func TestProjectOverviewFiltersAndCountsBeforePagination(t *testing.T) {
	s, e := testServer(t)
	project, err := e.Desk.CreateProject("Overview", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	other, err := e.Desk.CreateProject("Other", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 52; i++ {
		task, err := e.Desk.CreateWork(project.ID, desk.NewWork{Key: fmt.Sprint(i), Title: fmt.Sprintf("Assignment %02d", i), Intent: "Do work", Criteria: "Review result", Role: "agent"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if _, err = e.Desk.UpdateWork(project.ID, task.ID, "cancel", desk.WorkAction{Revision: task.Revision}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err = e.Desk.CreateWork(other.ID, desk.NewWork{Key: "other", Title: "Other assignment", Intent: "Do work", Criteria: "Review result", Role: "agent"}); err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"closed", "archived"} {
		req := studio.CreateGraph{Profile: studio.GraphProfile, Key: "overview-" + state, ProjectID: project.ID, GoalRevision: 1, Activation: "manual", Policy: studio.GraphPolicy{Mode: "adaptive", Applications: []string{"workspace-inventory"}}, Limits: studio.GraphLimits{Tasks: 16, Amendments: 16, Attempts: 16, Retries: 1, Expansions: 8, OrchestratorCalls: 4, Depth: 4, Fanout: 8, Deadline: time.Now().UTC().Add(time.Hour)}, Tasks: []studio.GraphSpec{{ID: "goal", Kind: "scope", Required: true, Criteria: "Review result", Acceptance: "operator", Brief: studio.Brief{Title: state + " work"}}}}
		body, _ := json.Marshal(req)
		response := request(s, "POST", "/api/graphs", string(body), false)
		var g studio.WorkGraph
		if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &g) != nil {
			t.Fatal(response.Body.String())
		}
		if _, err = e.Store.SetGraphWorkState(g.ID, studio.GraphWorkStateEdit{Key: state, Revision: g.Revision, State: state}); err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		Stats projectWorkStats `json:"stats"`
		Work  []projectWorkRow `json:"work"`
		Total int              `json:"total"`
		Next  int              `json:"next_offset"`
	}
	read := func(query string) result {
		t.Helper()
		response := request(s, "GET", "/api/projects/"+project.ID+"/overview"+query, "", false)
		var out result
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &out) != nil {
			t.Fatal(response.Code, response.Body.String())
		}
		return out
	}
	first := read("")
	if first.Stats.Open != 51 || first.Stats.Closed != 2 || first.Stats.Archived != 1 || first.Stats.ActiveAgents != 0 || first.Stats.Underway != 0 || first.Stats.CostUSD != nil || first.Total != 51 || len(first.Work) != 50 || first.Next != 50 {
		t.Fatalf("first: %+v", first)
	}
	second := read("?offset=50")
	if len(second.Work) != 1 || second.Work[0].ID == first.Work[0].ID || second.Stats != first.Stats {
		t.Fatalf("next page: %+v", second)
	}
	filtered := read("?q=ASSIGNMENT+51")
	if filtered.Total != 1 || filtered.Work[0].Title != "Assignment 51" || filtered.Stats != first.Stats {
		t.Fatalf("search: %+v", filtered)
	}
	for _, state := range []string{"closed", "archived"} {
		out := read("?state=" + state)
		if out.Stats != first.Stats {
			t.Fatal("filter changed statistics")
		}
		for _, row := range out.Work {
			if row.State != state {
				t.Fatal("wrong state", row)
			}
		}
	}
	if out := read("?q=Other"); out.Total != 0 {
		t.Fatal("other project leaked")
	}
	if out := read("?offset=9999"); len(out.Work) != 0 {
		t.Fatal("offset was not bounded")
	}
	for _, query := range []string{"?state=unknown", "?offset=-1", "?offset=nope"} {
		if got := request(s, "GET", "/api/projects/"+project.ID+"/overview"+query, "", false); got.Code != 400 {
			t.Fatal(query, got.Code)
		}
	}
	if got := request(s, "GET", "/api/projects/missing/overview", "", false); got.Code != 404 {
		t.Fatal("missing project", got.Code)
	}
}
