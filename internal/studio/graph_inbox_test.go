package studio

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"mitos/internal/messaging"
	"mitos/internal/workpool"
)

func signalDependency(g WorkGraph) workpool.Dependency {
	return workpool.Dependency{From: "release", Guard: "inbox_received", Payload: "data", Inbox: &workpool.Inbox{Project: g.ProjectID, Mailbox: g.ID, Sender: "remote", Kind: "event", Type: "workflow.signal", Correlation: "release-v1", Generation: int(g.OrchestratorGeneration)}}
}

func graphSignal(t *testing.T, s *Store, g WorkGraph, id string) messaging.Envelope {
	t.Helper()
	body, _ := messaging.Normalize([]byte(`{"release":"v1"}`))
	return messaging.Envelope{Version: 1, ID: id, From: "remote", To: s.Home().ID, Project: g.ProjectID, Mailbox: g.ID, Kind: "event", Type: "workflow.signal", Correlation: "release-v1", Generation: int(g.OrchestratorGeneration), Deadline: time.Now().Add(time.Hour), Payload: body, Digest: messaging.Hash(body)}
}

func TestGraphInboxAdmissionBindingAndRestart(t *testing.T) {
	e, req := graphEngine(t, false)
	app := executionApp(t, `cat ../inputs/release/message.json > result.txt`)
	e.Apps = append(e.Apps, app)
	req.Policy.Applications = append(req.Policy.Applications, app.ID)
	g := createGraphTest(t, e, req)
	// An earlier message must not satisfy a future-message dependency.
	if r, err := e.Store.AcceptMessage(graphSignal(t, e.Store, g, "old")); err != nil || r.Status != "accepted" {
		t.Fatal(r, err)
	}
	spec := graphWorker("consumer", g.RootTask)
	spec.Brief.AppID = app.ID
	spec.Brief.Scheduling.Deps = []workpool.Dependency{signalDependency(g)}
	g, receipt := amendGraphTest(t, e, g, "wait", GraphOperation{Kind: "add", Spec: &spec})
	id := receipt.IDs["consumer"]
	ready := func() workpool.Readiness {
		e.Store.mu.RLock()
		defer e.Store.mu.RUnlock()
		return e.Store.graphMemberReadiness(e.Store.graphs[g.ID], e.Store.graphs[g.ID].Members[id])
	}
	if ready().Ready {
		t.Fatal("historical message triggered future work")
	}
	for _, name := range []string{"expired", "generation", "correlation", "sender", "type", "project"} {
		m := graphSignal(t, e.Store, g, name)
		switch name {
		case "expired":
			m.Deadline = time.Now().Add(-time.Second)
		case "generation":
			m.Generation++
		case "correlation":
			m.Correlation = "another-release"
		case "sender":
			m.From = "other"
		case "type":
			m.Type = "other.signal"
		case "project":
			m.Project = "other-project"
		}
		if _, err := e.Store.AcceptMessage(m); err != nil {
			t.Fatal(err)
		}
		if ready().Ready {
			t.Fatalf("%s delivery enabled work", name)
		}
	}
	match := graphSignal(t, e.Store, g, "match")
	if r, err := e.Store.AcceptMessage(match); err != nil || r.Status != "accepted" {
		t.Fatal(r, err)
	}
	seq := e.Store.seq
	if _, err := e.Store.AcceptMessage(match); err != nil || e.Store.seq != seq {
		t.Fatal("redelivery was not deduplicated", err)
	}
	if !ready().Ready {
		t.Fatal("admitted matching inbox message did not enable task")
	}
	if err := e.reconcileGraphs(); err != nil {
		t.Fatal(err)
	}
	g, _ = e.Store.Graph(g.ID)
	rid := g.Members[id].CurrentRunID
	if ok, err := e.Store.Schedule(rid, nil); err != nil || !ok {
		t.Fatal("admission", ok, err)
	}
	task, _ := e.Store.Task(id)
	if len(task.DependencyEvidence) != 1 {
		t.Fatal(task.DependencyEvidence)
	}
	w := task.DependencyEvidence[0]
	if w.Inbox == nil || w.Inbox.Key != match.Key() || w.Seq != seq || len(w.Artifacts) != 1 {
		t.Fatal(w)
	}
	if _, err := e.Store.AcceptMessage(graphSignal(t, e.Store, g, "later")); err != nil {
		t.Fatal(err)
	}
	body, err := e.Store.boundInput(w, w.Artifacts[0])
	if err != nil {
		t.Fatal(err)
	}
	var bound messaging.Envelope
	if json.Unmarshal(body, &bound) != nil || bound.ID != "match" {
		t.Fatal("bound input drifted", string(body))
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
	after, _ := reopened.Task(id)
	if !reflect.DeepEqual(task.DependencyEvidence, after.DependencyEvidence) {
		t.Fatal("inbox witness changed on replay")
	}
	if _, err := reopened.boundInput(w, w.Artifacts[0]); err != nil {
		t.Fatal("replayed inbox evidence", err)
	}
}

func TestGraphInboxRejectsCrossProjectAndInvalidGuards(t *testing.T) {
	for _, mode := range []string{"project", "guard", "cursor", "artifacts", "source"} {
		t.Run(mode, func(t *testing.T) {
			e, req := graphEngine(t, false)
			g := createGraphTest(t, e, req)
			dep := signalDependency(g)
			switch mode {
			case "project":
				dep.Inbox.Project = "other"
			case "guard":
				dep.Guard = "done"
			case "cursor":
				dep.Inbox.AfterSequence = e.Store.seq + 1000
			case "artifacts":
				dep.Artifacts = []string{"report.md"}
			case "source":
				dep.Inbox = nil
			}
			spec := graphWorker("consumer", g.RootTask)
			spec.Brief.Scheduling.Deps = []workpool.Dependency{dep}
			if _, _, err := e.AmendGraph(proposal(g, "bad", GraphOperation{Kind: "add", Spec: &spec})); err == nil {
				t.Fatal("invalid inbox source accepted")
			}
		})
	}
}

func TestMQTTInboxStartsGraphTaskOnce(t *testing.T) {
	broker, address := mqttBroker(t, "127.0.0.1:0")
	defer broker.Close()
	e, req := graphEngine(t, false)
	app := executionApp(t, `cat ../inputs/release/message.json > result.txt`)
	e.Apps = append(e.Apps, app)
	req.Policy.Applications = append(req.Policy.Applications, app.ID)
	g := createGraphTest(t, e, req)
	a := messageStore(t)
	spec := graphWorker("consumer", g.RootTask)
	spec.Brief.AppID = app.ID
	dep := signalDependency(g)
	dep.Inbox.Sender = a.Home().ID
	spec.Brief.Scheduling.Deps = []workpool.Dependency{dep}
	g, receipt := amendGraphTest(t, e, g, "wait", GraphOperation{Kind: "add", Spec: &spec})
	id := receipt.IDs["consumer"]
	running, err := NewEngine(e.Store, e.Apps, e.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()
	ak, ap := mqttKeys(t)
	bk, bp := mqttKeys(t)
	sa := mqttStart(t, a, a.Home().ID, ak, messaging.Config{Broker: "mqtt://" + address, Peers: []messaging.Peer{{ID: e.Store.Home().ID, PublicKey: bp}}})
	mqttStart(t, e.Store, e.Store.Home().ID, bk, messaging.Config{Broker: "mqtt://" + address, Peers: []messaging.Peer{{ID: a.Home().ID, PublicKey: ap, Grants: []messaging.Grant{{Project: g.ProjectID, Mailbox: g.ID, Kind: "event", Type: "workflow.signal"}}}}})
	in := messaging.Request{ID: "release-once", To: e.Store.Home().ID, Project: g.ProjectID, Mailbox: g.ID, Kind: "event", Type: "workflow.signal", Correlation: "release-v1", Generation: int(g.OrchestratorGeneration), Deadline: time.Now().Add(time.Minute), Payload: json.RawMessage(`{"release":"v1"}`)}
	for i := 0; i < 2; i++ {
		if _, err := sa.Send(in); err != nil {
			t.Fatal(err)
		}
	}
	mqttWait(t, "one completed inbox consumer", func() bool { current, _ := e.Store.Graph(g.ID); return current.Members[id].Accepted != nil })
	current, _ := e.Store.Graph(g.ID)
	if len(current.Members[id].Attempts) != 1 {
		t.Fatal("redelivery launched multiple attempts")
	}
	task, _ := e.Store.Task(id)
	if len(task.DependencyEvidence) != 1 || task.DependencyEvidence[0].Inbox == nil {
		t.Fatal("MQTT message was not bound")
	}
	if _, err := e.Store.Artifact(current.Members[id].CurrentRunID, "dependency-inputs.json"); err != nil {
		t.Fatal("message input not materialized", err)
	}
}

func TestAutomaticOrchestratorWaitsForInboxAndCompletes(t *testing.T) {
	e, req := automaticScriptEngine(t, `import json,pathlib,sys
prompt=sys.stdin.read();out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
if '--output-schema' in sys.argv:
 ctx=json.loads(prompt.split('\nGraph context:\n')[1].split('\nSaved upstream evidence')[0])
 workers=[f for f in ctx['facts'] if f['spec']['kind']=='application']
 plan={'operations':[],'accept':[],'complete':bool(workers),'explanation':'Wait for the requested release signal and review its evidence.'}
 if not workers:
  inbox=dict(ctx['inbox'],sender='remote',kind='event',type='workflow.signal',correlation='release-v1')
  plan['operations']=[{'kind':'add','spec':{'id':'consumer','parent':ctx['root_task'],'kind':'application','required':True,'criteria':'Report the admitted release signal.','acceptance':'operator','brief':{'title':'Read release signal','intent':'Read the bound message.json and report its release.','app_id':'agent:mitos.generalist','timeout_minutes':1,'scheduling':{'deps':[{'from':'release','inbox':inbox,'guard':'inbox_received','payload':'data'}],'join':{'rule':'all'}}}}}]
 else:
  assert workers[0]['state']=='done',workers
  plan['accept']=[{'task':workers[0]['id'],'rationale':'The saved report identifies release v1.'}]
  plan['result']={'title':'Release received','markdown':'Received release v1.','source_tasks':[workers[0]['id']]}
 out.write_text(json.dumps(plan))
else:
 inputs=json.loads(prompt.split('\nSaved upstream evidence (read these files):\n')[1].split('\nWork only')[0])
 assert len(inputs)==1,inputs
 message=json.loads(pathlib.Path(inputs[0]['directory'],'message.json').read_text())
 assert message['payload']['release']=='v1',message
 out.write_text('Received release v1 from the admitted message.')
`)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "automatic-inbox", ProjectID: req.ProjectID, Goal: "Wait for release v1 and report it"})
	if err != nil {
		t.Fatal(err)
	}
	var worker string
	mqttWait(t, "orchestrator creates an inbox-dependent worker", func() bool {
		current, _ := e.Store.Graph(g.ID)
		for id, m := range current.Members {
			if m.Spec.Kind == "application" && m.OrchestratorFor == "" {
				worker = id
				return true
			}
		}
		return false
	})
	current, _ := e.Store.Graph(g.ID)
	if current.Members[worker].CurrentRunID != "" || current.OrchestratorCalls != 1 {
		t.Fatal("waiting inbox consumed an execution or orchestration call")
	}
	if receipt, err := e.Store.AcceptMessage(graphSignal(t, e.Store, current, "release-native")); err != nil || receipt.Status != "accepted" {
		t.Fatal(receipt, err)
	}
	mqttWait(t, "automatic inbox goal completes", func() bool { current, _ = e.Store.Graph(g.ID); return current.Status == "accepted" })
	if current.OrchestratorCalls != 2 || len(current.Members[worker].Attempts) != 1 {
		t.Fatal("unexpected polling calls or duplicate launches", current.OrchestratorCalls)
	}
}
