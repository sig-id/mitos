package studio

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Queuing the first graph attempt commits a shared graph-level journal event
// with an empty run ID. Both autonomy reducers must keep reporting the mixed
// store instead of aborting, without inventing usage for the shared event.
func amendMark(t *testing.T, e *Engine, g WorkGraph, key string, ops ...GraphOperation) {
	t.Helper()
	if _, _, err := e.AmendGraph(proposal(g, key, ops...)); err != nil {
		t.Fatalf("amend failed: %v", err)
	}
}
func TestAutonomyMetricsProjectSharedGraphEvents(t *testing.T) {
	root := t.TempDir()
	app := supervisedApp(t, root)
	e, s := testEngine(t, root, []Application{app})
	run := launchApp(t, e, root, app)
	run = waitRecovery(t, s, run.ID)
	if run.Status != "done" {
		t.Fatalf("setup: %s %s", run.Status, run.Summary)
	}
	project, err := e.Desk.CreateProject("Metrics graph", root)
	if err != nil {
		t.Fatal(err)
	}
	g := createGraphTest(t, e, CreateGraph{Profile: GraphProfile, Key: "metrics", ProjectID: project.ID, GoalRevision: 1, Activation: "manual", Policy: GraphPolicy{Mode: "adaptive", Applications: []string{app.ID}}, Limits: GraphLimits{Tasks: 8, Amendments: 16, Attempts: 8, Retries: 2, Expansions: 8, OrchestratorCalls: 4, Depth: 4, Fanout: 8, Deadline: time.Now().UTC().Add(time.Hour)}, Tasks: []GraphSpec{{ID: "goal", Kind: "scope", Required: true, Criteria: "Understand the workspace", Acceptance: "operator", Brief: Brief{Title: "Understand the workspace"}}}})
	worker := graphWorker("work", g.RootTask)
	worker.Brief.AppID = app.ID
	amendMark(t, e, g, "add", GraphOperation{Kind: "add", Spec: &worker}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "Initial work discovered"})
	if err = e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	// Compare one durable head. reconcileGraphs starts a real worker; without
	// the writer gate it can append between the live fold and the offline read.
	s.mu.writer.Lock()
	defer s.mu.writer.Unlock()
	live, err := ReportAutonomy(s.List())
	if err != nil {
		t.Fatal(err)
	}
	if live.Runs < 2 || live.Events == 0 {
		t.Fatalf("mixed store lost measurements: %+v", live)
	}
	projected := false
	for _, note := range live.Limitations {
		if strings.Contains(note, "shared graph journal events") {
			projected = true
		}
	}
	if !projected {
		t.Fatalf("graph projection not declared: %+v", live.Limitations)
	}
	offline, err := AuditAutonomyLogs(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(live, offline) {
		t.Fatalf("live and audited metrics differ:\n%+v\n%+v", live, offline)
	}
}

// A genuinely foreign event on a run's journal is still corrupt input.
func TestAutonomyMetricsStillRejectForeignRunEvents(t *testing.T) {
	foreign := Run{ID: "run_a", CreatedAt: time.Unix(1000, 0).UTC(), Events: []Event{{RunID: "run_b", Seq: 1, At: time.Unix(1000, 0).UTC(), Type: "effect", Kind: "run_created", Data: json.RawMessage(`{}`)}}}
	if report, err := ReportAutonomy([]Run{foreign}); err == nil || err.Error() != "mixed run events in metric input" {
		t.Fatalf("foreign events accepted: %v %+v", err, report)
	}
	graph := Run{ID: "run_a", CreatedAt: time.Unix(1000, 0).UTC(), Events: []Event{
		{RunID: "", Seq: 1, At: time.Unix(1000, 0).UTC(), Type: "effect", Kind: "graph_attempts_queued", Data: json.RawMessage(`{"graph":{"id":"graph_x","profile":"mitos.graph/v1","runs":[{"id":"run_other"}]}}`)},
	}}
	if report, err := ReportAutonomy([]Run{graph}); err == nil || err.Error() != "mixed run events in metric input" {
		t.Fatalf("unrelated graph event accepted: %v %+v", err, report)
	}
}
