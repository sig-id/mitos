package studio

import (
	"errors"
	"testing"
)

func TestConfiguredCapacityDeficitFairnessAndTenantConcurrency(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	s := e.Store
	a, _ := serviceTestPrincipal(t, s, "alpha", "a", true)
	b, _ := serviceTestPrincipal(t, s, "beta", "b", true)
	policy := defaultRuntimePolicy()
	policy.Capacity = 4
	policy.Default.Concurrency = 2
	policy.Default.Queue = 8
	weighted := policy.Default
	weighted.Weight = 2
	policy.Tenants[tenantPolicyKey(a.Tenant)] = weighted
	if err := s.ConfigureRuntimePolicy(policy); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct {
		principalID string
		admit       func(Brief) (Run, error)
	}{{"a", e.Application(a).Admit}, {"b", e.Application(b).Admit}} {
		for _, key := range []string{"one", "two", "three"} {
			if _, err := p.admit(structuredBrief(key, "agent:mitos.generalist")); err != nil {
				t.Fatal(err)
			}
		}
	}
	active := []string{}
	order := []string{}
	for range 4 {
		ready, err := s.ReadyRuns(active)
		if err != nil || len(ready) == 0 {
			t.Fatal("no fair candidate", err)
		}
		candidate := ready[0]
		ok, err := s.ScheduleSelected(candidate.Run.ID, active, candidate.Policy)
		if err != nil || !ok {
			t.Fatal("capacity grant", ok, err)
		}
		active = append(active, candidate.Run.ID)
		order = append(order, candidate.Run.Tenant.Org)
	}
	if order[0] != "a" || order[1] != "a" || order[2] != "b" || order[3] != "b" {
		t.Fatal("unit-cost DRR/tenant concurrency", order)
	}
	if ready, err := s.ReadyRuns(active); err != nil || len(ready) != 0 {
		t.Fatal("global capacity exceeded", len(ready), err)
	}
	if s.RuntimeCapacity() != 4 {
		t.Fatal("hard-coded capacity retained")
	}
}

func TestTenantQueueAndAttemptBudgetsSurviveRestart(t *testing.T) {
	e, _ := automaticScriptEngine(t, `print('unused')`)
	e.stop()
	e.wg.Wait()
	s := e.Store
	p, token := serviceTestPrincipal(t, s, "app", "tenant", true)
	policy := defaultRuntimePolicy()
	policy.Default.Queue = 1
	policy.Default.Attempts = 2
	if err := s.ConfigureRuntimePolicy(policy); err != nil {
		t.Fatal(err)
	}
	r, err := e.Application(p).Admit(structuredBrief("once", "agent:mitos.generalist"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Application(p).Admit(structuredBrief("excess", "agent:mitos.generalist")); !errors.Is(err, ErrTenantCapacity) {
		t.Fatal("queue limit ignored", err)
	}
	if ok, err := s.Schedule(r.ID, nil); err != nil || !ok {
		t.Fatal(ok, err)
	}
	root := e.Root
	e.Close()
	s.Close()
	for attempt := 0; attempt < 2; attempt++ {
		s, err = OpenStore(s.Dir())
		if err != nil {
			t.Fatal(err)
		}
		e, err = NewEngine(s, nil, root)
		if err != nil {
			t.Fatal(err)
		}
		e.stop()
		e.wg.Wait()
		task, err := s.Task(r.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		current, err := s.Get(task.CurrentRunID)
		if err != nil {
			t.Fatal(err)
		}
		if attempt == 0 {
			if current.ID == r.ID || current.ParentID != r.ID || current.TaskID != r.TaskID || current.Brief.Structured.RequestHash != r.Brief.Structured.RequestHash {
				t.Fatal("replacement lost admitted identity")
			}
			if ok, err := s.Schedule(current.ID, nil); err != nil || !ok {
				t.Fatal(ok, err)
			}
		} else {
			if current.Status != "failed" || current.RestartRetry || current.Decision != nil {
				t.Fatal("unbounded/human-dependent restart recovery", current.Status)
			}
		}
		if _, err = s.AuthenticateServicePrincipal(token); err != nil {
			t.Fatal("restart discarded principal", err)
		}
		e.Close()
		s.Close()
	}
}
