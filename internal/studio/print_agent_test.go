package studio

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mitos/internal/compute"
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
if 'You are the orchestrator: the final field' in p:
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
    elif not workers and not history:
        # A broken first plan (an extra brace, as GLM sent) is returned for resending.
        print('Plan follows.')
        print(prefix+'{"final":{"operations":[]}}}')
        sys.exit(0)
    else:
        assert not workers or 'rejected_reply' not in history[0],history
        if not workers:
            assert history[0]['error'].startswith('invalid coordinated CLI response'),history
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

// Models often drop the space after the marker (GLM through Pi does), and the
// prompt must not end in a space a model cannot see.
func TestPrintAdapterAcceptsAFrameWithoutTheSpace(t *testing.T) {
	var got string
	p := newPrintTurn("", harness.CodexOptions{Result: func(s string) error { got = s; return nil }}, nil)
	input, err := p.input()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(input), "\nReply prefix: "+p.marker) {
		t.Fatalf("prompt ends %q", input[len(input)-40:])
	}
	p.output.Write([]byte("Done.\n" + p.marker + `{"final":"report"}` + "\nusage statistics\n"))
	if done, err := p.reply(t.Context(), false); !done || err != nil || got != "report" {
		t.Fatalf("%v %v %q", done, err, got)
	}
}

// The orchestrator's plan travels inside the final envelope; a bare plan is
// refused, and the prompt shows the envelope so a model does not send one.
func TestPrintAdapterOrchestratorPlanNeedsTheEnvelope(t *testing.T) {
	p := newPrintTurn("", harness.CodexOptions{OutputSchema: OrchestratorSchema(), Result: func(string) error { return nil }}, nil)
	if !strings.Contains(p.base, `<reply prefix> {"final":{"operations":[...],...}}`) {
		t.Fatal("orchestrator prompt does not show the final envelope")
	}
	p.marker = "PREFIX"
	p.output.Write([]byte(`PREFIX{"operations":[],"accept":[],"complete":false,"explanation":"bare"}`))
	if _, err := p.reply(t.Context(), false); err == nil {
		t.Fatal("bare plan accepted")
	}
	p.output.Reset()
	p.output.Write([]byte(`PREFIX {"final":{"operations":[],"accept":[],"complete":false,"explanation":"wrapped"}}`))
	if done, err := p.reply(t.Context(), false); !done || err != nil {
		t.Fatalf("%v %v", done, err)
	}
}

type doneSession struct{}

func (doneSession) ID() string           { return "s" }
func (doneSession) PID() int             { return 0 }
func (doneSession) OutputOffset() uint64 { return 0 }
func (doneSession) Result() ([]byte, error) {
	return nil, nil
}
func (doneSession) Done() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

// replyingProvider answers each continued turn with the next scripted reply.
type replyingProvider struct {
	compute.Provider
	turn    *printTurn
	replies []string
	inputs  []string
}

func (r *replyingProvider) Next(_ context.Context, _ compute.Session, cmd compute.Command) (compute.Session, error) {
	r.inputs = append(r.inputs, string(cmd.Input))
	r.turn.output.Write([]byte(r.turn.marker + r.replies[0]))
	r.replies = r.replies[1:]
	return doneSession{}, nil
}

// An invalid reply line is returned for resending, at most twice in a row,
// with the error and the rejected text in the transcript.
func TestPrintAdapterResendsInvalidFrames(t *testing.T) {
	run := func(first string, replies ...string) (string, *replyingProvider, error) {
		var got string
		p := newPrintTurn("", harness.CodexOptions{Result: func(s string) error { got = s; return nil }}, nil)
		if _, err := p.input(); err != nil {
			t.Fatal(err)
		}
		p.output.Write([]byte(p.marker + first))
		provider := &replyingProvider{turn: p, replies: replies}
		_, err := p.run(t.Context(), provider, doneSession{}, func(b []byte) compute.Command { return compute.Command{Input: b} }, io.Discard)
		return got, provider, err
	}
	got, provider, err := run(` {"final":"ok"}}`, ` {"final":`, ` {"final":"fixed"}`)
	if err != nil || got != "fixed" {
		t.Fatalf("%q %v", got, err)
	}
	if !strings.Contains(provider.inputs[0], `"rejected_reply":"{\"final\":\"ok\"}}"`) || !strings.Contains(provider.inputs[1], "invalid coordinated CLI response") {
		t.Fatalf("resend turns did not carry the rejected reply: %q", provider.inputs)
	}
	if _, _, err = run(` nope`, ` nope`, ` nope`, ` {"final":"late"}`); err == nil || !strings.Contains(err.Error(), "invalid coordinated CLI response") {
		t.Fatalf("third consecutive invalid reply was not fatal: %v", err)
	}
}

// A worker's final report must follow a successful result_submit; one sent
// earlier is returned for resending instead of ending the attempt unsubmitted.
func TestPrintAdapterWorkerFinalNeedsASubmittedResult(t *testing.T) {
	submit := errors.New("handoff rejected")
	var got string
	p := newPrintTurn("", harness.CodexOptions{
		Tools:    []harness.DynamicTool{{Name: "result_submit"}},
		ToolCall: func(string, json.RawMessage) (any, error) { return "receipt", submit },
		Result:   func(s string) error { got = s; return nil },
	}, nil)
	p.marker = "PREFIX"
	reply := func(frame string) (bool, error) {
		p.output.Reset()
		p.output.Write([]byte("PREFIX " + frame))
		return p.reply(t.Context(), false)
	}
	var frame frameError
	if _, err := reply(`{"final":"early"}`); !errors.As(err, &frame) || got != "" {
		t.Fatalf("final before any submission: %v", err)
	}
	if _, err := reply(`{"tool":"result_submit","arguments":{}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := reply(`{"final":"after a refused submission"}`); !errors.As(err, &frame) || got != "" {
		t.Fatalf("final after a refused submission: %v", err)
	}
	submit = nil
	if _, err := reply(`{"tool":"result_submit","arguments":{}}`); err != nil {
		t.Fatal(err)
	}
	if done, err := reply(`{"final":"report"}`); !done || err != nil || got != "report" {
		t.Fatalf("%v %v %q", done, err, got)
	}
}
