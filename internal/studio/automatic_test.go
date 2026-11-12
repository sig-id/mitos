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
// role, independent planner review, and completion without operator amendments.
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
 plan={'explanation':'Evidence meets the goal.','steps':[],'accept':[],'retry':[],'complete':bool(workers)}
 if not workers:
  for i in range(2):
   plan['steps'].append({'id':'work'+str(i),'title':['Implement change','Review change'][i],'assignment':['Create answer.txt with 42.','Check upstream patch and evidence.'][i],'criteria':'Produce evidence for the answer.','agent':['mitos.engineer','mitos.reviewer'][i],'role':'Answer specialist' if i else '', 'instructions':'Check the answer carefully.' if i else '', 'after':['work0'] if i else [],'replaces':''})
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
	if err = os.WriteFile(filepath.Join(bin, "codex"), []byte("#!"+python+"\n"+script), 0700); err != nil {
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
				t.Fatalf("planner: %s", x.Error)
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
		if m.ControllerFor == "" && m.Spec.Kind == "application" {
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
			for _, path := range []string{"../answer.txt", "/etc/passwd", "hello.txt"} {
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
// scripted planner are involved; artifacts and graph acceptance must be real.
func TestAutomaticInstalledCodex(t *testing.T) {
	if os.Getenv("MITOS_TEST_NATIVE_CODEX") != "1" {
		t.Skip("set MITOS_TEST_NATIVE_CODEX=1 for the authenticated native CLI smoke test")
	}
	e, req := graphEngine(t, true)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "native-smoke", ProjectID: req.ProjectID, Goal: "Use exactly one researcher agent to read hello.txt and report its exact contents. Do not edit any files. The planner should review that report and finish; no additional workers are needed."})
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
			t.Logf("Native Codex finished with %d attempts and %d planner calls", g.Attempts, g.ControllerCalls)
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
	if err = os.WriteFile(filepath.Join(bin, "codex"), []byte("#!"+python+"\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return graphEngine(t, true)
}
func TestAutomaticRejectsEmptyCompletionAndBoundsRepairs(t *testing.T) {
	e, req := automaticScriptEngine(t, `import json,pathlib,sys
sys.stdin.read()
pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1]).write_text(json.dumps({'explanation':'Everything is finished','steps':[],'accept':[],'retry':[],'complete':True}))
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
	if g.Status == "accepted" || g.ControllerCalls != 3 || g.Expansions["initial"].Error == "" {
		t.Fatalf("invalid completion not bounded: status %s calls %d error %s", g.Status, g.ControllerCalls, g.Expansions["initial"].Error)
	}
	if len(g.Receipts) != 0 {
		t.Fatal("rejected proposal left a receipt")
	}
}
func TestAutomaticStopHarvestsPartialOutput(t *testing.T) {
	e, req := automaticScriptEngine(t, `import sys,time,pathlib
sys.stdin.read()
print('Started native planner',flush=True)
pathlib.Path("started.txt").write_text("ready")
time.sleep(60)
`)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "stop-native", ProjectID: req.ProjectID, Goal: "Stop a running planner"})
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
		t.Fatal("planner never started")
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
	if err != nil || !strings.Contains(string(output), "Started native planner") {
		t.Fatalf("partial output lost: %v", err)
	}
}
func TestAutomaticRepairsFailedWorker(t *testing.T) {
	e, req := automaticScriptEngine(t, `import sys,json,pathlib
prompt=sys.stdin.read();out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
if '--output-schema' in sys.argv:
 ctx=json.loads(prompt.split('\nGraph context:\n')[1].split('\nSaved upstream evidence')[0]);workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 p={'explanation':'Inspect evidence and repair the failed attempt.','steps':[],'accept':[],'retry':[],'complete':False}
 if not workers:p['steps']=[{'id':'worker','title':'Produce answer','assignment':'Produce an answer','criteria':'Report 42','agent':'mitos.generalist','role':'','instructions':'','after':[],'replaces':''}]
 elif workers[0]['state']=='failed':p['retry']=[workers[0]['id']]
 else:p['accept']=[{'task':workers[0]['id'],'rationale':'The saved report contains 42.'}];p['complete']=True
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
		if m.ControllerFor == "" && m.Spec.Kind == "application" {
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
