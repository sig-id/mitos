package resourcepolicy

import "testing"

func TestScopesAndInheritance(t *testing.T) {
	base := Defaults()
	parent := Policy{Workflow: Workflow{ElapsedMinutes: Capped(40), Tokens: Capped(5000)}, Agent: Agent{MemoryMiB: Capped(512)}}
	child := Policy{Workflow: Workflow{Attempts: Capped(8), Tokens: Limit{Mode: "unlimited"}}, Agent: Agent{CPUMillicores: Capped(500)}}
	resolved := Resolve(Resolve(base, parent), child)
	if resolved.Workflow.ElapsedMinutes.Amount != 40 || resolved.Workflow.Attempts.Amount != 8 || resolved.Workflow.Tokens.Mode != "unlimited" || resolved.Agent.MemoryMiB.Amount != 512 || resolved.Agent.CPUMillicores.Amount != 500 {
		t.Fatalf("lost per-field inheritance: %+v", resolved)
	}
	if base.Workflow.ElapsedMinutes.Amount != 120 || parent.Workflow.Tokens.Amount != 5000 {
		t.Fatal("resolution mutated an input")
	}
	if err := resolved.Check(Enforcement{Elapsed: true, Attempts: true, CPU: true, Memory: true}); err != nil {
		t.Fatal(err)
	}
}
func TestExplicitModesAndEnforcement(t *testing.T) {
	for _, limit := range []Limit{{Mode: "limit"}, {Mode: "limit", Amount: -1}, {Mode: "inherit", Amount: 10}, {Mode: "unlimited", Amount: 10}, {Mode: "guess", Amount: 10}} {
		if err := (Policy{Agent: Agent{MemoryMiB: limit}}).Validate(); err == nil {
			t.Fatalf("invalid limit accepted: %+v", limit)
		}
	}
	if err := (Policy{Workflow: Workflow{ElapsedMinutes: Limit{Mode: "unlimited"}}}).Validate(); err == nil {
		t.Fatal("unbounded workflow accepted by bounded runtime")
	}
	for _, p := range []Policy{{Workflow: Workflow{Tokens: Capped(100)}}, {Agent: Agent{CPUMillicores: Capped(1000)}}, {Agent: Agent{MemoryMiB: Capped(1024)}}} {
		if err := p.Check(Enforcement{Elapsed: true, Attempts: true}); err == nil {
			t.Fatal("unsupported cap silently ignored")
		}
	}
	if err := Defaults().Check(Enforcement{Elapsed: true, Attempts: true}); err != nil {
		t.Fatal(err)
	}
}
