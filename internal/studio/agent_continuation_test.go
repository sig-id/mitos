package studio

import (
	"context"
	"encoding/json"
	workspacefs "mitos/internal/workspace"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real scheduler + app-server transport: both worker and coordinator fail,
// then continue under their original task identities with larger allowances.
func TestAutomaticResourceContinuation(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required")
	}
	bin := t.TempDir()
	script := `import json,pathlib,sys
prompt=sys.stdin.read()
out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
if '--output-schema' in sys.argv:
 ctx=json.loads(prompt.split('\nGraph context:\n')[1].split('\nSaved upstream evidence')[0])
 workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 if workers and workers[0]['state']=='failed':sys.exit(9)
 plan={'explanation':'Continue the same work.','steps':[],'accept':[],'retry':[],'complete':bool(workers),'result':{'title':'','markdown':'','source_tasks':[]}}
 if not workers:
  plan['steps']=[{'id':'worker','title':'Keep partial work','assignment':'Finish answer.txt','criteria':'Preserve partial work','agent':'mitos.engineer','role':'','instructions':'','after':[],'replaces':''}]
 else:
  assert workers[0]['state']=='done',str(workers)
  plan['accept']=[{'task':workers[0]['id'],'rationale':'Verified the saved result'}]
  plan['result']={'title':'Continued','markdown':'Partial work retained.','source_tasks':[workers[0]['id']]}
 out.write_text(json.dumps(plan))
else:
 if 'Previous run:' not in prompt:
  pathlib.Path('partial.txt').write_text('keep me')
  pathlib.Path('hello.txt').unlink()
  sys.exit(8)
 assert pathlib.Path('partial.txt').read_text()=='keep me'
 assert not pathlib.Path('hello.txt').exists()
 pathlib.Path('answer.txt').write_text('finished')
 out.write_text('Kept partial work and finished.')
`
	fixture := appServerFixture(script)
	fixture = strings.Replace(fixture, "elif method=='thread/start':", "elif method in ('thread/start','thread/resume'):", 1)
	if err = os.WriteFile(filepath.Join(bin, "codex"), []byte("#!"+python+"\n"+fixture), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, req := graphEngine(t, true)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "resume-e2e", ProjectID: req.ProjectID, Goal: "Keep partial work and finish"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	stopped := false
	for time.Now().Before(deadline) {
		g, _ = e.Store.Graph(g.ID)
		for _, x := range g.Expansions {
			if x.Error == "controller_execution_needs_review" {
				stopped = true
			}
		}
		if stopped {
			settled := true
			for _, m := range g.Members {
				for _, id := range m.Attempts {
					r, _ := e.Store.Get(id)
					if !r.Terminal() || r.Claim != nil && r.Claim.ReleasedAt == nil {
						settled = false
					}
				}
			}
			if settled {
				break
			}
			stopped = false
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !stopped {
		t.Fatalf("workflow did not stop: %+v", g)
	}
	var worker, planner, workerRun, plannerRun string
	for id, m := range g.Members {
		if m.State == "failed" {
			if m.ControllerFor == "" {
				worker = id
				workerRun = m.CurrentRunID
			} else {
				planner = id
				plannerRun = m.CurrentRunID
			}
		}
	}
	if worker == "" || planner == "" {
		t.Fatal("missing failed agents")
	}
	r, _ := e.Store.Get(workerRun)
	if r.NativeThreadID != "fixture-thread" || r.Workspace.ResultCommit == "" {
		t.Fatal("conversation or workspace not persisted")
	}
	// Mutating an abandoned tree and source must not corrupt the saved checkpoint.
	if err = os.WriteFile(filepath.Join(e.Store.Dir(), "workspaces", workerRun, "workspace", "partial.txt"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(e.Root, "hello.txt"), []byte("source changed"), 0600); err != nil {
		t.Fatal(err)
	}
	next, err := e.ResumeGraph(g.ID, resumeRequest(g))
	if err != nil {
		t.Fatal(err)
	}
	if next.ID != g.ID {
		t.Fatal("created another workflow")
	}
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		next, _ = e.Store.Graph(g.ID)
		if next.Status == "accepted" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if next.Status != "accepted" {
		for _, m := range next.Members {
			if m.CurrentRunID != "" {
				r, _ := e.Store.Get(m.CurrentRunID)
				t.Log(r.Status, r.Summary)
			}
		}
		t.Fatalf("continuation did not finish: %+v", next.Expansions)
	}
	if len(next.Members) != len(g.Members) {
		t.Fatal("replaced workflow structure")
	}
	for id, source := range map[string]string{worker: workerRun, planner: plannerRun} {
		resumed, _ := e.Store.Get(next.Members[id].CurrentRunID)
		if resumed.ParentID != source || resumed.GraphAttempt.ContinueFrom != source || resumed.NativeThreadID != "fixture-thread" {
			t.Fatal("agent identity or conversation lost")
		}
	}
	patch, err := e.Store.Artifact(next.Members[worker].CurrentRunID, "changes.patch")
	if err != nil || !strings.Contains(string(patch), "partial.txt") || !strings.Contains(string(patch), "deleted file") || !strings.Contains(string(patch), "answer.txt") {
		t.Fatalf("changes are not cumulative: %s %v", patch, err)
	}
	if next.Attempts != g.Attempts+2 || next.ControllerCalls != g.ControllerCalls+1 {
		t.Fatal("continued attempts not charged")
	}
}

func TestAgentContinuationLegacyUsesImmutableWorkspace(t *testing.T) {
	e, g, _, stopped := stoppedGraph(t)
	prior, _ := e.Store.Get(g.Members[stopped].CurrentRunID)
	ctx := context.Background()
	// Use the ordinary workspace preparation and harvest contract for an old run
	// with no saved native conversation.
	base := filepath.Join(e.Store.Dir(), "workspaces", prior.ID)
	before, err := workspaceCapture(ctx, e.Root, e.Store.Dir(), base)
	if err != nil {
		t.Fatal(err)
	}
	e.Store.mu.Lock()
	e.Store.runs[prior.ID].Status = "running"
	e.Store.mu.Unlock()
	if err = e.Store.RecordWorkspace(prior.ID, "workspace_prepared", "baseline", WorkspaceRecord{Mode: "copy", BaselineCommit: before.BaselineCommit, Fingerprint: before.InitialFingerprint}); err != nil {
		t.Fatal(err)
	}
	if err = e.Store.RecordWorkspace(prior.ID, "workspace_harvested", "saved", WorkspaceRecord{Mode: "copy", BaselineCommit: before.BaselineCommit, ResultCommit: before.ResultCommit, Fingerprint: before.Fingerprint}); err != nil {
		t.Fatal(err)
	}
	e.Store.mu.Lock()
	e.Store.runs[prior.ID].Status = "failed"
	e.Store.mu.Unlock()
	r := prior
	r.GraphAttempt = graphClone(prior.GraphAttempt)
	r.GraphAttempt.ContinueFrom = prior.ID
	saved, baseline, current, err := e.agentContinuation(ctx, r)
	if err != nil || saved.NativeThreadID != "" || baseline.Fingerprint != before.InitialFingerprint || current.Fingerprint != before.Fingerprint {
		t.Fatal("legacy continuation failed", err)
	}
	failedStart := r
	failedStart.ID = "failed-start"
	failedStart.Workspace = nil
	failedStart.Events = nil
	e.Store.mu.Lock()
	e.Store.runs[failedStart.ID] = &failedStart
	e.Store.mu.Unlock()
	r.GraphAttempt = graphClone(r.GraphAttempt)
	r.GraphAttempt.ContinueFrom = failedStart.ID
	recovered, _, restored, err := e.agentContinuation(ctx, r)
	if err != nil || recovered.ID != prior.ID || restored.Fingerprint != current.Fingerprint {
		t.Fatal("failed launch lost the earlier checkpoint", err)
	}
	encoded, _ := json.Marshal(current)
	if !strings.Contains(string(encoded), "partial.txt") {
		t.Fatal("legacy partial files lost")
	}
}

func workspaceCapture(ctx context.Context, root, data, base string) (struct {
	workspacefs.Harvest
	InitialFingerprint string
}, error) {
	var result struct {
		workspacefs.Harvest
		InitialFingerprint string
	}
	before, err := workspacefs.CaptureSource(ctx, root, data, workspacefs.DefaultLimits)
	if err != nil {
		return result, err
	}
	h, err := workspacefs.Prepare(ctx, base, before)
	if err != nil {
		return result, err
	}
	if err = os.WriteFile(filepath.Join(base, "workspace", "partial.txt"), []byte("partial"), 0600); err != nil {
		return result, err
	}
	result.Harvest, err = h.Harvest(ctx, before)
	result.InitialFingerprint = before.Fingerprint
	return result, err
}
