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
 return {'id':ref,'parent':root,'kind':'application','required':True,'acceptance':'operator','criteria':'Answer','brief':{'title':ref,'intent':intent,'app_id':'agent:mitos.generalist','timeout_minutes':30,'scheduling':{'deps':[]}}}
if '--output-schema' in sys.argv:
 ctx=json.loads(section(prompt,'graph_state'));root=ctx['root_task']
 workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 plan={'explanation':'Coordinate.','operations':[],'accept':[],'complete':False,'result':{'title':'','markdown':'','source_tasks':[]}}
 pending=[(f,p) for f in workers for p in f.get('proposals',[])]
 lead=[f for f in workers if f['spec']['brief']['title']=='lead']
 helper=[f for f in workers if f['spec']['brief']['title']=='helper']
 if not workers:
  plan['operations']=[{'kind':'add','spec':spec('lead','LEAD fixture-manual-submit',root)}]
 elif pending:
  f,p=pending[0]
  assert p['detail']['blocking'] and lead[0]['state']=='yielded',str(pending)
  plan['operations']=[{'kind':'add','spec':spec('helper','HELPER',root)},{'kind':'accept_proposal','proposal':p['id'],'dependencies':[{'from':'helper','guard':'done','payload':'data','artifacts':['report.md']}],'rationale':'A helper can answer that.'}]
 elif helper and not helper[0]['accepted']:
  assert helper[0]['state']=='done',str(helper)
  plan['accept']=[{'task':helper[0]['id'],'rationale':'Helper answered.'}]
 else:
  assert lead[0]['state']=='done',str(lead)
  plan['accept']=[{'task':lead[0]['id'],'rationale':'Lead integrated the answer.'}];plan['complete']=True
  plan['result']={'title':'Answer','markdown':'The answer is 7.','source_tasks':[lead[0]['id'],helper[0]['id']]}
 out.write_text(json.dumps(plan))
elif 'HELPER' in prompt:
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
