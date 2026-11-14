package studio

import (
	"reflect"
	"testing"

	"mitos/internal/workpool"
)

func advanceInboxGeneration(t *testing.T, e *Engine, g WorkGraph, action string) WorkGraph {
	t.Helper()
	if action == "resume" {
		next, err := e.ResumeGraph(g.ID, resumeRequest(g))
		if err != nil {
			t.Fatal(err)
		}
		return next
	}
	next, _ := amendGraphTest(t, e, g, "replace", GraphOperation{Kind: "replace_orchestrator", Rationale: "Replace orchestration while waiting for a signal"})
	return next
}

func TestGraphInboxGenerationKeepsPendingJoinsRunnable(t *testing.T) {
	for _, action := range []string{"resume", "replace"} {
		for _, received := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "/waiting", true: "/partial-join"}[received], func(t *testing.T) {
				e, req := graphEngine(t, false)
				g := createGraphTest(t, e, req)
				spec := graphWorker("consumer", g.RootTask)
				first := signalDependency(g)
				first.Payload = "control"
				second := graphClone(first)
				second.From, second.Inbox.Correlation = "second", "second-release"
				other := graphClone(first)
				other.From, other.Strength, other.Inbox.Mailbox = "other", "soft", "another-mailbox"
				explicit := graphClone(first)
				explicit.From, explicit.Strength, explicit.Inbox.Generation = "explicit", "soft", 0
				spec.Brief.Scheduling.Deps = []workpool.Dependency{first, second, other, explicit}
				g, receipt := amendGraphTest(t, e, g, "wait", GraphOperation{Kind: "add", Spec: &spec})
				id := receipt.IDs["consumer"]
				before := graphClone(g.Members[id])
				if received {
					if receipt, err := e.Store.AcceptMessage(graphSignal(t, e.Store, g, "first-before")); err != nil || receipt.Status != "accepted" {
						t.Fatal(receipt, err)
					}
				}
				next := advanceInboxGeneration(t, e, g, action)
				want := graphClone(before.Spec.Brief.Scheduling.Deps)
				if !received {
					want[0].Inbox.Generation = int(next.OrchestratorGeneration)
				}
				want[1].Inbox.Generation = int(next.OrchestratorGeneration)
				if !reflect.DeepEqual(next.Members[id].Spec.Brief.Scheduling.Deps, want) {
					t.Fatal("generation change lost a received message, changed filters/cursors, or changed another mailbox")
				}
				if next.Members[id].Revision <= before.Revision || !reflect.DeepEqual(next.Members[id].History[0], before.Spec) {
					t.Fatal("selector change did not retain specification history")
				}
				task, _ := e.Store.Task(id)
				if !reflect.DeepEqual(task.Scheduling.Deps, want) {
					t.Fatal("task projection retained old selectors")
				}
				// Recover before delivery: the new selectors must be durable too.
				path := e.Store.Dir()
				if err := e.Store.Close(); err != nil {
					t.Fatal(err)
				}
				reopened, err := OpenStore(path)
				if err != nil {
					t.Fatal(err)
				}
				defer reopened.Close()
				e.Store = reopened
				if receipt, err := e.Store.AcceptMessage(graphSignal(t, e.Store, g, "stale")); err != nil || receipt.Status != "rejected" {
					t.Fatal("old-generation admission was not fenced", receipt, err)
				}
				if !received {
					if receipt, err := e.Store.AcceptMessage(graphSignal(t, e.Store, next, "first-after")); err != nil || receipt.Status != "accepted" {
						t.Fatal(receipt, err)
					}
				}
				message := graphSignal(t, e.Store, next, "second-after")
				message.Correlation = "second-release"
				if receipt, err := e.Store.AcceptMessage(message); err != nil || receipt.Status != "accepted" {
					t.Fatal(receipt, err)
				}
				if err := e.reconcileGraphs(); err != nil {
					t.Fatal(err)
				}
				current, _ := e.Store.Graph(g.ID)
				rid := current.Members[id].CurrentRunID
				if rid == "" {
					t.Fatal("waiting inbox join could not become runnable after generation change")
				}
				if ok, err := e.Store.Schedule(rid, nil); err != nil || !ok {
					t.Fatal("dispatch after generation change", ok, err)
				}
				task, _ = e.Store.Task(id)
				if len(task.DependencyEvidence) != 2 {
					t.Fatal("join did not bind both messages")
				}
				if received && task.DependencyEvidence[0].Inbox.Key != "remote/first-before" {
					t.Fatal("partial join lost the previously admitted message")
				}
			})
		}
	}
}

func TestGraphInboxGenerationPreservesFrozenAttempts(t *testing.T) {
	for _, action := range []string{"resume", "replace"} {
		for _, admitted := range []bool{false, true} {
			t.Run(action+map[bool]string{false: "/queued", true: "/admitted"}[admitted], func(t *testing.T) {
				e, req := graphEngine(t, false)
				app := executionApp(t, "printf result > result.txt")
				e.Apps = append(e.Apps, app)
				req.Policy.Applications = append(req.Policy.Applications, app.ID)
				g := createGraphTest(t, e, req)
				spec := graphWorker("consumer", g.RootTask)
				spec.Brief.AppID = app.ID
				dep := signalDependency(g)
				optional := graphClone(dep)
				optional.From, optional.Strength, optional.Inbox.Correlation = "optional", "soft", "later"
				spec.Brief.Scheduling.Deps = []workpool.Dependency{dep, optional}
				g, receipt := amendGraphTest(t, e, g, "wait", GraphOperation{Kind: "add", Spec: &spec})
				id := receipt.IDs["consumer"]
				if _, err := e.Store.AcceptMessage(graphSignal(t, e.Store, g, "bound")); err != nil {
					t.Fatal(err)
				}
				if err := e.reconcileGraphs(); err != nil {
					t.Fatal(err)
				}
				g, _ = e.Store.Graph(g.ID)
				rid := g.Members[id].CurrentRunID
				if admitted {
					if ok, err := e.Store.Schedule(rid, nil); err != nil || !ok {
						t.Fatal(ok, err)
					}
				}
				if action == "resume" {
					if err := e.Store.Complete(rid, "Candidate evidence ready", false); err != nil {
						t.Fatal(err)
					}
				}
				g, _ = e.Store.Graph(g.ID)
				before, _ := e.Store.Task(id)
				runBefore, _ := e.Store.Get(rid)
				next := advanceInboxGeneration(t, e, g, action)
				after, _ := e.Store.Task(id)
				runAfter, _ := e.Store.Get(rid)
				if !reflect.DeepEqual(next.Members[id].Spec, g.Members[id].Spec) || !reflect.DeepEqual(before.DependencyEvidence, after.DependencyEvidence) || !reflect.DeepEqual(runBefore.GraphAttempt, runAfter.GraphAttempt) {
					t.Fatal("generation change rewrote a frozen attempt or its evidence")
				}
				if admitted {
					w := after.DependencyEvidence[0]
					if _, err := e.Store.boundInput(w, w.Artifacts[0]); err != nil {
						t.Fatal("bound message became unreadable", err)
					}
				}
			})
		}
	}
}
