package studio

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInterruptedAsyncClarificationPreservesPrerequisite(t *testing.T) {
	// The incident's question was over 400 characters: the CLI acknowledged
	// it, while decision-card validation rejected it and left no Inbox item.
	question := strings.Repeat("Which configured publication route has been restored? ", 10)
	encoded, _ := json.Marshal(question)
	script := `import sys,json,pathlib
def send(v):print(json.dumps(v),flush=True)
for line in sys.stdin:
 r=json.loads(line);m=r.get('method')
 if m=='initialize':send({'id':r['id'],'result':{}})
 elif m=='thread/start':send({'id':r['id'],'result':{'thread':{'id':'async-thread'}}})
 elif m=='turn/start':
  pathlib.Path('prepared-issues.txt').write_text('preserved drafts')
  send({'id':r['id'],'result':{'turn':{'id':'async-turn'}}})
  send({'method':'item/completed','params':{'threadId':'async-thread','turnId':'async-turn','item':{'type':'agentMessage','id':'question','delivery':'async','questions':[{'title':` + string(encoded) + `,'options':None}]}}})
`
	e, g := nativeAuditGraph(t, script, 1)
	var task, run string
	for id, m := range g.Members {
		if m.Spec.Kind == "application" {
			task, run = id, m.CurrentRunID
		}
	}
	if ok, err := e.Store.AcquireClaims(run); err != nil || !ok {
		t.Fatal(err)
	}
	r, _ := e.Store.Get(run)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); e.execute(ctx, r) }()
	deadline := time.Now().Add(10 * time.Second)
	for e.Store.pendingAgentPrerequisite(run) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	activity, err := e.Store.GraphActivity(g.ID, task, run)
	if err != nil || len(activity.Requests) != 1 || activity.Requests[0].Blocking || !strings.Contains(activity.Requests[0].Decision.Context.Attempted, question) {
		t.Fatalf("async question not visible with full context: %v %+v", err, activity.Requests)
	}
	view, _ := e.Store.GraphView(g.ID, task)
	if view.Nodes[task].State != "running" {
		t.Fatal("optional input stopped independent work", view.Nodes[task])
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not settle")
	}
	r, _ = e.Store.Get(run)
	if r.Status != "failed" || r.BlockedReason != "native_request_unanswered" {
		t.Fatalf("clarification became generic timeout: %s %s", r.Status, r.Summary)
	}
	patch, err := e.Store.Artifact(run, "changes.patch")
	if err != nil || !strings.Contains(string(patch), "preserved drafts") {
		t.Fatal("lost prepared work", err)
	}
	var authority AuthorityReceipt
	raw, err := e.Store.Artifact(run, "authority.json")
	if err != nil || json.Unmarshal(raw, &authority) != nil || authority.Policy != EnforcementAdvisory || authority.Definition != "mitos.researcher@3" || authority.Granted("workspace.write") {
		t.Fatalf("attempt authority receipt missing or wrong: %v %s", err, raw)
	}
	view, _ = e.Store.GraphView(g.ID, task)
	if view.Nodes[task].State != "needs_attention" || view.Nodes[task].BlockedReason == "" {
		t.Fatal("missing prerequisite banner facts", view.Nodes[task])
	}
}

