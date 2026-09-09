package service

import (
	"encoding/json"
	"testing"
	"time"
)

func TestContinuousScheduleContract(t *testing.T) {
	for _, body := range []string{`{"type":"continuous"}`, `{"type":"continuous","restart_delay":0}`, `{"type":"continuous","restart_delay":31536000}`} {
		var schedule Schedule
		if err := json.Unmarshal([]byte(body), &schedule); err != nil {
			t.Fatal(err)
		}
		unit := Unit{Trigger: "continuous", Schedule: &schedule}
		if err := unit.ValidateRuntime(); err != nil {
			t.Fatal(body, err)
		}
		if schedule.Timed() {
			t.Fatal("continuous treated as an interval clock")
		}
		if _, err := schedule.Next(time.Now(), "daemon", 1); err == nil {
			t.Fatal("continuous obtained a periodic clock")
		}
	}
	if (Schedule{}).ContinuousDelay() != 5*time.Second {
		t.Fatal("default restart delay")
	}
	for _, body := range []string{`{"type":"continuous","restart_delay":-1}`, `{"type":"continuous","restart_delay":31536001}`, `{"type":"continuous","restart_delay":9223372036854775807}`, `{"type":"continuous","interval":1}`, `{"type":"continuous","run_on_startup":true}`, `{"type":"continuous","trigger":"service.completed:*"}`, `{"type":"continuous","cron":"* * * * *"}`} {
		var schedule Schedule
		if err := json.Unmarshal([]byte(body), &schedule); err != nil {
			t.Fatal(err)
		}
		if err := (Unit{Trigger: "continuous", Schedule: &schedule}).ValidateRuntime(); err == nil {
			t.Fatal("accepted", body)
		}
	}
	zero := 0
	if err := (Unit{Trigger: "interval", Schedule: &Schedule{Type: "interval", Interval: 1, RestartDelay: &zero}}).ValidateRuntime(); err == nil {
		t.Fatal("interval accepted restart delay")
	}
}
