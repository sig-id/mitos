package service

import "testing"

func TestServicePriorityContract(t *testing.T) {
	for name, want := range map[string]int{"": 0, "normal": 0, "low": -100, "high": 100, "critical": 200} {
		got, ok := PriorityAdjustment(name)
		if !ok || got != want {
			t.Fatal(name, got)
		}
		unit := Unit{Trigger: "manual", Concurrency: Concurrency{Priority: name}}
		if err := unit.ValidateRuntime(); err != nil {
			t.Fatal(name, err)
		}
	}
	for _, name := range []string{"High", "urgent", "high ", "100", "none"} {
		if err := (Unit{Trigger: "manual", Concurrency: Concurrency{Priority: name}}).ValidateRuntime(); err == nil {
			t.Fatal("invalid priority accepted", name)
		}
	}
	if (Concurrency{}).Effective().Priority != "normal" {
		t.Fatal("priority default")
	}
}