func TestUnroutableAsyncClarificationStopsReplacement(t *testing.T) {
	script := `import sys,json,pathlib
def send(v):print(json.dumps(v),flush=True)
for line in sys.stdin:
 r=json.loads(line);m=r.get('method')
 if m=='initialize':send({'id':r['id'],'result':{}})
 elif m=='thread/start':send({'id':r['id'],'result':{'thread':{'id':'async-thread'}}})
 elif m=='turn/start':
  pathlib.Path('partial.txt').write_text('preserve before routing failure')
  send({'id':r['id'],'result':{'turn':{'id':'async-turn'}}})
  send({'method':'item/completed','params':{'threadId':'async-thread','turnId':'async-turn','item':{'type':'agentMessage','id':'question','delivery':'async','questions':[{'title':str(i)} for i in range(4)]}}})
`
	e, g := nativeAuditGraph(t, script, 1)
	var run string
	for _, m := range g.Members {
		if m.Spec.Kind == "application" {
			run = m.CurrentRunID
		}
	}
	if ok, err := e.Store.AcquireClaims(run); err != nil || !ok {
		t.Fatal(err)
	}
	r, _ := e.Store.Get(run)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.execute(ctx, r)
	r, _ = e.Store.Get(run)
	if r.Status != "failed" || r.BlockedReason == "" || !strings.Contains(r.Summary, "could not be recorded") || !e.Store.GraphPrerequisiteBlocked(g.ID) {
		t.Fatalf("lost input routing failure: %s %s", r.Status, r.Summary)
	}
	patch, err := e.Store.Artifact(run, "changes.patch")
	if err != nil || !strings.Contains(string(patch), "preserve before routing failure") {
		t.Fatal("lost partial work", err)
	}
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	if g.Attempts != 1 || g.OrchestratorCalls != 0 {
		t.Fatal("repeated an unanswerable input request")
	}
}

func nativeAuditGraph(t *testing.T, script string, workers int) (*Engine, WorkGraph) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte("#!"+python+"\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, req := graphEngine(t, false)
	// Run selected attempts explicitly; keep the background scheduler stopped.
	e.ctx = context.Background()
	req.Policy.Automatic = true
	req.Policy.OrchestratorApplication = "workspace-inventory"
	req.Policy.Applications = append(req.Policy.Applications, "agent:mitos.researcher")
	for i := 0; i < workers; i++ {
		spec := graphWorker(strings.Repeat("w", i+1), "goal")
		spec.Brief.AppID = "agent:mitos.researcher"
		spec.Traits = traits("small", "low")
		if i > 0 {
			spec.Brief.Ownership.Leases = []string{"shared-github-write"}
		}
		req.Tasks = append(req.Tasks, spec)
	}
	g := createGraphTest(t, e, req)
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	return e, g
}

func TestNativeCopiesOverlapButResourceLeasesDoNot(t *testing.T) {
	e, g := nativeAuditGraph(t, "", 3)
	var runs []string
	for _, id := range graphIDs(&g) {
		m := g.Members[id]
		if m.Spec.Kind == "application" {
			runs = append(runs, m.CurrentRunID)
		}
	}
	acquired, blocked := 0, 0
	for _, id := range runs {
		ok, err := e.Store.AcquireClaims(id)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			acquired++
			r, _ := e.Store.Get(id)
			if r.Claim.Workspace != filepath.Join(e.Store.Dir(), "workspaces", id, "workspace") {
				t.Fatal("source tree still claimed")
			}
		} else {
			blocked++
			r, _ := e.Store.Get(id)
			if len(r.ClaimConflicts) != 1 || r.ClaimConflicts[0].Kind != "lease" {
				t.Fatalf("unexpected conflict: %+v", r.ClaimConflicts)
			}
		}
	}
	if acquired != 2 || blocked != 1 {
		t.Fatalf("acquired=%d blocked=%d", acquired, blocked)
	}
}

