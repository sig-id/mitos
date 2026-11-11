package studio

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"mitos/internal/workpool"
)

func graphEngine(t *testing.T, live bool) (*Engine, CreateGraph) {
	t.Helper()
	s := testStore(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "hello.txt"), []byte("native evidence"), 0600); err != nil {
		t.Fatal(err)
	}
	apps, _ := LoadApps("")
	e, err := NewEngine(s, apps, root)
	if err != nil {
		t.Fatal(err)
	}
	project, err := e.Desk.CreateProject("Graph tests", root)
	if err != nil {
		t.Fatal(err)
	}
	if live {
		t.Cleanup(e.Close)
	} else {
		e.Close()
	}
	req := CreateGraph{Profile: GraphProfile, Key: "initial", ProjectID: project.ID, GoalRevision: 1, Activation: "manual", Policy: GraphPolicy{Mode: "adaptive", Applications: []string{"workspace-inventory"}}, Limits: GraphLimits{Tasks: 32, Amendments: 64, Attempts: 32, Retries: 2, Expansions: 16, ControllerCalls: 8, Depth: 8, Fanout: 16, Deadline: time.Now().UTC().Add(time.Hour)}, Tasks: []GraphSpec{{ID: "goal", Kind: "scope", Required: true, Criteria: "Understand the workspace", Acceptance: "operator", Brief: Brief{Title: "Understand the workspace"}}}}
	return e, req
}
func createGraphTest(t *testing.T, e *Engine, req CreateGraph) WorkGraph {
	t.Helper()
	g, err := e.CreateGraph(req)
	if err != nil {
		t.Fatal(err)
	}
	return g
}
func graphWorker(id, parent string) GraphSpec {
	return GraphSpec{ID: id, Parent: parent, Kind: "application", Required: true, Criteria: "Inventory is independently checked", Acceptance: "application", Brief: Brief{Title: "Inspect workspace", AppID: "workspace-inventory", TimeoutMinutes: 1}}
}
func proposal(g WorkGraph, key string, ops ...GraphOperation) GraphProposal {
	return GraphProposal{Profile: GraphProfile, Key: key, GraphID: g.ID, Revision: g.Revision, PolicyRevision: g.PolicyRevision, Generation: g.ControllerGeneration, Deadline: g.Limits.Deadline, Operations: ops}
}
func amendGraphTest(t *testing.T, e *Engine, g WorkGraph, key string, ops ...GraphOperation) (WorkGraph, GraphReceipt) {
	t.Helper()
	out, r, err := e.AmendGraph(proposal(g, key, ops...))
	if err != nil {
		t.Fatal(err)
	}
	return out, r
}
func TestGraphAtomicCASReplayAndDuplicate(t *testing.T) {
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	spec := graphWorker("inspect", g.RootTask)
	p := proposal(g, "add", GraphOperation{Kind: "add", Spec: &spec}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "Initial work discovered"})
	preview, _, err := e.PreviewGraph(p)
	if err != nil || len(preview.Members) != 2 {
		t.Fatalf("preview: %v", err)
	}
	before, _ := e.Store.Graph(g.ID)
	if !reflect.DeepEqual(before, g) {
		t.Fatal("preview published")
	}
	next, r, err := e.AmendGraph(p)
	if err != nil {
		t.Fatal(err)
	}
	again, rr, err := e.AmendGraph(p)
	if err != nil || !reflect.DeepEqual(r, rr) || !reflect.DeepEqual(again, next) {
		t.Fatal("request replay changed result")
	}
	p.Explanation = "changed"
	if _, _, err = e.AmendGraph(p); !errors.Is(err, ErrConflict) {
		t.Fatal("changed duplicate accepted")
	}
	p.Key = "stale"
	if _, _, err = e.AmendGraph(p); !errors.Is(err, ErrConflict) {
		t.Fatal("stale CAS accepted")
	}
	tasks := e.Store.Tasks()
	if len(tasks) != 2 {
		t.Fatal("graph members not projected as common tasks")
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
	replayed, err := reopened.Graph(g.ID)
	if err != nil || !reflect.DeepEqual(next, replayed) {
		t.Fatal("graph replay diverged")
	}
}
func TestGraphInvalidAmendmentsPublishNothing(t *testing.T) {
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	a := graphWorker("a", g.RootTask)
	b := graphWorker("b", g.RootTask)
	a.Brief.Scheduling.Deps = []workpool.Dependency{{From: "b", Guard: "done"}}
	b.Brief.Scheduling.Deps = []workpool.Dependency{{From: "a", Guard: "done"}}
	_, _, err := e.AmendGraph(proposal(g, "cycle", GraphOperation{Kind: "add", Spec: &a}, GraphOperation{Kind: "add", Spec: &b}))
	if err == nil {
		t.Fatal("dependency cycle accepted")
	}
	after, _ := e.Store.Graph(g.ID)
	if !reflect.DeepEqual(g, after) || len(e.Store.Tasks()) != 1 {
		t.Fatal("invalid transaction leaked partial members")
	}
	for _, op := range []GraphOperation{{Kind: "seal", Task: g.RootTask}, {Kind: "accept", Task: g.RootTask, Evidence: "done", Rationale: "done"}, {Kind: "unknown"}} {
		if _, _, err = e.AmendGraph(proposal(g, "invalid", op)); err == nil {
			t.Fatalf("invalid operation accepted: %+v", op)
		}
	}
	spec := g.Members[g.RootTask].Spec
	spec.Required = false
	if _, _, err = e.AmendGraph(proposal(g, "weaken", GraphOperation{Kind: "amend", Task: g.RootTask, Spec: &spec})); err == nil {
		t.Fatal("obligation weakened")
	}
}
func TestGraphConcurrentAmendmentsAndPersistenceFailure(t *testing.T) {
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, key := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			spec := graphWorker(key, g.RootTask)
			_, _, err := e.AmendGraph(proposal(g, key, GraphOperation{Kind: "add", Spec: &spec}))
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrConflict) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatal("CAS did not serialize")
	}
	before, _ := e.Store.Graph(g.ID)
	e.Store.file = &controlledLog{eventLog: e.Store.file, failure: errors.New("sync failed")}
	if _, _, err := e.AmendGraph(proposal(before, "fail", GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "no change"})); err == nil {
		t.Fatal("sync failure ignored")
	}
	after, _ := e.Store.Graph(g.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("unpersisted graph published")
	}
}
func TestGraphDispatchFreezeTriggersAndAcceptance(t *testing.T) {
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	spec := graphWorker("worker", g.RootTask)
	g, receipt := amendGraphTest(t, e, g, "plan", GraphOperation{Kind: "add", Spec: &spec}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "work discovered"}, GraphOperation{Kind: "seal", Task: g.RootTask, Cursor: g.Cursor})
	id := receipt.IDs["worker"]
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	m := g.Members[id]
	run, _ := e.Store.Get(m.CurrentRunID)
	if run.GraphAttempt == nil || run.GraphAttempt.Specification != m.Revision {
		t.Fatal("attempt has no frozen binding")
	}
	spec = m.Spec
	spec.Brief.Intent = "changed after queuing"
	if _, _, err := e.AmendGraph(proposal(g, "edit", GraphOperation{Kind: "amend", Task: id, Spec: &spec})); err == nil {
		t.Fatal("attempt specification mutated")
	}
	acquired, err := e.Store.Schedule(run.ID, nil)
	if err != nil || !acquired {
		t.Fatalf("common scheduler: %v", err)
	}
	frozen, _ := e.Store.Get(run.ID)
	if frozen.GraphAttempt.AdmittedAt == nil {
		t.Fatal("no admission receipt")
	}
	if err = e.Store.Complete(run.ID, "Native output, no framework tags", true); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	if g.Members[id].Accepted == nil || g.Members[g.RootTask].Sealed || len(g.Triggers) != 1 {
		t.Fatal("completion did not atomically reopen and register relevant event")
	}
	p := proposal(g, "premature", GraphOperation{Kind: "seal", Task: g.RootTask, Cursor: g.Cursor})
	if _, _, err = e.AmendGraph(p); err == nil {
		t.Fatal("pending trigger ignored")
	}
	g, _ = amendGraphTest(t, e, g, "classify", GraphOperation{Kind: "classify_trigger", Trigger: g.Triggers[0].Seq, Rationale: "No additional work"}, GraphOperation{Kind: "seal", Task: g.RootTask, Cursor: g.Cursor})
	p = proposal(g, "accept", GraphOperation{Kind: "accept", Task: g.RootTask, Evidence: "inventory result reviewed", Rationale: "Exact required result satisfies the goal"})
	root := g.Members[g.RootTask]
	p.Reads = map[string]GraphRead{g.RootTask: {root.Revision, root.FactRevision}}
	for id, m := range g.Members {
		p.Reads[id] = GraphRead{m.Revision, m.FactRevision}
	}
	g, _, err = e.AmendGraph(p)
	if err != nil || g.Status != "accepted" {
		t.Fatalf("accept: %v", err)
	}
	if _, _, err = e.AmendGraph(proposal(g, "reopen", GraphOperation{Kind: "reopen", Task: g.RootTask})); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted history reopened")
	}
}
func TestGraphDependenciesUseAcceptanceAndFreezeJoin(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Policy.Mode = "defined"
	a := graphWorker("a", "goal")
	a.Acceptance = "operator"
	b := graphWorker("b", "goal")
	b.Brief.Scheduling.Deps = []workpool.Dependency{{From: "a", Guard: "done"}}
	req.Tasks = append(req.Tasks, a, b)
	g := createGraphTest(t, e, req)
	var aid, bid string
	for id, m := range g.Members {
		if m.Spec.Kind == "application" {
			if len(m.Spec.Brief.Scheduling.Deps) == 0 {
				aid = id
			} else {
				bid = id
			}
		}
	}
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	ar := g.Members[aid].CurrentRunID
	if ar == "" || g.Members[bid].CurrentRunID != "" {
		t.Fatal("dependency ignored")
	}
	if err := e.Store.Complete(ar, "Unverified completion is only a candidate", false); err != nil {
		t.Fatal(err)
	}
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	if g.Members[bid].CurrentRunID != "" {
		t.Fatal("process exit mistaken for acceptance")
	}
	p := proposal(g, "accept-a", GraphOperation{Kind: "accept", Task: aid, Evidence: "operator reviewed inventory", Rationale: "meets criteria"})
	p.Reads = map[string]GraphRead{aid: {g.Members[aid].Revision, g.Members[aid].FactRevision}}
	if _, _, err := e.AmendGraph(p); err != nil {
		t.Fatal(err)
	}
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	br := g.Members[bid].CurrentRunID
	if br == "" {
		t.Fatal("accepted dependency did not dispatch")
	}
	ok, err := e.Store.Schedule(br, nil)
	if err != nil || !ok {
		t.Fatal(err)
	}
	run, _ := e.Store.Get(br)
	if len(run.GraphAttempt.Witnesses) != 1 || run.GraphAttempt.Witnesses[0].RunID != ar {
		t.Fatal("input witness not frozen")
	}
}
func TestGraphRetryAccountingAndLegacyBypass(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Policy.Mode = "defined"
	req.Tasks = append(req.Tasks, graphWorker("worker", "goal"))
	g := createGraphTest(t, e, req)
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var id string
	for tid, m := range g.Members {
		if m.Spec.Kind == "application" {
			id = tid
		}
	}
	for attempt := 0; attempt < 3; attempt++ {
		runID := g.Members[id].CurrentRunID
		if err := e.Store.Transition(runID, "test_failed", "failed", "Failed", "fixture"); err != nil {
			t.Fatal(err)
		}
		if _, err := e.Retry(runID); err == nil {
			t.Fatal("legacy retry bypassed graph allowance")
		}
		g, _ = e.Store.Graph(g.ID)
		p := proposal(g, "retry-"+runID, GraphOperation{Kind: "retry", Task: id})
		next, _, err := e.AmendGraph(p)
		if attempt == 2 {
			if err == nil {
				t.Fatal("retry budget reset")
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		g = next
		if err = e.reconcileGraphs(); err != nil {
			t.Fatal(err)
		}
		g, _ = e.Store.Graph(g.ID)
	}
	if g.Attempts != 3 || len(g.Members[id].Attempts) != 3 {
		t.Fatal("attempt history or shared accounting lost")
	}
}
func TestGraphAutomaticInventoryExecution(t *testing.T) {
	e, req := graphEngine(t, true)
	req.Policy.Mode = "defined"
	req.Tasks = append(req.Tasks, graphWorker("worker", "goal"))
	g := createGraphTest(t, e, req)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		g, _ = e.Store.Graph(g.ID)
		for _, m := range g.Members {
			if m.Spec.Kind == "application" && m.Accepted != nil {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	b, _ := json.Marshal(g)
	t.Fatalf("automatic work did not complete: %s", b)
}
func TestGraphStrictJSONAndCanonicalDigest(t *testing.T) {
	for _, raw := range []string{`{"key":"a","key":"b"}`, `{"key":"a","unknown":1}`, `{"key":"a"} {}`, `{"revision":1.0}`, `{"revision":1e0}`, `{"key":"\ud800"}`, `{"key":"\udc00"}`, string([]byte{'{', '"', 'k', 'e', 'y', '"', ':', '"', 255, '"', '}'})} {
		var p GraphProposal
		if DecodeGraphJSON([]byte(raw), &p) == nil {
			t.Fatalf("ambiguous input accepted: %q", raw)
		}
	}
	var a, b GraphProposal
	if DecodeGraphJSON([]byte(`{"key":"é","revision":1}`), &a) != nil || DecodeGraphJSON([]byte(`{"revision":1,"key":"\u00e9"}`), &b) != nil || graphDigest(a) != graphDigest(b) {
		t.Fatal("typed canonicalization differs by field order or equivalent string escaping")
	}
	if got := graphDigest(a); got != "28c5565100e47a95566ebcf46c7192e1f71ea818f4c2f3fce41f57412994858d" {
		t.Fatalf("v1 digest encoding changed: %s", got)
	}
}

func TestGraphSealOutcomeRaceAndOldGeneration(t *testing.T) {
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	spec := graphWorker("worker", g.RootTask)
	g, r := amendGraphTest(t, e, g, "plan", GraphOperation{Kind: "add", Spec: &spec}, GraphOperation{Kind: "close_expansion", Expansion: "initial", Rationale: "ready"})
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	runID := g.Members[r.IDs["worker"]].CurrentRunID
	p := proposal(g, "seal", GraphOperation{Kind: "seal", Task: g.RootTask, Cursor: g.Cursor})
	var wg sync.WaitGroup
	wg.Add(2)
	var completionErr, sealErr error
	go func() { defer wg.Done(); completionErr = e.Store.Complete(runID, "verified", true) }()
	go func() { defer wg.Done(); _, _, sealErr = e.AmendGraph(p) }()
	wg.Wait()
	if completionErr != nil {
		t.Fatal(completionErr)
	}
	if sealErr != nil && !errors.Is(sealErr, ErrConflict) && !strings.Contains(sealErr.Error(), "trigger") {
		t.Fatal(sealErr)
	}
	current, _ := e.Store.Graph(g.ID)
	if current.Members[current.RootTask].Sealed || len(current.Triggers) != 1 || current.Triggers[0].Outcome != "" {
		t.Fatal("seal raced past relevant outcome")
	}
	stale := proposal(current, "old-controller", GraphOperation{Kind: "reopen", Task: current.RootTask})
	next, _ := amendGraphTest(t, e, current, "replace", GraphOperation{Kind: "replace_controller", Rationale: "new controller"})
	stale.Revision = next.Revision
	if _, _, err := e.amendGraph(stale, false, false); !errors.Is(err, ErrConflict) {
		t.Fatal("old generation admitted after replacement")
	}
}
func TestGraphReadSetIgnoresLogsButFencesOutcomes(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Policy.Mode = "defined"
	req.Tasks = append(req.Tasks, graphWorker("worker", "goal"))
	g := createGraphTest(t, e, req)
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var tid string
	for id, m := range g.Members {
		if m.Spec.Kind == "application" {
			tid = id
		}
	}
	m := g.Members[tid]
	p := proposal(g, "evidence", GraphOperation{Kind: "reopen", Task: g.RootTask})
	p.Reads = map[string]GraphRead{tid: {m.Revision, m.FactRevision}}
	if err := e.Store.RecordEvidence(m.CurrentRunID, "log", "ordinary output", map[string]string{"text": "hello"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.PreviewGraph(p); err != nil {
		t.Fatal("unrelated output invalidated read set", err)
	}
	if err := e.Store.Complete(m.CurrentRunID, "verified", true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.PreviewGraph(p); !errors.Is(err, ErrConflict) {
		t.Fatal("changed result did not invalidate read set")
	}
}
func TestGraphSupersessionKeepsRequiredWorkAndCustody(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Policy.Mode = "defined"
	req.Tasks = append(req.Tasks, graphWorker("worker", "goal"))
	g := createGraphTest(t, e, req)
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	var id string
	for tid, m := range g.Members {
		if m.Spec.Kind == "application" {
			id = tid
		}
	}
	oldRun := g.Members[id].CurrentRunID
	replacement := g.Members[id].Spec
	replacement.ID = "replacement"
	g, receipt := amendGraphTest(t, e, g, "replace-work", GraphOperation{Kind: "supersede", Task: id, Spec: &replacement, Rationale: "Change the investigation approach"})
	if g.Members[id].Replacement != receipt.IDs["replacement"] || !g.Members[id].Spec.Required || !g.Members[id].CancelRequested {
		t.Fatal("supersession lost obligation or cancellation intent")
	}
	r, _ := e.Store.Get(oldRun)
	if r.Status != "queued" {
		t.Fatal("amendment pretended process stop completed")
	}
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	r, _ = e.Store.Get(oldRun)
	if r.Status != "cancelled" {
		t.Fatal("durable cancellation was not applied")
	}
	g, _ = e.Store.Graph(g.ID)
	rid := g.Members[receipt.IDs["replacement"]].CurrentRunID
	if rid == "" {
		t.Fatal("replacement not independently scheduled")
	}
	if err := e.Store.Complete(rid, "new evidence", true); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	g, _ = amendGraphTest(t, e, g, "seal", GraphOperation{Kind: "seal", Task: g.RootTask, Cursor: g.Cursor})
	p := proposal(g, "accept", GraphOperation{Kind: "accept", Task: g.RootTask, Evidence: "replacement evidence reviewed", Rationale: "goal complete"})
	p.Reads = map[string]GraphRead{}
	for tid, m := range g.Members {
		p.Reads[tid] = GraphRead{m.Revision, m.FactRevision}
	}
	if _, _, err := e.AmendGraph(p); err != nil {
		t.Fatal(err)
	}
}

func TestGraphPreviewAndReceiptsDoNotMutateInputs(t *testing.T) {
	e, req := graphEngine(t, false)
	g := createGraphTest(t, e, req)
	a, b := graphWorker("a", g.RootTask), graphWorker("b", g.RootTask)
	b.Brief.Scheduling.Deps = []workpool.Dependency{{From: "a", Guard: "done"}}
	p := proposal(g, "paired", GraphOperation{Kind: "add", Spec: &a}, GraphOperation{Kind: "add", Spec: &b})
	before := graphClone(p)
	if _, _, err := e.PreviewGraph(p); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, p) {
		t.Fatal("preview rewrote caller references")
	}
	_, receipt, err := e.AmendGraph(p)
	if err != nil {
		t.Fatal(err)
	}
	_, duplicate, err := e.AmendGraph(p)
	if err != nil {
		t.Fatal(err)
	}
	duplicate.IDs["a"] = "tampered"
	stored, _ := e.Store.Graph(g.ID)
	if stored.Receipts[p.Key].IDs["a"] != receipt.IDs["a"] {
		t.Fatal("receipt read leaked mutable state")
	}
}
