package web

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"mitos/internal/studio"
)

// Issue #137: an amendment against a work item whose deadline has passed is
// HTTP 410 with the resume remedy in the body, distinct from the 409 of a
// genuine conflict.
func TestGraphAmendmentExpiredDeadlineIs410(t *testing.T) {
	s, e := testServer(t)
	project, err := e.Desk.CreateProject("Expired graph API", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	req := studio.CreateGraph{Profile: studio.GraphProfile, Key: "api-expired", ProjectID: project.ID, GoalRevision: 1, Activation: "manual", Policy: studio.GraphPolicy{Mode: "adaptive", Applications: []string{"workspace-inventory"}}, Limits: studio.GraphLimits{Tasks: 16, Amendments: 16, Attempts: 16, Retries: 1, Expansions: 8, OrchestratorCalls: 4, Depth: 4, Fanout: 8, Deadline: time.Now().UTC().Add(300 * time.Millisecond)}, Tasks: []studio.GraphSpec{{ID: "goal", Kind: "scope", Required: true, Criteria: "Explore project", Acceptance: "operator"}}}
	body, _ := json.Marshal(req)
	response := request(s, "POST", "/api/graphs", string(body), false)
	var g studio.WorkGraph
	if response.Code != 201 || json.Unmarshal(response.Body.Bytes(), &g) != nil {
		t.Fatalf("create: %d %s", response.Code, response.Body.String())
	}
	time.Sleep(400 * time.Millisecond)
	p := studio.GraphProposal{Profile: studio.GraphProfile, Key: "api-late", GraphID: g.ID, Revision: g.Revision, PolicyRevision: g.PolicyRevision, Generation: g.OrchestratorGeneration, Deadline: g.Limits.Deadline, Operations: []studio.GraphOperation{{Kind: "seal", Task: g.RootTask, Cursor: g.Cursor}}}
	body, _ = json.Marshal(p)
	expired := request(s, "POST", "/api/graphs/"+g.ID+"/amendments", string(body), false)
	if expired.Code != 410 {
		t.Fatalf("expired amendment not 410: %d %s", expired.Code, expired.Body.String())
	}
	if !strings.Contains(expired.Body.String(), "resume") {
		t.Fatalf("expiry response lacks the remedy: %s", expired.Body.String())
	}
	// A genuine conflict on a live graph stays 409.
	req.Key = "api-live"
	req.Limits.Deadline = time.Now().UTC().Add(time.Hour)
	body, _ = json.Marshal(req)
	created := request(s, "POST", "/api/graphs", string(body), false)
	var liveGraph studio.WorkGraph
	if created.Code != 201 || json.Unmarshal(created.Body.Bytes(), &liveGraph) != nil {
		t.Fatalf("live create: %d %s", created.Code, created.Body.String())
	}
	stale := studio.GraphProposal{Profile: studio.GraphProfile, Key: "api-stale", GraphID: liveGraph.ID, Revision: liveGraph.Revision + 7, PolicyRevision: liveGraph.PolicyRevision, Generation: liveGraph.OrchestratorGeneration, Deadline: liveGraph.Limits.Deadline, Operations: []studio.GraphOperation{{Kind: "seal", Task: liveGraph.RootTask, Cursor: liveGraph.Cursor}}}
	body, _ = json.Marshal(stale)
	conflict := request(s, "POST", "/api/graphs/"+liveGraph.ID+"/amendments", string(body), false)
	if conflict.Code != 409 {
		t.Fatalf("genuine conflict not 409: %d %s", conflict.Code, conflict.Body.String())
	}
}