func TestUnansweredNativeApprovalStopsReplanningAndPreservesWork(t *testing.T) {
	script := `import sys,json,pathlib,os
def send(v):print(json.dumps(v),flush=True)
for line in sys.stdin:
 r=json.loads(line);m=r.get('method')
 if m=='initialize':send({'id':r['id'],'result':{}})
 elif m=='thread/start':send({'id':r['id'],'result':{'thread':{'id':'audit-thread'}}})
 elif m=='turn/start':
  cache=pathlib.Path(os.environ['GOCACHE']);assert cache.is_dir()
  (cache/'writable').write_text('cache access without escalation')
  pathlib.Path('partial-audit.txt').write_text('saved findings')
  send({'id':r['id'],'result':{'turn':{'id':'audit-turn'}}})
  send({'id':77,'method':'item/commandExecution/requestApproval','params':{'threadId':'audit-thread','turnId':'audit-turn','command':'test -f partial-audit.txt','cwd':str(pathlib.Path.cwd())}})
`
	e, g := nativeAuditGraph(t, script, 1)
	var task, run string
	for id, m := range g.Members {
		if m.Spec.Kind == "application" {
			task, run = id, m.CurrentRunID
		}
	}
	if ok, err := e.Store.AcquireClaims(run); err != nil || !ok {
		t.Fatal(err)
	}
	r, _ := e.Store.Get(run)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); e.execute(ctx, r) }()
	deadline := time.Now().Add(10 * time.Second)
	for e.Store.pendingAgentPrerequisite(run) == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if e.Store.pendingAgentPrerequisite(run) == nil {
		t.Fatal("fixture did not request approval")
	}
	view, _ := e.Store.GraphView(g.ID, task)
	if view.Nodes[task].State != "waiting_input" || !strings.Contains(view.Nodes[task].Summary, "Waiting for an answer") {
		t.Fatal(view.Nodes[task])
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not settle")
	}
	r, _ = e.Store.Get(run)
	if r.Status != "failed" || r.BlockedReason != "native_request_unanswered" || r.Claim.ReleasedAt == nil {
		t.Fatalf("lost prerequisite: %s %s %s", r.Status, r.BlockedReason, r.Summary)
	}
	patch, err := e.Store.Artifact(run, "changes.patch")
	if err != nil || !strings.Contains(string(patch), "saved findings") {
		t.Fatal("lost partial audit", err)
	}
	if !e.Store.GraphPrerequisiteBlocked(g.ID) {
		t.Fatal("prerequisite not projected")
	}
	for i := 0; i < 3; i++ {
		if err := e.reconcileGraphs(); err != nil {
			t.Fatal(err)
		}
	}
	g, _ = e.Store.Graph(g.ID)
	if g.OrchestratorCalls != 0 || g.Attempts != 1 {
		t.Fatal("replanning repeated the missing prerequisite")
	}
	p := proposal(g, "automatic-retry", GraphOperation{Kind: "retry", Task: task})
	if _, _, err := e.proposeGraph(&g, p, graphDigest(p), false); err == nil {
		t.Fatal("planner bypassed prerequisite hold")
	}
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
	if !reopened.GraphPrerequisiteBlocked(g.ID) {
		t.Fatal("restart lost prerequisite")
	}
	if _, err := e.ResumeGraph(g.ID, resumeRequest(g)); err != nil {
		t.Fatal(err)
	}
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	next, _ := e.Store.Graph(g.ID)
	continued, _ := e.Store.Get(next.Members[task].CurrentRunID)
	if continued.GraphAttempt.ContinueFrom != run || continued.BlockedReason != "" {
		t.Fatal("operator continuation lost saved work or hold was inherited")
	}
}

func TestPrerequisiteExpiryKeepsReasonButOperatorCancellationWins(t *testing.T) {
	for _, mode := range []string{"deadline", "operator", "before_deadline"} {
		t.Run(mode, func(t *testing.T) {
			e, g := nativeAuditGraph(t, "", 1)
			var id string
			for _, m := range g.Members {
				if m.Spec.Kind == "application" {
					id = m.CurrentRunID
				}
			}
			e.Store.mu.Lock()
			r := e.Store.runs[id]
			r.Status = "stopping"
			graph := e.Store.graphs[g.ID]
			if mode != "before_deadline" {
				graph.Limits.Deadline = time.Now().Add(-time.Second)
			}
			graph.Members[r.TaskID].CancelRequested = mode == "operator"
			e.Store.mu.Unlock()
			err := e.Store.blockAgentPrerequisite(id, "Stopped while waiting for an answer.")
			got, _ := e.Store.Get(id)
			if mode == "deadline" {
				if err != nil || got.Status != "failed" || got.BlockedReason == "" {
					t.Fatalf("expiry lost prerequisite: %v %+v", err, got)
				}
			} else if err != ErrConflict || got.Status != "stopping" || got.BlockedReason != "" {
				t.Fatalf("overrode operator cancellation: %v %+v", err, got)
			}
		})
	}
}
