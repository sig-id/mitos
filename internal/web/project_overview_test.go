package web

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"mitos/internal/desk"
	"mitos/internal/harness"
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
	var firstTask desk.WorkAssignment
	for i := 0; i < 52; i++ {
		task, err := e.Desk.CreateWork(project.ID, desk.NewWork{Key: fmt.Sprint(i), Title: fmt.Sprintf("Assignment %02d", i), Intent: "Do work", Criteria: "Review result", Role: "agent"})
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			firstTask = task
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
	// Close the oldest task last so the two descending sort orders differ.
	if _, err = e.Desk.UpdateWork(project.ID, firstTask.ID, "cancel", desk.WorkAction{Revision: firstTask.Revision}); err != nil {
		t.Fatal(err)
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
	oldest := read("?sort=oldest&kind=task")
	if oldest.Stats != first.Stats || oldest.Total != 51 || oldest.Work[0].ID != second.Work[0].ID {
		t.Fatal("oldest sort must apply before pagination", oldest)
	}
	_, shortID, _ := strings.Cut(oldest.Work[0].ID, "_")
	if match := read("?q=%23" + shortID[:7]); match.Total != 1 || match.Work[0].ID != oldest.Work[0].ID {
		t.Fatal("short ID lookup", match)
	}
	if updated := read("?state=closed&sort=updated"); updated.Work[0].ID != firstTask.ID || read("?state=closed").Work[0].Kind != "graph" {
		t.Fatal("recently updated must use the last edit time", updated)
	}
	if graphs := read("?state=closed&kind=graph"); graphs.Total != 1 || graphs.Work[0].Kind != "graph" || graphs.Stats != first.Stats {
		t.Fatal("closed graph filter", graphs)
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
	labelsURL := "/api/projects/" + project.ID + "/overview/labels"
	edit := func(action string, ids ...string) int {
		body, _ := json.Marshal(map[string]any{"action": action, "ids": ids, "label": "Design"})
		return request(s, "POST", labelsURL, string(body), false).Code
	}
	foreign, err := e.Desk.Work(other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := edit("add", first.Work[0].ID, foreign.Tasks[0].ID); got != 404 || read("?q=design").Total != 0 {
		t.Fatal("cross-project edit must reject the entire selection", got)
	}
	for range 2 {
		if got := edit("add", first.Work[0].ID, second.Work[0].ID); got != 200 {
			t.Fatal("add", got)
		}
	}
	matches := read("?q=design")
	if matches.Total != 2 || len(matches.Work[0].Labels) != 1 || matches.Work[0].Labels[0] != "Design" || matches.Stats != first.Stats {
		t.Fatal("bulk label search", matches)
	}
	if got := edit("remove", first.Work[0].ID); got != 200 || read("?q=design").Total != 1 {
		t.Fatal("remove", got)
	}
	if row := first.Work[0]; row.AttemptMS == nil || *row.AttemptMS != 0 || row.CostUSD != nil || row.Workspace != project.Directory {
		t.Fatal("unstarted assignment metadata", row)
	}
	if out := read("?q=Other"); out.Total != 0 {
		t.Fatal("other project leaked")
	}
	if out := read("?offset=9999"); len(out.Work) != 0 {
		t.Fatal("offset was not bounded")
	}
	// Journaled usage evidence supplies the project cost total: a priced run
	// makes stats.cost_usd available, and unpriced usage is counted separately.
	usageRun, err := e.Store.Create(studio.Brief{Title: "Priced attempt", Intent: "Work", Workspace: e.Root, AppID: "workspace-inventory", ProjectID: project.ID, TimeoutMinutes: 1}, "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	tracker := harness.NewUsageTracker("gpt-6.2-sol")
	tracker.Observe(harness.Usage{InputTokens: 1_000_000, OutputTokens: 250_000})
	if err = e.Store.RecordAgentUsage(usageRun.ID, "mitos.engineer", "Engineer", "codex", tracker); err != nil {
		t.Fatal(err)
	}
	withCost := read("")
	if withCost.Stats.CostUSD == nil || *withCost.Stats.CostUSD != 4.50 || withCost.Stats.UnpricedRuns != 0 {
		t.Fatalf("project cost total missing: %+v", withCost.Stats)
	}
	unpricedRun, err := e.Store.Create(studio.Brief{Title: "Unpriced attempt", Intent: "Work", Workspace: e.Root, AppID: "workspace-inventory", ProjectID: project.ID, TimeoutMinutes: 1}, "Inventory", "", false)
	if err != nil {
		t.Fatal(err)
	}
	unknown := harness.NewUsageTracker("unpriced-model")
	unknown.Observe(harness.Usage{InputTokens: 10, OutputTokens: 10})
	if err = e.Store.RecordAgentUsage(unpricedRun.ID, "mitos.engineer", "Engineer", "codex", unknown); err != nil {
		t.Fatal(err)
	}
	mixed := read("")
	if mixed.Stats.CostUSD == nil || *mixed.Stats.CostUSD != 4.50 || mixed.Stats.UnpricedRuns != 1 {
		t.Fatalf("unpriced usage must not inflate the total: %+v", mixed.Stats)
	}
	for _, query := range []string{"?sort=random", "?kind=unknown", "?state=unknown", "?offset=-1", "?offset=nope"} {
		if got := request(s, "GET", "/api/projects/"+project.ID+"/overview"+query, "", false); got.Code != 400 {
			t.Fatal(query, got.Code)
		}
	}
	if got := request(s, "GET", "/api/projects/missing/overview", "", false); got.Code != 404 {
		t.Fatal("missing project", got.Code)
	}
}
