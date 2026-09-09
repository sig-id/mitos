package service

import (
	"math"
	"testing"
	"time"
)

func TestRestartDefaultsBoundsAndCappedBackoff(t *testing.T) {
	defaults := (RestartPolicy{}).Effective()
	if defaults.OnFailure != "skip" || defaults.MaxRetries != 2 || defaults.Delay(1) != 5*time.Second || defaults.Delay(3) != 20*time.Second || defaults.Delay(32) != 300*time.Second {
		t.Fatal("restart defaults or backoff wrong")
	}
	zero := 0
	rules := (RestartPolicy{OnFailure: "retry", MaxRetries: &zero, Backoff: Backoff{Initial: 1, Multiplier: 1.5, Max: 4}}).Effective()
	if err := rules.Validate(); err != nil || rules.MaxRetries != 0 || rules.Delay(3) != 2250*time.Millisecond || rules.Delay(32) != 4*time.Second {
		t.Fatal("explicit zero or fractional backoff lost", err)
	}
	for _, mutate := range []func(*RestartRules){func(r *RestartRules) { r.OnFailure = "restart" }, func(r *RestartRules) { r.MaxRetries = 33 }, func(r *RestartRules) { r.MaxRetries = -1 }, func(r *RestartRules) { r.Backoff.Initial = -1 }, func(r *RestartRules) { r.Backoff.Multiplier = math.Inf(1) }, func(r *RestartRules) { r.Backoff.Multiplier = .5 }, func(r *RestartRules) { r.Backoff.Max = 1 }} {
		bad := defaults
		mutate(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid restart policy accepted")
		}
	}
}
