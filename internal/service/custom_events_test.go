package service

import "testing"

func TestCustomEventNamesAndSubscriptions(t *testing.T) {
	for _, name := range []string{"inventory.ready", "custom:event", "service_trigger_event", "jobs-v2"} {
		if !ValidCustomEventName(name) || !ValidEventPattern(name) || !ValidEventPattern(name+"*") {
			t.Fatal("custom name rejected", name)
		}
		if len(MatchEvents([]string{"*", name + "*", name}, []string{name})) != 1 {
			t.Fatal("overlap duplicated event")
		}
	}
	for _, name := range []string{"", "*", "bad event", "service.succeeded:foo", "service.custom", "worker.completed", "task.done", "run.end", "decision.resolved", "scheduling_event", "bad*middle"} {
		if ValidCustomEventName(name) {
			t.Fatal("invalid/reserved name accepted", name)
		}
	}
}
