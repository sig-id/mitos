package studio

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Real scheduler + app-server wire: a worker yields with a blocking proposal,
// the orchestrator admits the work, and the worker resumes its own native
// thread with the accepted result.
func TestAutomaticYieldProposalContinuation(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required")
	}
	bin := t.TempDir()
	script := `import json,pathlib,sys
prompt=sys.stdin.read()
out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
def spec(ref,intent,root):
 return {'id':ref,'parent':root,'kind':'application','traits':{'nature':'','size':'small','intelligence':'low'},'required':True,'acceptance':'operator','criteria':'Answer','brief':{'title':ref,'intent':intent,'app_id':'agent:mitos.generalist','timeout_minutes':30,'scheduling':{'deps':[]}}}
if '--output-schema' in sys.argv:
 ctx=json.loads(section(prompt,'graph_state'));root=ctx['root_task']
 latest={}
 for e in ctx['journal']:latest[e['task']]=e
 workers=[e for e in latest.values() if e['kind']=='application']
 plan={'explanation':'Coordinate.','operations':[],'accept':[],'complete':False,'result':{'title':'','markdown':'','source_tasks':[]}}
 pending=[p for p in ctx['proposals']]
 # The bounded journal carries no titles: identify the helper by its submitted
 # answer and the lead as the proposal's proposing task.
 helper=[e for e in workers if 'sub-answer' in e.get('summary','')]
 if not workers:
  plan['operations']=[{'kind':'add','spec':spec('lead','LEAD fixture-manual-submit',root)}]
 elif pending:
  p=pending[0]
  lead=latest[p['task']]
  assert p['detail']['blocking'] and lead['state']=='yielded',str(pending)+str(workers)
  plan['operations']=[{'kind':'add','spec':spec('helper','HELPER',root)},{'kind':'accept_proposal','proposal':p['id'],'dependencies':[{'from':'helper','guard':'done','payload':'data','artifacts':['report.md']}],'rationale':'A helper can answer that.'}]
 elif helper and not helper[0].get('accepted'):
  assert helper[0]['state']=='done',str(helper)
  plan['accept']=[{'task':helper[0]['task'],'rationale':'Helper answered.'}]
 else:
  lead=[e for e in workers if e['task']!=helper[0]['task']]
  assert len(lead)==1 and lead[0]['state']=='done',str(workers)
  plan['accept']=[{'task':lead[0]['task'],'rationale':'Lead integrated the answer.'}];plan['complete']=True
  plan['result']={'title':'Answer','markdown':'The answer is 7.','source_tasks':[lead[0]['task'],helper[0]['task']]}
 out.write_text(json.dumps(plan))
elif 'HELPER' in prompt:
 pathlib.Path(ARGV_RECORD).write_text(json.dumps(sys.modules['__main__'].oldargv))
 out.write_text('The sub-answer is 7.')
else:
 pathlib.Path('fixture-report.md').write_text('lead report')
 ref=tool('artifact_register',{'path':'fixture-report.md','role':'report'})
 pathlib.Path('fixture-report.md').unlink()
 sub={'version':'mitos.handoff/v1','key':'lead','summary':'s','artifacts':[{'artifact_id':ref['id'],'role':'report'}],'checks':[],'limitations':[],'unresolved_requests':[],'proposals':[]}
 if 'yielded on blocking proposals' in prompt:
  assert '"outcome":"accepted"' in prompt.replace(' ',''),prompt
  sub['key']='lead-final';sub['summary']='Integrated the helper answer 7.'
  tool('result_submit',sub);out.write_text('Integrated the helper answer 7.')
 else:
  sub['proposals']=[{'id':'ask-helper','rationale':'Needs a separate sub-answer','blocking':True,'tasks':[spec('helper','HELPER','')],'artifacts':[ref['id']]}]
  receipt=tool('result_submit',sub)
  assert len(receipt['proposal_ids'])==1,receipt
  out.write_text('Yielding to a helper.')
`
	argvRecord := filepath.Join(bin, "helper-argv.json")
	argvJSON, _ := json.Marshal(argvRecord)
	script = strings.Replace(script, "ARGV_RECORD", string(argvJSON), 1)
	fixture := appServerFixture(script)
	fixture = strings.Replace(fixture, "elif method=='thread/start':", "elif method in ('thread/start','thread/resume'):", 1)
	wireLog := filepath.Join(bin, "native-requests.jsonl")
	wireJSON, _ := json.Marshal(wireLog)
	fixture = strings.Replace(fixture, " req=json.loads(line);method=req.get('method','')", ` req=json.loads(line);method=req.get('method','')
 with open(`+string(wireJSON)+`, 'a') as log:log.write(json.dumps(req)+'\n')`, 1)
	if err = os.WriteFile(filepath.Join(bin, "codex"), []byte("#!"+python+"\n"+fixture), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, req := graphEngine(t, true)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "yield-e2e", ProjectID: req.ProjectID, Goal: "Answer with help"})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		g, _ = e.Store.Graph(g.ID)
		if g.Status != "active" {
			break
		}
		for _, x := range g.Expansions {
			if x.Error != "" && x.OrchestratorTask == "" {
				t.Fatal(x.ID, x.Error, x.LastError)
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	if g.Status != "accepted" {
		blockers, _ := e.Store.GraphBlockers(g.ID)
		for _, x := range g.Expansions {
			t.Log(x.ID, x.Error, x.LastError)
		}
		t.Fatal("yield workflow did not complete", g.Status, blockers)
	}
	var lead *GraphMember
	for _, m := range g.Members {
		if m.Spec.Brief.Title == "lead" {
			lead = m
		}
	}
	if lead == nil || lead.Yields != 1 || len(lead.Attempts) != 2 || len(g.Proposals) != 1 {
		t.Fatalf("lead did not yield once and continue: %+v", lead)
	}
	for _, rec := range g.Proposals {
		if rec.Outcome != "accepted" || rec.Local != "ask-helper" {
			t.Fatal("proposal record", rec)
		}
	}
	continued, _ := e.Store.Get(lead.Attempts[1])
	if continued.GraphAttempt.ContinueFrom != lead.Attempts[0] || continued.Result == nil || continued.Result.Submission.Key != "lead-final" {
		t.Fatal("continuation did not run on the yielded attempt", continued.GraphAttempt, continued.Result)
	}
	// Calibration attributes the helper to the worker proposal that asked for it.
	groups, err := e.Store.ExecutionCalibration(req.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	requesters := map[string]int{}
	for _, c := range groups {
		requesters[c.Requester] += c.Attempts
		if c.Size != "small" || c.Intelligence != "low" || c.Tier != "low" || c.BudgetMinutes != 15 {
			t.Fatalf("calibration group: %+v", c)
		}
	}
	if requesters["orchestrator"] != 2 || requesters["proposal:mitos.generalist"] != 1 {
		t.Fatalf("calibration requesters: %v", requesters)
	}
	if b := continued.GraphAttempt.Executor.Application.Agent; b.Effort != "low" || b.Selection == nil || b.Selection.Traits.Nature != "implement" {
		t.Fatalf("binding lost the low-intelligence selection: %+v", b)
	}
	if argv, err := os.ReadFile(argvRecord); err != nil || !strings.Contains(string(argv), `model_reasoning_effort=\"low\"`) {
		t.Fatalf("codex launch lacks the tier's effort: %s %v", argv, err)
	}
	wire, err := os.ReadFile(wireLog)
	if err != nil {
		t.Fatal(err)
	}
	resumed, advertised := false, false
	for _, line := range strings.Split(strings.TrimSpace(string(wire)), "\n") {
		var request struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		_ = json.Unmarshal([]byte(line), &request)
		if request.Method == "thread/resume" && strings.Contains(string(request.Params), "fixture-thread") {
			resumed = true
		}
		if request.Method == "thread/start" && strings.Contains(string(request.Params), `"proposals"`) && strings.Contains(string(request.Params), "result_submit") {
			advertised = true
		}
	}
	if !resumed || !advertised {
		t.Fatalf("native thread resumed %v; proposals advertised %v", resumed, advertised)
	}
}
