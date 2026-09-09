package service

import (
	"testing"
	"time"
)

func TestIntervalSchemaBoundsAndDeterministicJitter(t *testing.T) {
	base := Unit{ID: "worker", Scope: "project", Trigger: "interval", Schedule: &Schedule{Type: "interval", Interval: 60, Jitter: 10}}
	if err := base.ValidateRuntime(); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	next, err := base.Schedule.Next(now, "worker", 9)
	if err != nil {
		t.Fatal(err)
	}
	again, err := base.Schedule.Next(now, "worker", 9)
	if err != nil {
		t.Fatal(err)
	}
	if next.Before(now.Add(time.Minute)) || next.After(now.Add(70*time.Second)) || !next.Equal(again) {
		t.Fatal("jitter unbounded or nondeterministic")
	}
	for _, mutate := range []func(*Unit){
		func(u *Unit) { u.Schedule.Interval = 0 }, func(u *Unit) { u.Schedule.Jitter = 61 }, func(u *Unit) { u.Trigger = "cron" }, func(u *Unit) { u.Concurrency.IfRunning = "unknown-overlap" }, func(u *Unit) { u.Concurrency.MaxInstances = 33 }, func(u *Unit) { u.Concurrency.QueueMax = -1 },
	} {
		u := base
		s := *base.Schedule
		u.Schedule = &s
		mutate(&u)
		if err := u.ValidateRuntime(); err == nil {
			t.Fatal("invalid runtime accepted")
		}
	}
}
