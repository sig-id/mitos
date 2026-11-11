package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mitos/internal/studio"
)

func TestGraphAPITransactionsAndDiscovery(t *testing.T) {
	s, e := testServer(t)
	project, err := e.Desk.CreateProject("Graph API", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	req := studio.CreateGraph{Profile: studio.GraphProfile, Key: "api-create", ProjectID: project.ID, GoalRevision: 1, Activation: "manual", Policy: studio.GraphPolicy{Mode: "adaptive", Applications: []string{"workspace-inventory"}}, Limits: studio.GraphLimits{Tasks: 16, Amendments: 16, Attempts: 16, Retries: 1, Expansions: 8, ControllerCalls: 4, Depth: 4, Fanout: 8, Deadline: time.Now().UTC().Add(time.Hour)}, Tasks: []studio.GraphSpec{{ID: "goal", Kind: "scope", Required: true, Criteria: "Explore project", Acceptance: "operator"}}}
	body, _ := json.Marshal(req)
	response := request(s, "POST", "/api/graphs", string(body), false)
	var g studio.WorkGraph
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &g) != nil {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	duplicate := request(s, "POST", "/api/graphs", string(body), false)
	var again studio.WorkGraph
	_ = json.Unmarshal(duplicate.Body.Bytes(), &again)
	if duplicate.Code != 201 || again.ID != g.ID {
		t.Fatal("duplicate graph created")
	}
	p := studio.GraphProposal{Profile: studio.GraphProfile, Key: "api-close", GraphID: g.ID, Revision: g.Revision, PolicyRevision: g.PolicyRevision, Generation: g.ControllerGeneration, Deadline: g.Limits.Deadline, Operations: []studio.GraphOperation{{Kind: "close_expansion", Expansion: "initial", Rationale: "No discovery required"}, {Kind: "seal", Task: g.RootTask, Cursor: g.Cursor}}}
	body, _ = json.Marshal(p)
	preview := request(s, "POST", "/api/graphs/"+g.ID+"/preview", string(body), false)
	if preview.Code != 200 {
		t.Fatal(preview.Body.String())
	}
	unmodified, _ := e.Store.Graph(g.ID)
	if unmodified.Revision != g.Revision {
		t.Fatal("preview mutated graph")
	}
	applied := request(s, "POST", "/api/graphs/"+g.ID+"/amendments", string(body), false)
	if applied.Code != 200 {
		t.Fatal(applied.Body.String())
	}
	p.Key = "stale"
	body, _ = json.Marshal(p)
	if got := request(s, "POST", "/api/graphs/"+g.ID+"/amendments", string(body), false); got.Code != 409 {
		t.Fatal("stale amendment not reported as conflict")
	}
	for _, path := range []string{"/api/graphs", "/api/graphs/" + g.ID, "/api/graphs/" + g.ID + "/blockers", "/api/graphs/capabilities"} {
		if got := request(s, "GET", path, "", false); got.Code != 200 {
			t.Fatalf("%s: %s", path, got.Body.String())
		}
	}
	if got := request(s, "GET", "/api/graphs?limit=0", "", false); got.Code != 400 {
		t.Fatal("invalid read bound")
	}
	capabilities := request(s, "GET", "/api/graphs/capabilities", "", false).Body.String()
	if !strings.Contains(capabilities, `"complete_profile":false`) || !strings.Contains(capabilities, `"interactive_pty_dispatch":false`) {
		t.Fatal("unsupported capabilities advertised")
	}
	if got := request(s, "POST", "/api/graphs", `{"key":"a","key":"b"}`, false); got.Code != 400 {
		t.Fatal("duplicate JSON keys accepted")
	}
	if got := request(s, "POST", "/api/graphs", `{"unknown":"field"}`, false); got.Code != 400 {
		t.Fatal("unknown schema accepted")
	}
}
