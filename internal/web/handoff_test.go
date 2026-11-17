package web

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/acceptance"
	"mitos/internal/decision"
	"mitos/internal/studio"
)

func TestHandoffHTTPUsesBoundIdentityAndFrontendProjection(t *testing.T) {
	server, e := testServer(t)
	project, err := e.Desk.CreateProject("Handoff", e.Root)
	if err != nil {
		t.Fatal(err)
	}
	e.Apps = append(e.Apps, studio.Application{ID: "handoff-fixture", Name: "Handoff fixture", Acceptance: acceptance.Spec{Cmd: []string{"/bin/true"}}, Command: []string{"/bin/sh", "-c", "sleep 30"}})
	g, err := e.CreateGraph(studio.CreateGraph{Profile: studio.GraphProfile, Key: "handoff-http", ProjectID: project.ID, GoalRevision: 1, Activation: "manual", Policy: studio.GraphPolicy{Mode: "defined", Applications: []string{"handoff-fixture"}}, Limits: studio.GraphLimits{Tasks: 4, Amendments: 4, Attempts: 4, Expansions: 2, OrchestratorCalls: 2, Depth: 3, Fanout: 3, Deadline: time.Now().Add(time.Minute)}, Tasks: []studio.GraphSpec{{ID: "root", Kind: "scope", Required: true, Criteria: "Evidence", Acceptance: "operator"}, {ID: "worker", Parent: "root", Kind: "application", Required: true, Criteria: "Evidence", Acceptance: "operator", Brief: studio.Brief{Title: "Record evidence", AppID: "handoff-fixture", TimeoutMinutes: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	var run studio.Run
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		g, _ = e.Store.Graph(g.ID)
		for _, m := range g.Members {
			if m.Spec.Kind == "application" {
				run, _ = e.Store.Get(m.CurrentRunID)
			}
		}
		if run.Status == "running" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.Status != "running" {
		t.Fatal("worker did not start", run.Status)
	}
	defer e.Cancel(run.ID)
	session, token, err := e.Store.CreateBoundAgentSession("codex", "Fixture", e.Root, project.ID, "", run.ID, decision.LocalOperator())
	if err != nil {
		t.Fatal(err)
	}
	call := func(method string, input any, secret string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(input)
		r := versioned(httptest.NewRequest("POST", "http://localhost/api/agent-sessions/"+session.ID+"/handoff/"+method, strings.NewReader(string(raw))))
		r.Header.Set("Authorization", "Bearer "+secret)
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w
	}
	if err = os.WriteFile(filepath.Join(e.Root, "report.md"), []byte("HTTP evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	response := call("artifact_register", studio.ArtifactRegister{Path: "report.md", Role: "report"}, token)
	var artifact studio.ArtifactDescriptor
	if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &artifact) != nil {
		t.Fatal(response.Code, response.Body.String())
	}
	in := studio.ResultSubmission{Version: studio.HandoffVersion, Key: "result", Summary: "HTTP evidence saved", Artifacts: []studio.ResultArtifact{{ArtifactID: artifact.ID, Role: "report"}}}
	first := call("result_submit", in, token)
	again := call("result_submit", in, token)
	if first.Code != 200 || first.Body.String() != again.Body.String() {
		t.Fatal(first.Body.String(), again.Body.String())
	}
	forged := call("artifact_read", studio.ArtifactRead{RunID: run.ID, ArtifactID: artifact.ID, Limit: 32}, "forged")
	if forged.Code != 401 {
		t.Fatal("forged bearer must return 401", forged.Code)
	}
	raw, _ := json.Marshal(studio.ArtifactRead{RunID: run.ID, ArtifactID: artifact.ID, Limit: 32})
	front := request(server, "POST", "/api/projects/"+project.ID+"/runs/"+run.ID+"/evidence/artifact_read", string(raw), false)
	var data studio.ArtifactRange
	if front.Code != 200 || json.Unmarshal(front.Body.Bytes(), &data) != nil || string(data.Data) != "HTTP evidence" || !data.Complete {
		t.Fatal(front.Code, front.Body.String())
	}
	wrong := request(server, "POST", "/api/projects/wrong/runs/"+run.ID+"/evidence/artifact_read", string(raw), false)
	if wrong.Code == 200 {
		t.Fatal("wrong project")
	}
	snapshot := request(server, "GET", "/api/graphs/"+g.ID+"/nodes/"+run.TaskID+"/runs/"+run.ID, "", false)
	if snapshot.Code != 200 || !strings.Contains(snapshot.Body.String(), `"result"`) {
		t.Fatal(snapshot.Body.String())
	}
	for _, path := range []string{"/api/graphs?project_id=" + project.ID, "/api/handoff", "/static/handoff.js"} {
		if response := request(server, "GET", path, "", false); response.Code != 200 {
			t.Fatal(path, response.Body.String())
		}
	}
	// Full identities in a tool payload cannot override the leased run binding.
	if forged := call("result_submit", map[string]any{"version": studio.HandoffVersion, "run_id": "other"}, token); forged.Code == 200 {
		t.Fatal("forged origin field accepted")
	}
}
