package studio

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"mitos/internal/fleet"
	"mitos/internal/workpool"
)

func resumeRequest(g WorkGraph) ResumeGraph {
	limits := g.Limits
	limits.Deadline = time.Now().UTC().Add(2 * time.Hour)
	limits.Attempts += 8
	limits.OrchestratorCalls += 4
	limits.Retries += 1
	return ResumeGraph{Key: "resume", Revision: g.Revision, Limits: limits, AttemptMinutes: 60}
}

func stoppedGraph(t *testing.T) (*Engine, WorkGraph, string, string) {
	t.Helper()
	e, req := graphEngine(t, false)
	req.Policy.Mode = "defined"
	a, b := graphWorker("finished", "goal"), graphWorker("stopped", "goal")
	b.Brief.Scheduling.Deps = []workpool.Dependency{{From: "finished", Guard: "done", Payload: "control"}}
	req.Tasks = append(req.Tasks, a, b)
	g := createGraphTest(t, e, req)
	var finished, stopped string
	for id, m := range g.Members {
		if m.Spec.Kind == "application" {
			if len(m.Spec.Brief.Scheduling.Deps) == 0 {
				finished = id
			} else {
				stopped = id
			}
		}
	}
	gc := graphChange{Graph: g}
	for _, id := range []string{finished, stopped} {
		m := gc.Graph.Members[id]
		r := Run{ID: newID("local"), TaskID: id, ProjectID: g.ProjectID, Status: "failed", Brief: m.Spec.Brief, GraphAttempt: &GraphAttempt{GraphID: g.ID, Executor: *m.Executor}, CreatedAt: time.Now().UTC()}
		m.CurrentRunID = r.ID
		m.Attempts = []string{r.ID}
		m.State = "failed"
		if id == finished {
			r.Status = "done"
			m.State = "accepted"
			m.Accepted = &GraphAcceptance{RunID: r.ID, SpecRevision: m.Revision}
		}
		gc.Runs = append(gc.Runs, r)
	}
	gc.Graph.Attempts = 2
	gc.Graph.Limits.Deadline = time.Now().UTC().Add(-time.Minute)
	e.Store.mu.Lock()
	e.Store.projectGraph(&gc, &g)
	err := e.Store.commit("", "effect", "test_stopped_graph", change{Graph: &gc})
	e.Store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return e, gc.Graph, finished, stopped
}

func TestResumeGraphPreservesIdentityEvidenceAndUsage(t *testing.T) {
	e, g, finished, stopped := stoppedGraph(t)
	req := resumeRequest(g)
	next, err := e.ResumeGraph(g.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != g.ID || next.RootTask != g.RootTask || len(next.Members) != len(g.Members) || next.Attempts != g.Attempts || next.OrchestratorCalls != g.OrchestratorCalls {
		t.Fatal("workflow or accounting replaced")
	}
	if !reflect.DeepEqual(next.Members[finished], g.Members[finished]) {
		t.Fatal("accepted work changed")
	}
	old, m := g.Members[stopped], next.Members[stopped]
	if m.ContinueFrom != old.CurrentRunID || m.CurrentRunID != "" || m.State != "queued" || !reflect.DeepEqual(m.Attempts, old.Attempts) || !reflect.DeepEqual(m.Spec.Brief.Scheduling.Deps, old.Spec.Brief.Scheduling.Deps) || !reflect.DeepEqual(m.Executor, old.Executor) {
		t.Fatal("lost agent, dependencies, or lineage")
	}
	if next.PolicyRevision != g.PolicyRevision+1 || next.OrchestratorGeneration != g.OrchestratorGeneration+1 {
		t.Fatal("old orchestrator authority retained")
	}
	if err = e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	queued, _ := e.Store.Graph(g.ID)
	r, _ := e.Store.Get(queued.Members[stopped].CurrentRunID)
	if r.ParentID != old.CurrentRunID || r.GraphAttempt.ContinueFrom != old.CurrentRunID || queued.Attempts != 3 || r.Brief.TimeoutMinutes != 60 || !r.Brief.Scheduling.Deadline.Equal(req.Limits.Deadline) {
		t.Fatalf("wrong continuation: %+v", r)
	}
	again, err := e.ResumeGraph(g.ID, req)
	if err != nil || again.Attempts != 3 {
		t.Fatal("duplicate request launched more work", err)
	}
	path := e.Store.Dir()
	if err = e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	replayed, _ := reopened.Graph(g.ID)
	if !reflect.DeepEqual(again, replayed) {
		t.Fatal("continuation did not survive replay")
	}
}

func TestResumeGraphRejectsUnsafeOrStaleRequests(t *testing.T) {
	for _, test := range []string{"stale", "reduced", "expired", "used-up", "running", "claims", "cancelled", "accepted", "service", "retry-limit", "invalid-time"} {
		t.Run(test, func(t *testing.T) {
			e, g, _, stopped := stoppedGraph(t)
			req := resumeRequest(g)
			e.Store.mu.Lock()
			live := e.Store.graphs[g.ID]
			r := e.Store.runs[live.Members[stopped].CurrentRunID]
			switch test {
			case "stale":
				req.Revision--
			case "reduced":
				req.Limits.Tasks--
			case "expired":
				req.Limits.Deadline = time.Now().Add(-time.Second)
			case "used-up":
				live.Attempts = live.Limits.Attempts
				req.Limits.Attempts = live.Attempts
			case "running":
				r.Status = "running"
			case "claims":
				r.Claim = &fleet.Claim{}
			case "cancelled":
				live.Status = "cancelled"
			case "accepted":
				live.Status = "accepted"
			case "service":
				live.ServiceRunID = "parent"
			case "retry-limit":
				live.Limits.Retries = 0
				req.Limits.Retries = 0
			case "invalid-time":
				req.AttemptMinutes = 121
			}
			before := graphClone(*live)
			e.Store.mu.Unlock()
			if _, err := e.ResumeGraph(g.ID, req); err == nil {
				t.Fatal("invalid resume accepted")
			}
			after, _ := e.Store.Graph(g.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("rejected resume partially mutated graph")
			}
		})
	}
	e, g, _, _ := stoppedGraph(t)
	req := resumeRequest(g)
	if _, err := e.ResumeGraph(g.ID, req); err != nil {
		t.Fatal(err)
	}
	req.Limits.Attempts++
	if _, err := e.ResumeGraph(g.ID, req); !errors.Is(err, ErrConflict) {
		t.Fatal("request key reused for different resource grant")
	}
}

func TestResumeUntouchedGoalStartsInPlaceWithChosenLimits(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, create := graphEngine(t, false)
	g := createGraphTest(t, e, create)
	req := resumeRequest(g)
	req.StartAutomatic = true
	next, err := e.ResumeGraph(g.ID, req)
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != g.ID || next.RootTask != g.RootTask || len(next.Members) != 1 || !next.Policy.Automatic || next.Policy.OrchestratorApplication == "" || next.Setup == nil || !reflect.DeepEqual(next.Limits, req.Limits) {
		t.Fatal("untouched goal was recreated or limits reset")
	}
	replay, err := e.ResumeGraph(g.ID, req)
	if err != nil || replay.Revision != next.Revision {
		t.Fatal("automatic start was not idempotent", err)
	}
	if err = e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	scheduled, _ := e.Store.Graph(g.ID)
	if len(scheduled.Members) != 2 || scheduled.Attempts != 1 {
		t.Fatal("orchestrator was not scheduled")
	}
}
