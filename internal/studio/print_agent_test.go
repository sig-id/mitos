package studio

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/decision"
	"mitos/internal/harness"
)

// Exercise the stdin/stdout protocol through an actual graph, including a
// fresh native process for each tool response and access to the same workspace.
func TestPrintAdapterCoordinatedGoal(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 required for CLI fixture")
	}
	bin := t.TempDir()
	script := `import json, pathlib, sys
args=sys.argv[1:]
assert args[0]=='run' and args[args.index('--model')+1]=='fixture/worker',args
p=sys.stdin.read()
prefix=p.split('\nReply prefix: ')[-1]
history=json.loads(p.split('\nPrior turns: ')[-1].split('\nReply prefix: ')[0]) or []
if 'For the orchestrator the final field' in p:
    state=json.JSONDecoder().raw_decode(p.split('\n## graph_state (')[1].split('\n',1)[1])[0]
    workers=[e for e in state['journal'] if e['kind']=='application']
    plan={'explanation':'Review the saved evidence.','operations':[],'accept':[],'complete':bool(workers)}
    if workers:
        plan['accept']=[{'task':workers[-1]['task'],'rationale':'Worker delivered the requested answer.'}]
        plan['result']={'title':'Answer','markdown':'The answer is 42.','source_tasks':[workers[-1]['task']]}
    else:
        plan['operations']=[{'kind':'add','spec':{'id':'work0','parent':state['root_task'],'kind':'application','traits':{'nature':'','size':'small','intelligence':'low'},'required':True,'acceptance':'operator','criteria':'Provide the answer','brief':{'title':'Compute answer','intent':'Write the answer.','app_id':'agent:mitos.engineer'}}}]
    if workers and not history:
        reply={'tool':'artifact_list','arguments':{'run_id':workers[-1]['run_id'],'cursor':0,'limit':32}}
    elif workers and len(history)==1:
        ref=next(a for a in history[0]['response']['artifacts'] if a['role']=='report')
        reply={'tool':'artifact_read','arguments':{'run_id':workers[-1]['run_id'],'artifact_id':ref['id'],'offset':0,'limit':32768}}
    else:
        reply={'final':plan}
elif not history:
    reply={'question':'What is the requested answer?'}
elif len(history)==1:
    assert history[0]['answer']['result']['answers']['question']['answers']==['42'],history
    pathlib.Path('answer.txt').write_text('42\n')
    reply={'tool':'artifact_register','arguments':{'path':'answer.txt','role':'report'}}
elif len(history)==2:
    receipt=history[-1]['response']
    reply={'tool':'result_submit','arguments':{'version':'mitos.handoff/v1','key':'answer','summary':'42','artifacts':[{'artifact_id':receipt['id'],'role':'report'}],'checks':[],'limitations':[],'unresolved_requests':[]}}
else:
    assert 'error' not in history[-1]['response']
    reply={'final':'The answer is '+pathlib.Path('answer.txt').read_text().strip()+'.'}
print(prefix+'{"final":"wrong stream"}',file=sys.stderr)
print(prefix+json.dumps(reply))
print('usage statistics')
`
	if err = os.WriteFile(filepath.Join(bin, "opencode"), []byte("#!"+python+"\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, req := graphEngine(t, true)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "print-adapter", ProjectID: req.ProjectID, Goal: "Compute and review the answer", Execution: defaultExecution("opencode", "fixture/worker")})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(20 * time.Second)
	for time.Now().Before(until) {
		g, err = e.Store.Graph(g.ID)
		if err != nil {
			t.Fatal(err)
		}
		if g.Status == "accepted" {
			break
		}
		for _, run := range e.Store.List() {
			if d := run.Decision; d != nil && d.Resolution == nil && d.Source != nil && d.Source.Adapter == "opencode" {
				err = e.Store.ResolveChoice(run.ID, decision.Resolution{DecisionID: d.ID, Choice: "answer", Value: json.RawMessage(`"42"`), Via: "api"}, decision.LocalOperator(), nil)
				if err != nil {
					t.Fatal(err)
				}
			}
		}
		for _, x := range g.Expansions {
			if x.Error != "" {
				t.Fatalf("orchestration: %s", x.Error)
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	if g.Result == nil || g.Status != "accepted" || g.Result.Markdown != "The answer is 42." {
		t.Fatalf("incomplete: %+v", g)
	}
	for _, m := range g.Members {
		if m.Spec.Brief.Title != "Compute answer" {
			continue
		}
		body, err := e.Store.GraphRunFile(g.ID, m.Spec.ID, m.CurrentRunID, "answer.txt")
		if err != nil || string(body) != "42\n" || m.Accepted == nil {
			t.Fatalf("worker: %s %v", body, err)
		}
		return
	}
	t.Fatal("missing worker")
}

func TestPrintAdapterRejectsInvalidFrames(t *testing.T) {
	for _, frame := range []string{`{"final":null}`, `{"final":"ok","tool":"other"}`, `{"tool":"ungranted","arguments":{}}`, `{"final":"ok","unexpected":1}`, "{\"final\":\"ok\"}\nPREFIX {\"final\":\"extra\"}"} {
		p := newPrintTurn("", harness.CodexOptions{Result: func(string) error { t.Fatal("accepted invalid reply"); return nil }}, nil)
		p.marker = "PREFIX"
		p.output.Write([]byte("PREFIX " + frame))
		if _, err := p.reply(t.Context(), false); err == nil {
			t.Fatal(frame)
		}
	}
	p := newPrintTurn("", harness.CodexOptions{Tools: []harness.DynamicTool{{Name: "check"}}, ToolCall: func(string, json.RawMessage) (any, error) { return "receipt", nil }}, nil)
	p.marker = "PREFIX"
	p.output.Write([]byte(`PREFIX {"tool":"check","arguments":{}}`))
	if _, err := p.reply(t.Context(), true); err == nil || len(p.history) != 0 {
		t.Fatal("tool accepted after budget exhausted")
	}
	p.output.Reset()
	p.output.Write([]byte(`PREFIX {"tool":"check","arguments":{}}`))
	if done, err := p.reply(t.Context(), false); done || err != nil || !strings.Contains(string(p.history[0]), "receipt") {
		t.Fatalf("%v %v", done, err)
	}
}
