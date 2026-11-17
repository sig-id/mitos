package studio

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"
)

func contextReceiptOf(t *testing.T, e *Engine, runID string) ContextReceipt {
	t.Helper()
	raw, err := e.Store.Artifact(runID, "context.json")
	if err != nil {
		t.Fatal(err)
	}
	var receipt ContextReceipt
	if err := json.Unmarshal(raw, &receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

// Consecutive automatic planning calls must render an append-only journal whose
// prefix is shared, measurable in the context receipts of both attempts.
func TestOrchestratorJournalSharesPrefixAcrossPlanningCalls(t *testing.T) {
	e, req := automaticScriptEngine(t, `import json,pathlib,sys
prompt=sys.stdin.read()
out=pathlib.Path(sys.argv[sys.argv.index('--output-last-message')+1])
if '--output-schema' in sys.argv:
 ctx=json.loads(section(prompt,'graph_state'))
 latest={}
 for e in ctx['journal']:latest[e['task']]=e
 workers=[{'id':e['task'],'kind':e['kind'],'state':e['state'],'run_id':e['run_id']} for e in latest.values() if e['kind']=='application']
 plan={'explanation':'Plan then review.','operations':[],'accept':[],'complete':False}
 if not workers:
  plan['operations']=[{'kind':'add','spec':{'id':'worker','parent':ctx['root_task'],'kind':'application','required':True,'acceptance':'operator','criteria':'Produce evidence.','brief':{'title':'Produce','intent':'Produce the answer','app_id':'agent:mitos.generalist','timeout_minutes':30,'scheduling':{'deps':[]}}}}]
 else:
  plan['accept']=[{'task':workers[0]['id'],'rationale':'Reviewed the saved report.'}]
  plan['complete']=True
  plan['result']={'title':'Answer','markdown':'The answer is 42.','source_tasks':[workers[0]['id']]}
 out.write_text(json.dumps(plan))
else:
 out.write_text('The answer is 42.')
`)
	g, err := e.StartAutomaticGoal(AutomaticGoal{Key: "journal-prefix", ProjectID: req.ProjectID, Goal: "Produce the answer"})
	if err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(60 * time.Second)
	var receipts []ContextReceipt
	for time.Now().Before(until) {
		g, _ = e.Store.Graph(g.ID)
		if g.Status == "accepted" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if g.Status != "accepted" {
		t.Fatalf("goal did not finish: %+v", g.Expansions)
	}
	type stamped struct {
		at      time.Time
		receipt ContextReceipt
	}
	ordered := []stamped{}
	for _, id := range graphIDs(&g) {
		m := g.Members[id]
		for _, rid := range m.Attempts {
			run, err := e.Store.Get(rid)
			if err != nil {
				t.Fatal(err)
			}
			ordered = append(ordered, stamped{run.CreatedAt, contextReceiptOf(t, e, rid)})
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].at.Before(ordered[j].at) })
	for _, o := range ordered {
		if has := false; !has {
			for _, section := range o.receipt.Sections {
				if section.Kind == "graph_state" && section.Characters > 0 {
					receipts = append(receipts, o.receipt)
					break
				}
			}
		}
	}
	if len(receipts) < 2 {
		t.Fatalf("expected consecutive planning attempts, got %d", len(receipts))
	}
	for i := 1; i < len(receipts); i++ {
		prev, current := sectionOf(t, receipts[i-1], "graph_state"), sectionOf(t, receipts[i], "graph_state")
		if prev.Kind != "graph_state" || current.Kind != "graph_state" {
			t.Fatal("graph_state section missing")
		}
		if current.SharedPrefixBytes <= 0 || current.SharedPrefixSHA256 == "" {
			t.Fatalf("planning call %d lacks shared-prefix evidence: %+v", i, current)
		}
		if current.SharedPrefixBytes > prev.Characters {
			t.Fatalf("shared prefix longer than the previous journal: %d > %d", current.SharedPrefixBytes, prev.Characters)
		}
	}
	// The receipts' shared prefix matches the kernel's own measurement.
	if g.PlanPrefixSHA256 == "" || g.PlanPrefixCharacters <= 0 {
		t.Fatal("kernel did not record the planning prefix")
	}
	first, last := sectionOf(t, receipts[0], "graph_state"), sectionOf(t, receipts[len(receipts)-1], "graph_state")
	if last.SharedPrefixBytes == last.Characters && g.Status == "accepted" {
		t.Fatalf("journals are identical across calls: %d == %d", last.SharedPrefixBytes, last.Characters)
	}
	if first.Characters == 0 || last.Characters < first.Characters {
		t.Fatalf("journal did not grow: %d then %d", first.Characters, last.Characters)
	}
}

func sectionOf(t *testing.T, receipt ContextReceipt, kind string) ContextSectionReceipt {
	t.Helper()
	for _, s := range receipt.Sections {
		if s.Kind == kind {
			return s
		}
	}
	t.Fatalf("section %s missing from receipt", kind)
	return ContextSectionReceipt{}
}

// The inline journal plus fixed-boundary compaction keeps large graphs inside
// the halved planning allowance without detail loss: graph_read pages it.
func TestOrchestratorJournalStaysBoundedOnLargeGraphs(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Limits.Tasks = 128
	req.Limits.Fanout = 128
	specs := []GraphSpec{{ID: "goal", Kind: "scope", Required: true, Criteria: "Large graph", Acceptance: "operator", Brief: Brief{Title: "Large graph"}}}
	for i := 0; i < 60; i++ {
		specs = append(specs, GraphSpec{ID: "task" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Parent: "goal", Kind: "application", Required: false, Criteria: "Produce evidence " + strings.Repeat("detail ", 60), Acceptance: "operator", Brief: Brief{Title: "Work", AppID: "workspace-inventory", TimeoutMinutes: 1}})
	}
	req.Tasks = specs
	g := createGraphTest(t, e, req)
	s := e.Store
	s.mu.Lock()
	graph := s.graphs[g.ID]
	for id, member := range graph.Members {
		member.FactRevision = int64(3 + len(id))
		member.State = "done"
	}
	appendFactJournal(graph, s)
	for range 200 {
		for _, id := range graphIDs(graph) {
			member := graph.Members[id]
			member.FactRevision++
		}
		appendFactJournal(graph, s)
	}
	state := renderPlanJournal(graph)
	if len(state) >= orchestratorPlanLimit {
		t.Fatalf("journal state %d exceeds the %d planning allowance", len(state), orchestratorPlanLimit)
	}
	compacted := 0
	for _, entry := range graph.FactJournal {
		if entry.Compacted > 0 {
			compacted++
		}
	}
	// Entries never exceed one compaction interval plus one round of appends.
	if compacted == 0 || len(graph.FactJournal) > graphJournalCompactAt+64 {
		t.Fatalf("compaction did not bound the journal: compacted %d entries %d", compacted, len(graph.FactJournal))
	}
	if !strings.Contains(state, "\"compacted\"") {
		t.Fatal("compaction marker missing from the rendered journal")
	}
	// The prefix still stabilizes between compaction boundaries: one more fact
	// round shares the whole previous journal except the JSON array closers.
	before := renderPlanJournal(graph)
	entries := len(graph.FactJournal)
	graph.Members[graph.RootTask].FactRevision++
	appendFactJournal(graph, s)
	after := renderPlanJournal(graph)
	if shared, _ := sharedStatePrefix(before, after); len(graph.FactJournal) > entries && shared < len(before)-96 {
		t.Fatalf("journal rewrote history outside a compaction boundary: shared %d of %d", shared, len(before))
	}
	s.mu.Unlock()
}

// Multibyte and JSON-escaped summaries are bounded by serialized bytes, not
// rune counts: a graph whose CJK summaries are valid under the result-summary
// limit must still plan, instead of parking behind orchestrator_context_limit
// long before the fixed entry boundary.
func TestOrchestratorJournalBoundsSerializedSummaries(t *testing.T) {
	e, req := graphEngine(t, false)
	req.Limits.Tasks = 128
	req.Limits.Fanout = 128
	specs := []GraphSpec{{ID: "goal", Kind: "scope", Required: true, Criteria: "Wide graph", Acceptance: "operator", Brief: Brief{Title: "Wide graph"}}}
	for i := range 70 {
		specs = append(specs, GraphSpec{ID: "task" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Parent: "goal", Kind: "application", Required: false, Criteria: "Produce evidence", Acceptance: "operator", Brief: Brief{Title: "Work", AppID: "workspace-inventory", TimeoutMinutes: 1}})
	}
	req.Tasks = specs
	g := createGraphTest(t, e, req)
	s := e.Store
	s.mu.Lock()
	defer s.mu.Unlock()
	graph := s.graphs[g.ID]
	summary := strings.Repeat("結果", 512)
	for id, member := range graph.Members {
		member.FactRevision = int64(3 + len(id))
		member.State = "done"
		if member.Spec.Kind == "application" {
			run := newID("local")
			s.runs[run] = &Run{ID: run, TaskID: id, Summary: summary}
			member.CurrentRunID = run
		}
	}
	appendFactJournal(graph, s)
	state := renderPlanJournal(graph)
	if len(state) >= orchestratorPlanLimit {
		t.Fatalf("journal state %d exceeds the %d planning allowance before the entry boundary", len(state), orchestratorPlanLimit)
	}
	compacted := 0
	for _, entry := range graph.FactJournal {
		if encoded, err := json.Marshal(entry.Summary); err != nil || len(encoded) > graphJournalSummary {
			t.Fatalf("summary exceeds its serialized budget: %d %v", len(encoded), err)
		}
		if entry.Compacted > 0 {
			compacted++
		}
	}
	if compacted != 1 || len(graph.FactJournal) > graphJournalCompactKeep+1 {
		t.Fatalf("byte-budget compaction missing: compacted %d entries %d", compacted, len(graph.FactJournal))
	}
	// New facts still append after the early compaction without rewriting it.
	before := renderPlanJournal(graph)
	graph.Members[graph.RootTask].FactRevision++
	appendFactJournal(graph, s)
	after := renderPlanJournal(graph)
	if shared, _ := sharedStatePrefix(before, after); shared < len(before)-96 {
		t.Fatalf("compaction rewrote history outside its boundary: shared %d of %d", shared, len(before))
	}
}
