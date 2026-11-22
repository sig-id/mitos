package studio

import (
	"errors"
	"os"
	"strings"
	"testing"

	"mitos/agents"
)

func bundledDefinition(t *testing.T, id string) agents.Definition {
	t.Helper()
	library, err := agents.Library()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range library {
		if d.ID == id {
			return d
		}
	}
	t.Fatalf("%s missing", id)
	return agents.Definition{}
}

func workerAssembly(attempt, document string) contextAssembly {
	var ca contextAssembly
	ca.instruct("protocol text")
	ca.add(tierAttempt, contextAttempt, "identity", attempt)
	ca.add(tierTask, "assignment", "assignment", "Fix the parser.")
	ca.add(tierGraph, "constraints", "request", "Fix parsing bugs only.")
	ca.add(tierGraph, "project_context", "AGENTS.md", document)
	ca.add(tierGraph, "graph_state", "graph", "not requested by workers")
	return ca
}

func TestContextAssemblyOrdersStableContentFirst(t *testing.T) {
	d := bundledDefinition(t, "mitos.engineer")
	first, r1, err := func() (string, ContextReceipt, error) {
		ca := workerAssembly("run A", "project rules")
		return ca.assemble(d, t.TempDir())
	}()
	if err != nil {
		t.Fatal(err)
	}
	second, r2, err := func() (string, ContextReceipt, error) {
		ca := workerAssembly("run B", "project rules")
		return ca.assemble(d, t.TempDir())
	}()
	if err != nil {
		t.Fatal(err)
	}
	// Everything before the attempt tier is byte-identical across attempts.
	cut := strings.Index(first, "run A")
	if cut < 0 || !strings.HasPrefix(second, first[:cut]) || r1.ProtocolSHA256 != r2.ProtocolSHA256 || r1.PromptSHA256 == r2.PromptSHA256 {
		t.Fatal("volatile attempt facts precede stable context")
	}
	if !(strings.Index(first, "protocol text") < strings.Index(first, "Fix parsing bugs") && strings.Index(first, "Fix parsing bugs") < strings.Index(first, "Fix the parser") && strings.Index(first, "Fix the parser") < cut) {
		t.Fatal("tiers out of order:\n" + first)
	}
	if strings.Contains(first, "not requested by workers") {
		t.Fatal("undeclared input kind included")
	}
	for _, s := range r1.Sections {
		if s.Kind == "graph_state" && (s.Included || s.Reason == "") {
			t.Fatalf("undeclared input not recorded as excluded: %+v", s)
		}
	}
}

func TestContextOverflowOmitsOptionalWithReference(t *testing.T) {
	d := bundledDefinition(t, "mitos.engineer")
	big := strings.Repeat("rule ", d.Manifest.Context.MaxCharacters/5+10)
	dir := t.TempDir()
	ca := workerAssembly("run A", big)
	prompt, r, err := ca.assemble(d, dir)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(prompt, big) || r.OverflowAction != "omit_with_reference" || r.Used > r.Budget {
		t.Fatalf("optional input not omitted: %+v", r)
	}
	var omitted ContextSectionReceipt
	for _, s := range r.Sections {
		if s.Kind == "project_context" {
			omitted = s
		}
	}
	if omitted.Included || !strings.Contains(prompt, omitted.Reference) {
		t.Fatalf("omission not referenced: %+v", omitted)
	}
	path := omitted.Reference[strings.Index(omitted.Reference, dir):strings.Index(omitted.Reference, " when")]
	if data, err := os.ReadFile(path); err != nil || string(data) != big {
		t.Fatalf("omitted text not retrievable at %s: %v", path, err)
	}
	// Same inputs, same bytes: omission is deterministic.
	again := workerAssembly("run A", big)
	if p2, _, _ := again.assemble(d, dir); p2 != prompt {
		t.Fatal("assembly is not deterministic")
	}
}

func TestContextRefusesMissingOrOversizedRequiredInputs(t *testing.T) {
	d := bundledDefinition(t, "mitos.engineer")
	var prerequisite *agentPrerequisiteError
	var missing contextAssembly
	missing.add(tierTask, "assignment", "assignment", "Fix the parser.")
	if _, _, err := missing.assemble(d, t.TempDir()); !errors.As(err, &prerequisite) {
		t.Fatalf("missing constraints accepted: %v", err)
	}
	huge := workerAssembly("run", "")
	huge.add(tierTask, "assignment", "more", strings.Repeat("x", d.Manifest.Context.MaxCharacters+1))
	if _, r, err := huge.assemble(d, t.TempDir()); !errors.As(err, &prerequisite) || r.Used <= r.Budget {
		t.Fatalf("oversized required input dropped or accepted: %v", err)
	}
	d.Manifest.Context.Overflow = "wait"
	wait := workerAssembly("run", strings.Repeat("rule ", d.Manifest.Context.MaxCharacters/5+10))
	if _, _, err := wait.assemble(d, t.TempDir()); !errors.As(err, &prerequisite) {
		t.Fatal("wait overflow omitted input")
	}
}

func TestOrchestratorContextSplitsByKind(t *testing.T) {
	d := bundledDefinition(t, "mitos.orchestrator")
	var ca contextAssembly
	ca.add(tierGraph, "constraints", "request", "Audit the gap.")
	ca.add(tierTask, "assignment", "assignment", "Plan goal.")
	if err := addOrchestratorContext(&ca, `{"graph_state":{"facts":[{"id":"t1"}]},"watch_inbox":{"pending":1,"recent":[{"operator_message_untrusted":"Review the findings"}]},"agent_library":[{"id":"mitos.engineer"}],"remaining_tasks":3,"remaining_attempts":5,"remaining_plans":2,"graph_id":"g"}`); err != nil {
		t.Fatal(err)
	}
	prompt, r, err := ca.assemble(d, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, s := range r.Sections {
		kinds[s.Kind] = s.Included
	}
	if !strings.Contains(prompt, "Review the findings") {
		t.Fatal("automatic planning prompt dropped the responsibility inbox")
	}
	if !kinds["agent_library"] || !kinds["graph_state"] || !kinds["remaining_allowance"] {
		t.Fatalf("orchestrator inputs: %+v", r.Sections)
	}
	if strings.Index(prompt, "mitos.engineer") > strings.Index(prompt, `"facts"`) || strings.Index(prompt, `"facts"`) > strings.Index(prompt, "remaining_attempts") {
		t.Fatal("library, graph state and allowance out of stability order")
	}
	if err := addOrchestratorContext(&ca, "not json"); err == nil {
		t.Fatal("malformed planning context accepted")
	}
}
