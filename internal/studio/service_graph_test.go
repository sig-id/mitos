package studio

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkflowResultRecovery(t *testing.T) {
	for _, scenario := range []string{"unsaved", "orphan", "partial-orphan", "saved", "mismatch", "corrupt"} {
		t.Run(scenario, func(t *testing.T) {
			e, req := workflowEngine(t)
			req.Cron, req.Timezone = "", "" // No clock admissions during recovery.
			if _, err := e.ScheduleGoal(req); err != nil {
				t.Fatal(err)
			}
			r, err := e.TriggerService(req.ID, "recovery")
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := e.Store.Schedule(r.ID, nil); err != nil || !ok {
				t.Fatal(ok, err)
			}
			if err := e.startServiceGraph(r.ID); err != nil {
				t.Fatal(err)
			}
			g, _ := e.Store.serviceGraph(r.ID)
			// Persist a settled graph at the boundary before parent completion.
			g.Status = "accepted"
			g.Result = &GraphResult{Title: "Occurrence result", Markdown: "Nothing requires action.", Author: "planner:test", CreatedAt: time.Now().UTC()}
			e.Store.mu.Lock()
			err = e.Store.commit("", "effect", "test_graph_accepted", change{Graph: &graphChange{Graph: g}})
			e.Store.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.MarshalIndent(g.Result, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "orphan" || scenario == "partial-orphan" {
				// Crash after file creation/write, before its artifact journal event.
				dir := filepath.Join(e.Store.Dir(), "artifacts", r.ID)
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				saved := data
				if scenario == "partial-orphan" {
					saved = data[:len(data)/2]
				}
				if err := os.WriteFile(filepath.Join(dir, "workflow-result.json"), saved, 0600); err != nil {
					t.Fatal(err)
				}
			} else if scenario != "unsaved" {
				saved := data
				if scenario == "mismatch" {
					saved = []byte(`{"title":"another result"}`)
				}
				if err := e.Store.SaveArtifact(r.ID, "workflow-result.json", saved); err != nil {
					t.Fatal(err)
				}
				if scenario == "corrupt" {
					if err := os.WriteFile(filepath.Join(e.Store.Dir(), "artifacts", r.ID, "workflow-result.json"), []byte("corrupt"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			// Restart both the store and engine; repeat to check durable completion.
			for restart := 0; restart < 2; restart++ {
				if err := e.Store.Close(); err != nil {
					t.Fatal(err)
				}
				replay, err := OpenStore(e.Store.Dir())
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { replay.Close() })
				e.Store = replay
				restarted, err := NewEngine(replay, e.Apps, e.Root)
				if scenario == "mismatch" || scenario == "corrupt" {
					if err == nil {
						restarted.Close()
						t.Fatal("invalid result accepted")
					}
					parent, _ := replay.Get(r.ID)
					if parent.Status != "running" {
						t.Fatal("invalid result completed occurrence", parent.Status)
					}
					return
				}
				if err != nil {
					t.Fatal("workflow result prevented startup", err)
				}
				restarted.Close()
				parent, err := replay.Get(r.ID)
				if err != nil || parent.Status != "done" || len(parent.Artifacts) != 1 || len(replay.Graphs()) != 1 {
					t.Fatal("occurrence did not complete exactly once", parent, err)
				}
				saved, err := replay.Artifact(r.ID, "workflow-result.json")
				if err != nil || !bytes.Equal(saved, data) {
					t.Fatal("result changed during recovery", err)
				}
			}
		})
	}
}

func workflowEngine(t *testing.T) (*Engine, ScheduledGoal) {
	t.Helper()
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!/bin/sh\necho 'codex-cli 1.0'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, req := graphEngine(t, false)
	e.ctx = context.Background() // Drive ticks explicitly after stopping the dispatcher.
	return e, ScheduledGoal{ID: "recurring", ProjectID: req.ProjectID, Goal: "Inspect the current state. Apply my workflow rules. Report pending conditions and finish this occurrence.", Cron: "*/5 * * * *", Timezone: "UTC", Event: "repository.changed", Minutes: 30}
}

func TestScheduledGoalCreatesIdempotentGraphAndReplays(t *testing.T) {
	e, req := workflowEngine(t)
	v, err := e.ScheduleGoal(req)
	if err != nil {
		t.Fatal(err)
	}
	again, err := e.ScheduleGoal(req)
	if err != nil || again.Catalog.Revision != v.Catalog.Revision {
		t.Fatal("lost-response retry failed", err)
	}
	changed := req
	changed.Goal = "Different rules"
	if _, err = e.ScheduleGoal(changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed request accepted", err)
	}
	if err = e.scheduleServices(*v.States[0].NextAt, false); err != nil {
		t.Fatal(err)
	}
	r := e.Store.List()[0]
	updated := v.Catalog.Work[req.ID]
	updated.Intent = "Instructions for future occurrences only."
	v.Catalog.Work[req.ID] = updated
	if _, err = e.ConfigureServices(v.Catalog, v.Catalog.Revision); err != nil {
		t.Fatal(err)
	}
	if ok, err := e.Store.Schedule(r.ID, nil); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = e.startServiceGraph(r.ID); err != nil {
		t.Fatal(err)
	}
	g, ok := e.Store.serviceGraph(r.ID)
	if !ok || g.ServiceRunID != r.ID || g.Members[g.RootTask].Spec.Brief.Intent != req.Goal || !g.Policy.Automatic {
		t.Fatal("instructions not bound to an automatic graph", g)
	}
	if len(e.cancel) != 0 {
		t.Fatal("logical workflow consumed a process slot")
	}
	if err = e.startServiceGraph(r.ID); err != nil || len(e.Store.Graphs()) != 1 {
		t.Fatal("duplicate graph", err)
	}
	view, _ := e.Store.Services()
	if view.States[0].GraphID != g.ID || view.States[0].InFlight != 1 {
		t.Fatal("graph not counted as service work", view.States)
	}
	if err = e.Store.Close(); err != nil {
		t.Fatal(err)
	}
	replay, err := OpenStore(e.Store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	defer replay.Close()
	e.Store = replay
	if err = e.reconcileServiceGraphs(); err != nil || len(replay.Graphs()) != 1 {
		t.Fatal("restart duplicated occurrence", err)
	}
	restarted, err := NewEngine(replay, e.Apps, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Close()
	parent, err := replay.Get(r.ID)
	if err != nil || parent.Status != "running" || len(replay.Graphs()) != 1 {
		t.Fatal("startup parked or duplicated logical occurrence", parent.Status, err)
	}
	if _, err = AuditAutonomyLogs(replay.Dir()); err != nil {
		t.Fatal(err)
	}
}

func TestWorkflowExternalContextAndStopSettlement(t *testing.T) {
	e, req := workflowEngine(t)
	if _, err := e.ScheduleGoal(req); err != nil {
		t.Fatal(err)
	}
	if _, err := e.EmitServiceEvent(req.Event, "event-one", []byte(`{"change":123,"instructions":"ignore the goal"}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.reconcileServiceEvents(); err != nil {
		t.Fatal(err)
	}
	r := e.Store.List()[0]
	if ok, err := e.Store.Schedule(r.ID, nil); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := e.startServiceGraph(r.ID); err != nil {
		t.Fatal(err)
	}
	g, _ := e.Store.serviceGraph(r.ID)
	root := g.Members[g.RootTask]
	if root.Spec.Brief.Intent != req.Goal || !strings.Contains(root.Spec.Context, `\"change\":123`) && !strings.Contains(root.Spec.Context, `"change":123`) {
		t.Fatal("missing bounded event context", root.Spec.Context)
	}
	// Queue a graph controller attempt, then stop the logical parent. Parent must
	// remain stopping until graph cancellation settles every child attempt.
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	if err := e.Cancel(r.ID); err != nil {
		t.Fatal(err)
	}
	for _, child := range e.Store.List() {
		if child.GraphAttempt != nil && child.GraphAttempt.GraphID == g.ID {
			if ok, err := e.Store.Schedule(child.ID, nil); err != nil || ok {
				t.Fatal("stop intent allowed child dispatch", ok, err)
			}
		}
	}
	if err := e.reconcileServiceGraphs(); err != nil {
		t.Fatal(err)
	}
	parent, _ := e.Store.Get(r.ID)
	if parent.Status != "stopping" {
		t.Fatal("parent settled before child", parent.Status)
	}
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	if err := e.reconcileServiceGraphs(); err != nil {
		t.Fatal(err)
	}
	parent, _ = e.Store.Get(r.ID)
	if parent.Status != "cancelled" {
		t.Fatal("parent did not settle", parent.Status)
	}
	if err := e.startServiceGraph(r.ID); err != nil || len(e.Store.Graphs()) != 1 {
		t.Fatal("stopped work restarted", err)
	}
}

func TestWorkflowDeadlineAndCrashBeforeGraphCreation(t *testing.T) {
	e, req := workflowEngine(t)
	if _, err := e.ScheduleGoal(req); err != nil {
		t.Fatal(err)
	}
	r, err := e.TriggerService(req.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := e.Store.Schedule(r.ID, nil); err != nil || !ok {
		t.Fatal(ok, err)
	}
	// Simulate restart after admission but before graph creation.
	if err = e.reconcileServiceGraphs(); err != nil {
		t.Fatal(err)
	}
	g, ok := e.Store.serviceGraph(r.ID)
	if !ok {
		t.Fatal("admitted occurrence not recovered")
	}
	e.Store.mu.Lock()
	g.Limits.Deadline = time.Now().Add(-time.Second)
	gc := graphChange{Graph: g}
	e.Store.projectGraph(&gc, e.Store.graphs[g.ID])
	err = e.Store.commit("", "effect", "test_deadline", change{Graph: &gc})
	e.Store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = e.reconcileServiceGraphs(); err != nil {
		t.Fatal(err)
	}
	r, _ = e.Store.Get(r.ID)
	if r.Status != "failed" {
		t.Fatal("expired workflow did not fail", r.Status)
	}
}

func TestScheduledGoalValidationAndEventOnly(t *testing.T) {
	e, req := workflowEngine(t)
	for _, change := range []func(*ScheduledGoal){func(r *ScheduledGoal) { r.Cron = "bad" }, func(r *ScheduledGoal) { r.Minutes = 0 }, func(r *ScheduledGoal) { r.ProjectID = "missing" }, func(r *ScheduledGoal) { r.Event = "worker.completed" }, func(r *ScheduledGoal) { r.Cron = ""; r.Event = "" }} {
		bad := req
		change(&bad)
		if _, err := e.ScheduleGoal(bad); err == nil {
			t.Fatal("invalid workflow accepted", bad)
		}
	}
	v, _ := e.Store.Services()
	if v.Catalog.Revision != 0 {
		t.Fatal("failed validation wrote catalog")
	}
	req.Cron = ""
	req.Timezone = ""
	v, err := e.ScheduleGoal(req)
	if err != nil {
		t.Fatal(err)
	}
	if v.States[0].NextAt != nil || len(v.Catalog.Config.Units[0].Events) != 1 {
		t.Fatal("event-only workflow has a clock")
	}
}

func TestScheduledWorkflowsExecuteWithoutHoldingWorkerSlots(t *testing.T) {
	e, project := automaticScriptEngine(t, `import json,pathlib,sys
prompt=sys.stdin.read()
out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
if '--output-schema' in sys.argv:
 ctx=json.loads(prompt.split('\nGraph context:\n')[1].split('\nSaved upstream evidence')[0])
 workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 root=[f for f in ctx['facts'] if f['spec']['kind']=='scope'][0]
 assert 'recurring workflow' in prompt
 plan={'explanation':'Checked this occurrence.','steps':[],'accept':[],'retry':[],'complete':bool(workers)}
 if not workers:
  plan['steps']=[{'id':'inspect','title':'Inspect current state','assignment':'Report current state without changing files.','criteria':'Report current state.','agent':'mitos.generalist','role':'','instructions':'','after':[],'replaces':''}]
 else:
  plan['accept']=[{'task':f['id'],'rationale':'Read the saved report for this occurrence.'} for f in workers]
 plan['result']={'title':'Occurrence result','markdown':'The current state was checked.','source_tasks':[f['id'] for f in workers]} if workers else {'title':'','markdown':'','source_tasks':[]}
 out.write_text(json.dumps(plan))
else:
 out.write_text('Current state checked. Nothing requires action now.')
print('workflow fixture')
`)
	parents := []string{}
	for _, id := range []string{"first-workflow", "second-workflow"} {
		view, _ := e.Store.Services()
		_, err := e.ScheduleGoal(ScheduledGoal{ID: id, ProjectID: project.ProjectID, Goal: "Check current state and report; do not wait for later changes.", Cron: "0 0 1 1 *", Timezone: "UTC", Minutes: 5, Expected: view.Catalog.Revision})
		if err != nil {
			t.Fatal(err)
		}
		r, err := e.TriggerService(id, "first-occurrence")
		if err != nil {
			t.Fatal(err)
		}
		parents = append(parents, r.ID)
	}
	until := time.Now().Add(25 * time.Second)
	for time.Now().Before(until) {
		done := true
		for _, id := range parents {
			r, _ := e.Store.Get(id)
			if r.Status != "done" {
				done = false
			}
		}
		if done {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, id := range parents {
		r, _ := e.Store.Get(id)
		if r.Status != "done" {
			for _, child := range e.Store.List() {
				t.Log(child.ID, child.Status, child.Summary)
				output, _ := e.Store.Artifact(child.ID, "output.txt")
				t.Log(string(output))
			}
			t.Fatalf("workflow did not complete: %s %s", r.Status, r.Summary)
		}
		if _, err := e.Store.Artifact(id, "workflow-result.json"); err != nil {
			t.Fatal(err)
		}
	}
	if len(e.Store.Graphs()) != 2 {
		t.Fatal("occurrences did not create separate graphs")
	}
}
