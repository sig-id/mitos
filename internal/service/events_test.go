package service

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEventPatternsHooksAndMatching(t *testing.T) {
	for _, raw := range []string{`"service.succeeded:extract"`, `["service.succeeded:extract"]`} {
		var p EventPatterns
		if err := json.Unmarshal([]byte(raw), &p); err != nil || !reflect.DeepEqual(p, EventPatterns{"service.succeeded:extract"}) {
			t.Fatal("string/array trigger failed", err)
		}
	}
	unit := Unit{Triggers: &Triggers{OnComplete: []string{"extract"}, OnFailure: []string{"errors"}, OnFinish: []string{"extract"}}}
	if unit.TriggerKind() != "event" || unit.ValidateRuntime() != nil {
		t.Fatal("hooks did not normalize to event trigger")
	}
	patterns, err := unit.EventPatterns()
	if err != nil {
		t.Fatal(err)
	}
	matched := MatchEvents(patterns, []string{"service.succeeded:extract", "service.completed:extract"})
	if len(matched) != 2 {
		t.Fatal("success/finish hook matching failed")
	}
	for _, p := range []string{"service.completed:*", "service.completed:extract*"} {
		if len(MatchEvents([]string{p}, []string{"service.completed:extract-one"})) != 1 {
			t.Fatal("suffix wildcard failed")
		}
	}
	if len(MatchEvents([]string{"service.failed:extract"}, []string{"service.succeeded:extract", "service.completed:extract"})) != 0 {
		t.Fatal("failure hook matched success")
	}
}
func TestEventSchemaRejectsUnsupportedSignalsAndMixedClocks(t *testing.T) {
	for _, pattern := range []string{"worker.completed", "scheduling_event", "bad event", "service.completed:*middle*", "service.failed:", "service.succeeded:../private"} {
		if ValidEventPattern(pattern) {
			t.Fatal("unsupported pattern accepted", pattern)
		}
	}
	for _, u := range []Unit{
		{Trigger: "event", Schedule: &Schedule{Type: "event"}},
		{Trigger: "event", Schedule: &Schedule{Type: "event", Trigger: EventPatterns{"service.completed:*"}, Interval: 1}},
		{Trigger: "event", Schedule: &Schedule{Type: "event", Trigger: EventPatterns{"service.completed:*"}, RunOnStartup: true}},
		{Trigger: "manual", Triggers: &Triggers{OnComplete: []string{"source"}}},
		{Trigger: "event", Schedule: &Schedule{Type: "event", Trigger: EventPatterns{"service.completed:*"}}, Triggers: &Triggers{OnComplete: []string{"source"}}},
	} {
		if u.ValidateRuntime() == nil {
			t.Fatal("mixed/invalid event definition accepted")
		}
	}
}

func TestAdditionalEventsShareTimedOrManualService(t *testing.T) {
	u := Unit{Trigger: "cron", Schedule: &Schedule{Type: "cron", Cron: "*/5 * * * *"}, Events: EventPatterns{"github.pr.changed"}}
	if err := u.ValidateRuntime(); err != nil {
		t.Fatal(err)
	}
	u.Events = EventPatterns{"worker.completed"}
	if u.ValidateRuntime() == nil {
		t.Fatal("unsupported event accepted")
	}
	u.Events = EventPatterns{"github.pr.changed"}
	u.Trigger = "continuous"
	u.Schedule = &Schedule{Type: "continuous"}
	if u.ValidateRuntime() == nil {
		t.Fatal("unsupported combined trigger accepted")
	}
}
