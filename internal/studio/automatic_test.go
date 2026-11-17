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
 ctx=json.loads(section(prompt,'graph_state'))
 workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 plan={'explanation':'Evidence meets the goal.','operations':[],'accept':[],'complete':bool(workers)}
 if not workers:
  for i in range(2):
   plan['operations'].append({'kind':'add','spec':{'id':'work'+str(i),'parent':ctx['root_task'],'kind':'application','traits':{'nature':'','size':'small','intelligence':'low'},'required':True,'acceptance':'operator','criteria':'Produce evidence for the answer.','brief':{'title':['Implement change','Review change'][i],'intent':['Create answer.txt with 42.','Check upstream patch and evidence.'][i],'app_id':'agent:'+ (['mitos.engineer','mitos.reviewer'][i]),'timeout_minutes':30,'scheduling':{'deps':[{'from':ref,'guard':'candidate_available','payload':'data','artifacts':['report.md','changes.patch']} for ref in (['work0'] if i else [])]}},'agent':({'role':'Answer specialist' if i else '','instructions':'Check the answer carefully.' if i else ''} if ('Answer specialist' if i else '') else None)}})
 else:
  inputs=json.loads(section(prompt,'selected_artifacts','[]'))
  assert len(inputs)==0,inputs
  for f in workers:
   page=tool('artifact_list',{'run_id':f['run_id'],'cursor':0,'limit':32})
   ref=next(a for a in page['artifacts'] if a['role']=='report')
   read=tool('artifact_read',{'run_id':f['run_id'],'artifact_id':ref['id'],'offset':0,'limit':32768})
   assert read['complete'],read
  assert len(workers)==2
  for f in workers:
   assert f['state']=='done',str(f)
   plan['accept'].append({'task':f['id'],'rationale':'Read the saved native report and reviewed its evidence.'})
 plan['result']={'title':'Answer','markdown':'The answer is **42**.\n\n- [ ] Example open finding.\n\n[Answer file](mitos://'+workers[0]['id']+'/answer.txt) and [source](mitos://'+workers[0]['id']+'/hello.txt#L1).','source_tasks':[f['id'] for f in workers]} if workers else {'title':'','markdown':'','source_tasks':[]}
 out.write_text(json.dumps(plan))
