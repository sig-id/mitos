package studio

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercises the native CLI boundary, durable scheduler, dependencies, generated
// role, independent orchestrator review, and completion without operator amendments.
func TestAutomaticGoalEndToEnd(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for native CLI fixture")
	}
	bin := t.TempDir()
	script := `import json,pathlib,sys
prompt=sys.stdin.read()
out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
if '--output-schema' in sys.argv:
 ctx=json.loads(prompt.split('\nGraph context:\n')[1].split('\nSaved upstream evidence')[0])
 workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 plan={'explanation':'Evidence meets the goal.','operations':[],'accept':[],'complete':bool(workers)}
 if not workers:
  for i in range(2):
   plan['operations'].append({'kind':'add','spec':{'id':'work'+str(i),'parent':ctx['root_task'],'kind':'application','required':True,'acceptance':'operator','criteria':'Produce evidence for the answer.','brief':{'title':['Implement change','Review change'][i],'intent':['Create answer.txt with 42.','Check upstream patch and evidence.'][i],'app_id':'agent:'+ (['mitos.engineer','mitos.reviewer'][i]),'timeout_minutes':30,'scheduling':{'deps':[{'from':ref,'guard':'candidate_available','payload':'data','artifacts':['report.md','changes.patch']} for ref in (['work0'] if i else [])]}},'agent':({'role':'Answer specialist' if i else '','instructions':'Check the answer carefully.' if i else ''} if ('Answer specialist' if i else '') else None)}})
 else:
  inputs=json.loads(prompt.split('\nSaved upstream evidence (read these files):\n')[1].split('\nWork only')[0])
  assert len(inputs)==2
  for item in inputs:
   assert pathlib.Path(item['directory'],'changes.patch').exists()
   assert pathlib.Path(item['directory'],'changes.json').exists()
  assert len(workers)==2
  for f in workers:
   assert f['state']=='done',str(f)
   plan['accept'].append({'task':f['id'],'rationale':'Read the saved native report and reviewed its evidence.'})
 plan['result']={'title':'Answer','markdown':'The answer is **42**.\n\n- [ ] Example open finding.\n\n[Answer file](mitos://'+workers[0]['id']+'/answer.txt) and [source](mitos://'+workers[0]['id']+'/hello.txt#L1).','source_tasks':[f['id'] for f in workers]} if workers else {'title':'','markdown':'','source_tasks':[]}
 out.write_text(json.dumps(plan))
else:
 if 'Check upstream patch and evidence.' in prompt:
  inputs=json.loads(prompt.split('\nSaved upstream evidence (read these files):\n')[1].split('\nWork only')[0])
  assert len(inputs)==1, inputs
  assert '42' in pathlib.Path(inputs[0]['directory'],'changes.patch').read_text()
  assert pathlib.Path('answer.txt').read_text().strip()=='42'
 else:
  pathlib.Path('answer.txt').write_text('42\n')
 out.write_text('# Result\n\nThe answer is **42**.\n\nEvidence checked.')
print('native execution log')
`
	if err = os.WriteFile(filepath.Join(bin, "codex"), []byte("#!"+python+"\n"+appServerFixture(script)), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, req := graphEngine(t, true)
	goal := AutomaticGoal{Key: "automatic-test", ProjectID: req.ProjectID, Goal: "Produce and review the answer"}
	g, err := e.StartAutomaticGoal(goal)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := e.StartAutomaticGoal(goal)
	if err != nil || replay.ID != g.ID {
		t.Fatalf("replay: %v", err)
	}
	changed := goal
	changed.Goal = "Different goal"
	if _, err = e.StartAutomaticGoal(changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("key conflict: %v", err)
	}
	until := time.Now().Add(25 * time.Second)
	for time.Now().Before(until) {
		g, err = e.Store.Graph(g.ID)
		if err != nil {
			t.Fatal(err)
		}
		if g.Status == "accepted" {
			break
		}
		for _, x := range g.Expansions {
			if x.Error != "" {
				for _, m := range g.Members {
					if m.CurrentRunID != "" {
						r, _ := e.Store.Get(m.CurrentRunID)
						t.Logf("run %s %s %s", r.ID, r.Status, r.Summary)
						output, _ := e.Store.Artifact(r.ID, "output.txt")
						t.Log(string(output))
					}
				}
				t.Fatalf("orchestrator: %s", x.Error)
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if g.Status != "accepted" {
		raw, _ := json.Marshal(g)
		t.Fatalf("goal did not finish: %s", raw)
	}
	workers := 0
	generated := false
	for _, m := range g.Members {
		if m.OrchestratorFor == "" && m.Spec.Kind == "application" {
			workers++
			if m.Accepted == nil {
				t.Fatal("worker not reviewed")
			}
			r, _ := e.Store.Get(m.CurrentRunID)
			if r.Verified {
				t.Fatal("worker exit incorrectly presented as verifier proof")
			}
			body, _ := e.Store.Artifact(r.ID, "report.md")
			if !strings.Contains(string(body), "**42**") {
				t.Fatal("native report missing")
			}
			if m.Executor.Application.Agent.Specialization != nil {
				generated = true
			}
		}
	}

	if g.Result == nil || g.Result.Title != "Answer" || !strings.Contains(g.Result.Markdown, "42") || len(g.Result.Sources) != 2 {
		t.Fatalf("missing final answer: %+v", g.Result)
	}
	for _, m := range g.Members {
		if m.Spec.Brief.Title == "Implement change" {
			files, err := e.Store.GraphRunFiles(g.ID, m.Spec.ID, m.CurrentRunID)
			if err != nil || len(files) != 1 || files[0].Path != "answer.txt" {
				t.Fatalf("saved files: %v %v", files, err)
			}
			body, err := e.Store.GraphRunFile(g.ID, m.Spec.ID, m.CurrentRunID, "answer.txt")
			if err != nil || string(body) != "42\n" {
				t.Fatalf("saved deliverable: %q %v", body, err)
			}
			source, err := e.Store.GraphRunFile(g.ID, m.Spec.ID, m.CurrentRunID, "hello.txt")
			if err != nil || len(source) == 0 {
				t.Fatalf("unchanged source unavailable: %v", err)
			}
			for _, path := range []string{"../answer.txt", "/etc/passwd", "missing.txt"} {
				if _, err = e.Store.GraphRunFile(g.ID, m.Spec.ID, m.CurrentRunID, path); err == nil {
					t.Fatalf("unauthorized result path %s", path)
				}
			}
			if _, err = e.Store.GraphRunFile(g.ID, g.RootTask, m.CurrentRunID, "answer.txt"); err == nil {
				t.Fatal("result task/run binding ignored")
			}
			if err = os.WriteFile(filepath.Join(e.Store.Dir(), "workspaces", m.CurrentRunID, "workspace", "answer.txt"), []byte("mutated"), 0600); err != nil {
				t.Fatal(err)
			}
			body, err = e.Store.GraphRunFile(g.ID, m.Spec.ID, m.CurrentRunID, "answer.txt")
			if err != nil || string(body) != "42\n" {
				t.Fatal("result read mutable workspace")
			}
		}
	}
	if workers != 2 || !generated {
		t.Fatal("missing worker or specialization")
	}

	// An older accepted graph can receive its missing answer without rerunning work.
	e.Store.mu.Lock()
	e.Store.graphs[g.ID].Result = nil
	e.Store.mu.Unlock()
	answer := PublishGraphResult{Revision: g.Revision, FinalAnswer: FinalAnswer{Title: "Recovered answer", Markdown: "The answer is 42.", SourceTasks: []string{g.Result.Sources[0].Task}}}
	stale := answer
	stale.Revision--
	if _, err = e.Store.PublishResult(g.ID, stale); !errors.Is(err, ErrConflict) {
		t.Fatal("stale result publication accepted")
	}
	published, err := e.Store.PublishResult(g.ID, answer)
	if err != nil || published.Status != "accepted" || published.Attempts != g.Attempts || published.Revision != g.Revision+1 || published.Result.Markdown != answer.Markdown {
		t.Fatalf("publication changed work lifecycle: %v", err)
	}
	if _, err = e.Store.PublishResult(g.ID, answer); !errors.Is(err, ErrConflict) {
		t.Fatal("overwrote published answer")
	}

	continuationReq := AutomaticGoal{Key: "continue-answer", ProjectID: req.ProjectID, Goal: "Use the previous answer for the next task", FromGraph: g.ID, FromRevision: published.Revision}
	staleContinuation := continuationReq
	staleContinuation.Key = "stale-continuation"
	staleContinuation.FromRevision--
	if _, err = e.StartAutomaticGoal(staleContinuation); !errors.Is(err, ErrConflict) {
		t.Fatal("stale continuation accepted")
	}
	continuation, err := e.StartAutomaticGoal(continuationReq)
	if err != nil || continuation.Predecessor != g.ID || continuation.GoalRevision != g.GoalRevision+1 {
		t.Fatalf("continuation: %v", err)
	}
	replayContinuation, err := e.StartAutomaticGoal(continuationReq)
	if err != nil || replayContinuation.ID != continuation.ID {
		t.Fatal("continuation was duplicated")
	}
	var prior FollowupContext
	if err = json.Unmarshal([]byte(continuation.Members[continuation.RootTask].Spec.Context), &prior); err != nil || prior.Result.Markdown != published.Result.Markdown {
		t.Fatal("previous result was not frozen")
	}
	notes, err := e.prepareFollowup(Run{GraphAttempt: &GraphAttempt{GraphID: continuation.ID}}, t.TempDir())
	if err != nil || !strings.Contains(notes, "Recovered answer") || !strings.Contains(notes, "workspace.tar.gz") {
		t.Fatalf("saved evidence handoff: %s %v", notes, err)
	}
	if _, err = os.Stat(filepath.Join(e.Root, "answer.txt")); !os.IsNotExist(err) {
		t.Fatal("agent modified original project")
	}
}

func TestAutomaticMissingRuntimeAndExplicitUpgrade(t *testing.T) {
	e, req := graphEngine(t, false)
	t.Setenv("PATH", "") // Registry captures its own environment; force unavailable adapter below.
	e.Desk.Agents = nil
	// A missing registry must be reported without creating inert Planning work.
	if _, err := e.StartAutomaticGoal(AutomaticGoal{Key: "no-runtime", ProjectID: req.ProjectID, Goal: "Do some work"}); err == nil {
		t.Fatal("missing runtime admitted")
	}
	if len(e.Store.Graphs()) != 0 {
		t.Fatal("created an inert goal")
	}
}

// Opt-in integration with an installed, authenticated Codex. No fixed replies or
// scripted orchestrator are involved; artifacts and graph acceptance must be real.
func TestAutomaticInstalledCodex(t *testing.T) {
	if os.Getenv("MITOS_TEST_NATIVE_CODEX") != "1" {
		t.Skip("set MITOS_TEST_NATIVE_CODEX=1 for the authenticated native CLI smoke test")
	}
	e, req := graphEngine(t, true)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "native-smoke", ProjectID: req.ProjectID, Goal: "Use exactly one researcher agent to read hello.txt and report its exact contents. Do not edit any files. The orchestrator should review that report and finish; no additional workers are needed."})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(150 * time.Second)
	for time.Now().Before(until) {
		g, err = e.Store.Graph(g.ID)
		if err != nil {
			t.Fatal(err)
		}
		if g.Status == "accepted" {
			t.Logf("Native Codex finished with %d attempts and %d orchestrator calls", g.Attempts, g.OrchestratorCalls)
			return
		}
		for _, x := range g.Expansions {
			if x.Error != "" {
				for _, m := range g.Members {
					if m.CurrentRunID != "" {
						r, _ := e.Store.Get(m.CurrentRunID)
						t.Logf("%s: %s %s", r.AppName, r.Status, r.Summary)
						b, _ := e.Store.Artifact(r.ID, "output.txt")
						if len(b) > 4000 {
							b = b[len(b)-4000:]
						}
						t.Log(string(b))
					}
				}
				t.Fatal(x.Error)
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("native workflow did not complete within 150 seconds")
}

func automaticScriptEngine(t *testing.T, script string) (*Engine, CreateGraph) {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required")
	}
	bin := t.TempDir()
	if err = os.WriteFile(filepath.Join(bin, "codex"), []byte("#!"+python+"\n"+appServerFixture(script)), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return graphEngine(t, true)
}
func TestAutomaticRejectsEmptyCompletionAndBoundsRepairs(t *testing.T) {
	e, req := automaticScriptEngine(t, `import json,pathlib,sys
sys.stdin.read()
pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1]).write_text(json.dumps({'explanation':'Everything is finished','operations':[],'accept':[],'complete':True}))
`)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "invalid-completion", ProjectID: req.ProjectID, Goal: "Require actual worker evidence"})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		g, _ = e.Store.Graph(g.ID)
		if g.Expansions["initial"].Error != "" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if g.Status == "accepted" || g.OrchestratorCalls != 3 || g.Expansions["initial"].Error == "" {
		t.Fatalf("invalid completion not bounded: status %s calls %d error %s", g.Status, g.OrchestratorCalls, g.Expansions["initial"].Error)
	}
	if len(g.Receipts) != 0 {
		t.Fatal("rejected proposal left a receipt")
	}
}
func TestAutomaticStopHarvestsPartialOutput(t *testing.T) {
	e, req := automaticScriptEngine(t, `import sys,time,pathlib
sys.stdin.read()
print('Started native orchestrator',flush=True)
pathlib.Path("started.txt").write_text("ready")
time.sleep(60)
`)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "stop-native", ProjectID: req.ProjectID, Goal: "Stop a running orchestrator"})
	if err != nil {
		t.Fatal(err)
	}
	var runID string
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		g, _ = e.Store.Graph(g.ID)
		for _, m := range g.Members {
			if m.State == "running" {
				runID = m.CurrentRunID
			}
		}
		if runID != "" {
			if _, err := os.Stat(filepath.Join(e.Store.Dir(), "workspaces", runID, "workspace", "started.txt")); err == nil {
				break
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if _, err := os.Stat(filepath.Join(e.Store.Dir(), "workspaces", runID, "workspace", "started.txt")); err != nil {
		t.Fatal("orchestrator never started")
	}
	time.Sleep(150 * time.Millisecond)
	g, _ = e.Store.Graph(g.ID)
	if _, _, err = e.AmendGraph(proposal(g, "stop", GraphOperation{Kind: "cancel", Task: g.RootTask, Rationale: "User requested a stop"})); err != nil {
		t.Fatal(err)
	}
	until = time.Now().Add(5 * time.Second)
	var r Run
	for time.Now().Before(until) {
		r, _ = e.Store.Get(runID)
		if r.Terminal() {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if r.Status != "cancelled" || r.Claim != nil && r.Claim.ReleasedAt == nil {
		t.Fatalf("stop failed: %s", r.Status)
	}
	output, err := e.Store.Artifact(runID, "output.txt")
	if err != nil || !strings.Contains(string(output), "Started native orchestrator") {
		t.Fatalf("partial output lost: %v", err)
	}
}
func TestAutomaticRepairsFailedWorker(t *testing.T) {
	e, req := automaticScriptEngine(t, `import sys,json,pathlib
prompt=sys.stdin.read();out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
if '--output-schema' in sys.argv:
 ctx=json.loads(prompt.split('\nGraph context:\n')[1].split('\nSaved upstream evidence')[0]);workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 p={'explanation':'Inspect evidence and repair the failed attempt.','operations':[],'accept':[],'complete':False}
 if not workers:p['operations']=[{'kind':'add','spec':{'id':'worker','parent':ctx['root_task'],'kind':'application','required':True,'acceptance':'operator','criteria':'Report 42','brief':{'title':'Produce answer','intent':'Produce an answer','app_id':'agent:'+ ('mitos.generalist'),'timeout_minutes':30,'scheduling':{'deps':[{'from':ref,'guard':'candidate_available','payload':'data','artifacts':['report.md','changes.patch']} for ref in ([])]}},'agent':({'role':'','instructions':''} if ('') else None)}}]
 elif workers[0]['state']=='failed':p['operations']=[{'kind':'retry','task':workers[0]['id']}]
 else:p['accept']=[{'task':workers[0]['id'],'rationale':'The saved report contains 42.'}];p['complete']=True
 if p['complete']:p['result']={'title':'Answer','markdown':'The answer is 42.','source_tasks':[workers[0]['id']]}
 out.write_text(json.dumps(p))
else:
 flag=pathlib.Path(__file__+'.attempted')
 if not flag.exists():flag.touch();print('Transient worker failure',flush=True);sys.exit(1)
 out.write_text('The answer is 42.')
`)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "retry-native", ProjectID: req.ProjectID, Goal: "Produce answer with recovery"})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(15 * time.Second)
	for time.Now().Before(until) {
		g, _ = e.Store.Graph(g.ID)
		if g.Status == "accepted" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if g.Status != "accepted" {
		t.Fatal("failed worker was not repaired")
	}
	for _, m := range g.Members {
		if m.OrchestratorFor == "" && m.Spec.Kind == "application" {
			if len(m.Attempts) != 2 {
				t.Fatalf("attempt count %d", len(m.Attempts))
			}
			r, _ := e.Store.Get(m.Attempts[0])
			if r.Status != "failed" {
				t.Fatal("prior failure lost")
			}
		}
	}
}

// Existing workflow fixtures describe task behavior. This wrapper exercises the
// actual app-server wire boundary, including final items and turn acknowledgments.
func appServerFixture(script string) string {
	encoded, _ := json.Marshal(script)
	return `import sys,json,io,tempfile,pathlib,contextlib
script=` + string(encoded) + `
wire=sys.stdin
outwire=sys.stdout
def send(value):
 outwire.write(json.dumps(value)+'\n');outwire.flush()
for line in wire:
 req=json.loads(line);method=req.get('method','')
 if method=='initialize':send({'id':req['id'],'result':{}})
 elif method=='thread/start':send({'id':req['id'],'result':{'thread':{'id':'fixture-thread'},'model':'fixture'}})
 elif method=='turn/start':
  send({'id':req['id'],'result':{'turn':{'id':'fixture-turn'}}})
  params=req['params'];prompt=params['input'][0]['text']
  with tempfile.TemporaryDirectory() as tmp:
   result=pathlib.Path(tmp,'result.txt')
   oldargv=sys.argv;oldin=sys.stdin
   sys.argv=[oldargv[0],'exec','--output-last-message',str(result)]
   if 'outputSchema' in params:sys.argv+=['--output-schema','fixture-schema']
   sys.stdin=io.StringIO(prompt)
   try:
    with contextlib.redirect_stdout(sys.stderr):exec(compile(script,__file__,'exec'),{'__file__':__file__})
   finally:sys.argv=oldargv;sys.stdin=oldin
   if result.exists():
    text=result.read_text()
    send({'method':'item/agentMessage/delta','params':{'delta':text}})
    send({'method':'item/completed','params':{'threadId':'fixture-thread','turnId':'fixture-turn','item':{'id':'final','type':'agentMessage','phase':'final_answer','text':text}}})
  send({'method':'turn/completed','params':{'threadId':'fixture-thread','turn':{'id':'fixture-turn','status':'completed'}}})
`
}
