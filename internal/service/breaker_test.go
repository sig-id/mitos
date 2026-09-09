package service

import (
	"reflect"
	"testing"
	"time"
)

func TestCircuitThresholdCooldownProbesAndStaleOutcomes(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	c := NewCircuit(BreakerConfig{Enabled: true, Threshold: 2, Cooldown: 10, HalfOpenRequests: 2}, 8)
	var first, late BreakerBinding
	c, first, _ = c.Admit(now)
	c, late, _ = c.Admit(now)
	c, _ = c.Outcome(first, "failed", now)
	if c.State != "closed" || c.Failures != 1 {
		t.Fatal("threshold opened early")
	}
	var second BreakerBinding
	c, second, _ = c.Admit(now)
	c, _ = c.Outcome(second, "failed", now)
	if c.State != "open" || c.Available(now.Add(9*time.Second)) {
		t.Fatal("cooldown not enforced")
	}
	unchanged, applied := c.Outcome(late, "done", now)
	if applied || !reflect.DeepEqual(c, unchanged) {
		t.Fatal("late success closed a new generation")
	}
	c, first, _ = c.Admit(now.Add(10 * time.Second))
	c, second, _ = c.Admit(now.Add(10 * time.Second))
	if c.State != "half-open" || !first.Probe || c.Available(now.Add(20*time.Second)) {
		t.Fatal("probe limit not enforced")
	}
	c, _ = c.Outcome(first, "done", now.Add(11*time.Second))
	if c.State != "half-open" || c.Succeeded != 1 {
		t.Fatal("partial probe success closed circuit")
	}
	c, _ = c.Outcome(second, "done", now.Add(12*time.Second))
	if c.State != "closed" || c.Failures != 0 || !c.Available(now) {
		t.Fatal("successful probe cohort did not close circuit")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestCircuitProbeFailureCancellationAndPolicyEpoch(t *testing.T) {
	now := time.Now().UTC()
	c := NewCircuit(BreakerConfig{Enabled: true, Threshold: 1, Cooldown: 1}, 1)
	c, b, _ := c.Admit(now)
	c, _ = c.Outcome(b, "failed", now)
	c, b, _ = c.Admit(now.Add(time.Second))
	c, _ = c.Outcome(b, "cancelled", now.Add(time.Second))
	if c.Issued != 0 || !c.Available(now.Add(time.Second)) || c.State != "half-open" {
		t.Fatal("cancelled probe did not release its reservation")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c, b, _ = c.Admit(now.Add(time.Second))
	c, _ = c.Outcome(b, "failed", now.Add(time.Second))
	if c.State != "open" || !c.OpenUntil.Equal(now.Add(2*time.Second)) {
		t.Fatal("failed probe did not renew cooldown")
	}
	fresh := NewCircuit(c.Config, 2)
	if _, applied := fresh.Outcome(b, "failed", now); applied {
		t.Fatal("old policy outcome changed new policy")
	}
	for _, config := range []BreakerConfig{{Threshold: -1}, {Cooldown: -1}, {HalfOpenRequests: 33}} {
		if config.Validate() == nil {
			t.Fatal("invalid config accepted")
		}
	}
}
