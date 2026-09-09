package service

import "testing"

func TestKillConcurrencyContract(t *testing.T) {
	for _, trigger := range []string{"manual", "interval", "cron", "event", "continuous"} {
		unit := Unit{Trigger: trigger, Concurrency: Concurrency{IfRunning: "kill", MaxInstances: 2}}
		switch trigger {
		case "interval":
			unit.Schedule = &Schedule{Type: trigger, Interval: 1}
		case "cron":
			unit.Schedule = &Schedule{Type: trigger, Cron: "* * * * *"}
		case "event":
			unit.Schedule = &Schedule{Type: trigger, Trigger: EventPatterns{"service.completed:*"}}
		case "continuous":
			unit.Schedule = &Schedule{Type: trigger}
		}
		if err := unit.ValidateRuntime(); err != nil {
			t.Fatal(trigger, err)
		}
	}
	if err := (Unit{Trigger: "manual", Concurrency: Concurrency{IfRunning: "replace"}}).ValidateRuntime(); err == nil {
		t.Fatal("unregistered overlap mode accepted")
	}
}