else:
 if 'Check upstream patch and evidence.' in prompt:
  inputs=json.loads(section(prompt,'selected_artifacts','[]'))
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
			activity, err := e.Store.Artifact(r.ID, "activity.jsonl")
			if err != nil || !strings.Contains(string(activity), `"kind":"message"`) {
				t.Fatalf("public activity not saved: %s, %v", activity, err)
			}
			raw, _ := e.Store.Artifact(r.ID, "output.txt")
			if strings.Contains(string(raw), `"mitos_activity":1`) {
				t.Fatal("host records leaked into raw log")
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
	goal := "Use exactly one researcher agent to read hello.txt and report its exact contents. Do not edit any files. The orchestrator should review that report and finish; no additional workers are needed."
	if os.Getenv("MITOS_TEST_HANDOFF_LARGE") == "1" {
		goal = "Use exactly one engineer worker to create audit-report.md in its workspace: first line 'Verified finding 48', followed by at least 120000 lines saying 'Supporting evidence'. Register that report using artifact_register, submit a typed result, and finish. The orchestrator must inspect result_get and artifact_list, search for the finding, read only its small relevant range, and accept without creating another worker. The report and harvested patch must both exceed 1 MiB. This is a protocol smoke test; no external actions are needed."
	}
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "native-smoke", ProjectID: req.ProjectID, Goal: goal})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(240 * time.Second)
	for time.Now().Before(until) {
		g, err = e.Store.Graph(g.ID)
		if err != nil {
			t.Fatal(err)
		}
		if g.Status == "accepted" {
			t.Logf("Native Codex finished with %d attempts and %d orchestrator calls", g.Attempts, g.OrchestratorCalls)
			if os.Getenv("MITOS_TEST_HANDOFF_LARGE") == "1" {
				workers := 0
				toolsSeen := map[string]bool{}
				var readBytes int64
				for _, member := range g.Members {
					run, err := e.Store.Get(member.CurrentRunID)
					if err != nil {
						continue
					}
					if member.OrchestratorFor != "" {
						readBytes += run.HandoffBytes
						activity, _ := e.Store.Artifact(run.ID, "activity.jsonl")
						for _, line := range strings.Split(string(activity), "\n") {
							var record struct {
								Kind string `json:"kind"`
								Name string `json:"name"`
							}
							if json.Unmarshal([]byte(line), &record) == nil && record.Kind == "tool" {
								toolsSeen[record.Name] = true
							}
						}
						continue
					}
					if member.Spec.Kind != "application" {
						continue
					}
					workers++
					if run.Result == nil || member.Accepted == nil || member.Accepted.ResultID != run.Result.ID {
						t.Fatal("missing accepted typed result")
					}
					var reportBytes, patchBytes int64
					for _, a := range run.Artifacts {
						if a.Role == "report" {
							reportBytes = max(reportBytes, a.Bytes)
						}
						if a.Name == "changes.patch" {
							patchBytes = a.Bytes
						}
					}
					if reportBytes <= 1<<20 || patchBytes <= 1<<20 {
						t.Fatal("large evidence was not produced", reportBytes, patchBytes)
					}
					t.Logf("Retained report %d bytes; patch %d bytes", reportBytes, patchBytes)
				}
				if workers != 1 || g.Attempts != 3 || g.OrchestratorCalls != 2 || readBytes == 0 || readBytes > HandoffRetrievalBudget {
					t.Fatal("unexpected workflow or evidence reads", workers, g.Attempts, g.OrchestratorCalls, readBytes)
				}
				for _, name := range []string{"result_get", "artifact_list", "artifact_search", "artifact_read"} {
					if !toolsSeen[name] {
						t.Fatal("review did not call", name, toolsSeen)
					}
				}
				t.Logf("Reviewer consumed %d retrieval bytes; tools %v; no operator interventions", readBytes, toolsSeen)
			}
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
	t.Fatal("native workflow did not complete within 240 seconds")
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
 ctx=json.loads(section(prompt,'graph_state'));workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 p={'explanation':'Inspect evidence and repair the failed attempt.','operations':[],'accept':[],'complete':False}
 if not workers:p['operations']=[{'kind':'add','spec':{'id':'worker','parent':ctx['root_task'],'kind':'application','traits':{'nature':'','size':'small','intelligence':'low'},'required':True,'acceptance':'operator','criteria':'Report 42','brief':{'title':'Produce answer','intent':'Produce an answer','app_id':'agent:'+ ('mitos.generalist'),'timeout_minutes':30,'scheduling':{'deps':[{'from':ref,'guard':'candidate_available','payload':'data','artifacts':['report.md','changes.patch']} for ref in ([])]}},'agent':({'role':'','instructions':''} if ('') else None)}}]
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

// A native reviewer that rewrites its bound, read-only dependency copy after
// reading it must park with a durable integrity reason, keep its evidence, and
// block every acceptance path for the graph.
func TestAutomaticRejectsMutatedNativeDependencyCopies(t *testing.T) {
	e, req := automaticScriptEngine(t, `import json,pathlib,sys,os
prompt=sys.stdin.read()
out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
if '--output-schema' in sys.argv:
 ctx=json.loads(section(prompt,'graph_state'))
 workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 plan={'explanation':'Evidence meets the goal.','operations':[],'accept':[],'complete':False}
 if not workers:
  for i in range(2):
   plan['operations'].append({'kind':'add','spec':{'id':'work'+str(i),'parent':ctx['root_task'],'kind':'application','traits':{'nature':'','size':'small','intelligence':'low'},'required':True,'acceptance':'operator','criteria':'Produce evidence for the answer.','brief':{'title':['Implement change','Review change'][i],'intent':['Create answer.txt with 42.','Check upstream patch and evidence.'][i],'app_id':'agent:'+ (['mitos.engineer','mitos.reviewer'][i]),'timeout_minutes':30,'scheduling':{'deps':[{'from':ref,'guard':'candidate_available','payload':'data','artifacts':['report.md','changes.patch']} for ref in (['work0'] if i else [])]}},'agent':({'role':'Answer specialist' if i else '','instructions':'Check the answer carefully.' if i else ''} if ('Answer specialist' if i else '') else None)}})
 out.write_text(json.dumps(plan))
else:
 if 'Check upstream patch and evidence.' in prompt:
  inputs=json.loads(section(prompt,'selected_artifacts','[]'))
  assert len(inputs)==1,inputs
  bound=pathlib.Path(inputs[0]['directory'],'report.md')
  assert 'Result' in bound.read_text()
  os.chmod(bound,0o644)
  bound.write_text('AUDIT: mutated dependency after materialization')
 else:
  pathlib.Path('answer.txt').write_text('42\n')
 out.write_text('# Result\n\nThe answer is **42**.\n\nEvidence checked.')
print('native execution log')
`)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "mutated-dependency", ProjectID: req.ProjectID, Goal: "Produce and review the answer"})
	if err != nil {
		t.Fatal(err)
	}
	unified := time.Now().Add(90 * time.Second)
	var reviewer Run
	for time.Now().Before(unified) {
		g, _ = e.Store.Graph(g.ID)
		for _, m := range g.Members {
			if m.OrchestratorFor != "" || m.Spec.Kind != "application" || m.Spec.Brief.Title != "Review change" || m.CurrentRunID == "" {
				continue
			}
			r, _ := e.Store.Get(m.CurrentRunID)
			if r.Status == "parked" {
				reviewer = r
			}
		}
		if reviewer.ID != "" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if reviewer.ID == "" {
		t.Fatal("mutated dependency copy was accepted or lost")
	}
	if reviewer.Decision == nil || reviewer.Decision.ReasonSignal != "dependency_inputs_changed" {
		t.Fatalf("integrity reason missing: %+v", reviewer.Decision)
	}
	for _, name := range []string{"output.txt", "report.md", "changes.patch", "workspace.tar.gz", "dependency-inputs.json"} {
		if _, err := e.Store.Artifact(reviewer.ID, name); err != nil {
			t.Fatalf("evidence %s not retained: %v", name, err)
		}
	}
	bound, err := filepath.Glob(filepath.Join(e.Store.Dir(), "workspaces", reviewer.ID, "inputs", "*", "report.md"))
	if err != nil || len(bound) != 1 {
		t.Fatalf("bound input copy not found: %v %v", bound, err)
	}
	mutated, err := os.ReadFile(bound[0])
	if err != nil || string(mutated) != "AUDIT: mutated dependency after materialization" {
		t.Fatalf("mutated copy not inspectable: %q %v", mutated, err)
	}
	g, _ = e.Store.Graph(g.ID)
	if g.Status == "accepted" {
		t.Fatal("graph accepted a member with changed dependency inputs")
	}
	var reviewerID string
	for id, m := range g.Members {
		if m.OrchestratorFor == "" && m.Spec.Kind == "application" && m.Spec.Brief.Title == "Review change" {
			reviewerID = id
		}
	}
	if reviewerID == "" {
		t.Fatal("reviewer member missing")
	}
	// Acceptance proposals carry the matching read witnesses so the requests
	// exercise the acceptance guards themselves, not proposal read fencing.
	memberAccept := proposal(g, "accept-mutated", GraphOperation{Kind: "accept", Task: reviewerID, Evidence: "mutated evidence", Rationale: "reviewed"})
	if m := g.Members[reviewerID]; m != nil {
		memberAccept.Reads = map[string]GraphRead{reviewerID: {Specification: m.Revision, Fact: m.FactRevision}}
	}
	if _, _, err = e.AmendGraph(memberAccept); err == nil || errors.Is(err, ErrConflict) {
		t.Fatalf("operator accepted a member with changed dependency inputs: %v", err)
	}
	rootAccept := proposal(g, "accept-root", GraphOperation{Kind: "accept", Task: g.RootTask, Evidence: "mutated evidence", Rationale: "reviewed"})
	rootAccept.Reads = map[string]GraphRead{}
	for tid, m := range g.Members {
		if tid == g.RootTask || graphDescendant(&g, tid, g.RootTask) {
			rootAccept.Reads[tid] = GraphRead{Specification: m.Revision, Fact: m.FactRevision}
		}
	}
	if _, _, err = e.AmendGraph(rootAccept); err == nil || errors.Is(err, ErrConflict) {
		t.Fatalf("root accepted while a required member is unaccepted: %v", err)
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
toolseq=1000
def tool(name,arguments):
 global toolseq
 toolseq+=1
 send({'id':toolseq,'method':'item/tool/call','params':{'threadId':'fixture-thread','turnId':'fixture-turn','callId':str(toolseq),'tool':name,'arguments':arguments}})
 reply=json.loads(wire.readline())['result']
 assert reply['success'],reply
 return json.loads(reply['contentItems'][0]['text'])
def send(value):
 outwire.write(json.dumps(value)+'\n');outwire.flush()
def section(prompt,kind,default=None):
 # Context sections render as '## <kind> (<source>)' followed by their text.
 for block in prompt.split('\n## ')[1:]:
  head,_,body=block.partition('\n')
  if head.split(' (')[0]==kind:return body.split('\n## ')[0].rstrip('\n')
 if default is None:raise KeyError(kind)
 return default
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
    with contextlib.redirect_stdout(sys.stderr):exec(compile(script,__file__,'exec'),{'__file__':__file__,'tool':tool,'section':section})
   finally:sys.argv=oldargv;sys.stdin=oldin
   if result.exists():
    text=result.read_text()
    if 'outputSchema' not in params and 'fixture-no-submit' not in prompt and 'fixture-manual-submit' not in prompt:
     pathlib.Path('fixture-report.md').write_text(text)
     ref=tool('artifact_register',{'path':'fixture-report.md','role':'report'})
     pathlib.Path('fixture-report.md').unlink()
     tool('result_submit',{'version':'mitos.handoff/v1','key':'fixture-result','summary':text[:400],'artifacts':[{'artifact_id':ref['id'],'role':'report'}],'checks':[],'limitations':[],'unresolved_requests':[]})
    send({'method':'item/agentMessage/delta','params':{'delta':text}})
    send({'method':'item/completed','params':{'threadId':'fixture-thread','turnId':'fixture-turn','item':{'id':'final','type':'agentMessage','phase':'final_answer','text':text}}})
  send({'method':'turn/completed','params':{'threadId':'fixture-thread','turn':{'id':'fixture-turn','status':'completed'}}})
`
}

func TestAutomaticLargeEvidenceUsesBoundedTools(t *testing.T) {
	script := `import json,pathlib,sys,base64
prompt=sys.stdin.read()
out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
if '--output-schema' in sys.argv:
 ctx=json.loads(section(prompt,'graph_state'))
 workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 plan={'operations':[],'accept':[],'complete':bool(workers),'explanation':'Read retained report through bounded tools.','result':{'title':'','markdown':'','source_tasks':[],'items':[]}}
 if not workers:
  plan['operations']=[{'kind':'add','spec':{'id':'audit','parent':ctx['root_task'],'kind':'application','traits':{'nature':'','size':'small','intelligence':'low'},'required':True,'acceptance':'operator','criteria':'Produce an independently readable large report.','brief':{'title':'Audit evidence','intent':'fixture-manual-submit: produce report','app_id':'agent:mitos.engineer','timeout_minutes':1}}}]
 else:
  assert len(workers)==1,workers
  worker=workers[0]
  receipt=tool('result_get',{'run_id':worker['run_id'],'cursor':0,'limit':16})
  assert receipt['state']=='candidate',receipt
  page=tool('artifact_list',{'run_id':worker['run_id'],'cursor':0,'limit':32})
  report=next(a for a in page['artifacts'] if a['role']=='report')
  patch=next(a for a in page['artifacts'] if a['name']=='changes.patch')
  assert report['bytes']>1048576 and patch['bytes']>1048576,page
  matches=tool('artifact_search',{'run_id':worker['run_id'],'artifact_id':report['id'],'query':'verified finding 48','cursor':0,'limit':4})
  assert matches['offsets']==[0],matches
  data=tool('artifact_read',{'run_id':worker['run_id'],'artifact_id':report['id'],'offset':0,'limit':128})
  assert not data['complete'] and data['next_offset']==128,data
  assert base64.b64decode(data['data']).startswith(b'verified finding 48')
  plan['accept']=[{'task':worker['id'],'rationale':'Read exact registered report and verified finding.'}]
  plan['result']={'title':'Audit','markdown':'Verified finding 48. Large report inspected with bounded tools.','source_tasks':[worker['id']],'items':[]}
 out.write_text(json.dumps(plan))
else:
 pathlib.Path('report.md').write_text('verified finding 48\n'+'supporting audit evidence\n'*60000)
 a=tool('artifact_register',{'path':'report.md','role':'report'})
 tool('result_submit',{'version':'mitos.handoff/v1','key':'audit','summary':'Verified finding 48; full report retained.','artifacts':[{'artifact_id':a['id'],'role':'report'}],'checks':[],'limitations':['Only the fixture finding is verified.'],'unresolved_requests':[]})
 out.write_text('Audit complete; see registered report.')
`
	e, req := automaticScriptEngine(t, script)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "large-handoff", ProjectID: req.ProjectID, Goal: "fixture-manual-submit: audit and review large evidence"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		current, _ := e.Store.Graph(g.ID)
		if current.Status == "accepted" {
			if current.Attempts != 3 || current.OrchestratorCalls != 2 {
				t.Fatal("extra copying worker", current.Attempts, current.OrchestratorCalls)
			}
			return
		}
		for _, x := range current.Expansions {
			if x.Error != "" {
				for _, m := range current.Members {
					if r, err := e.Store.Get(m.CurrentRunID); err == nil {
						t.Log(r.Summary)
						data, _ := e.Store.Artifact(r.ID, "output.txt")
						t.Log(string(data))
					}
				}
				t.Fatal(x.Error)
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("large evidence review did not complete")
}
