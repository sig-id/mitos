package studio

import (
	"errors"
	"sort"
	"time"

	"mitos/internal/decision"
)

type TenantLimits struct {
	Concurrency    int `json:"concurrency"`
	Queue          int `json:"queue"`
	Attempts       int `json:"attempts_per_task"`
	ElapsedMinutes int `json:"elapsed_minutes_per_task"`
	Weight         int `json:"weight"`
}
type RuntimePolicy struct {
	Capacity int                     `json:"capacity"`
	Default  TenantLimits            `json:"default_tenant"`
	Tenants  map[string]TenantLimits `json:"tenants,omitempty"`
}
type TenantScheduler struct {
	Cursor  string         `json:"cursor"`
	Credits map[string]int `json:"credits"`
}

var ErrTenantCapacity = errors.New("tenant_capacity_exceeded")
var ErrTenantBudget = errors.New("tenant_budget_exhausted")

func defaultRuntimePolicy() RuntimePolicy {
	return RuntimePolicy{Capacity: 2, Default: TenantLimits{Concurrency: 2, Queue: 64, Attempts: 4, ElapsedMinutes: 120, Weight: 1}, Tenants: map[string]TenantLimits{}}
}
func tenantPolicyKey(t decision.Tenant) string { return t.Org + "/" + t.Project }
func (s *Store) runtimePolicyLocked() RuntimePolicy {
	if s.runtimePolicy == nil {
		return defaultRuntimePolicy()
	}
	return *s.runtimePolicy
}
func (s *Store) RuntimePolicy() RuntimePolicy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return graphClone(s.runtimePolicyLocked())
}
func (s *Store) RuntimeCapacity() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.runtimePolicyLocked().Capacity
}
func (s *Store) tenantLimitsLocked(t decision.Tenant) TenantLimits {
	p := s.runtimePolicyLocked()
	if limits, ok := p.Tenants[tenantPolicyKey(t)]; ok {
		return limits
	}
	return p.Default
}
func (s *Store) ConfigureRuntimePolicy(p RuntimePolicy) error {
	if p.Capacity < 1 || p.Capacity > 128 || len(p.Tenants) > 1024 {
		return errors.New("capacity must be 1..128 with at most 1024 tenant overrides")
	}
	valid := func(l TenantLimits) bool {
		return l.Concurrency >= 1 && l.Concurrency <= p.Capacity && l.Queue >= 1 && l.Queue <= 100000 && l.Attempts >= 1 && l.Attempts <= 128 && l.ElapsedMinutes >= 1 && l.ElapsedMinutes <= 10080 && l.Weight >= 1 && l.Weight <= 16
	}
	if !valid(p.Default) {
		return errors.New("invalid default tenant limits")
	}
	for key, l := range p.Tenants {
		if key == "" || !valid(l) {
			return errors.New("invalid tenant limits")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	reserved := 0
	for _, pool := range s.configuredPools() {
		reserved += pool.Reserved
	}
	if reserved > p.Capacity {
		return errors.New("capacity is below reserved pool slots")
	}
	return s.commit("", "effect", "runtime_policy_changed", change{RuntimePolicy: &p})
}

func (s *Store) tenantAdmissionLocked(p decision.Principal, b Brief, parent string) error {
	if b.Structured == nil {
		return nil
	}
	limits := s.tenantLimitsLocked(p.Tenant)
	currentQueueLimit := limits.Queue
	if parent != "" {
		r := s.runs[parent]
		if r == nil {
			return ErrNotFound
		}
		task := s.tasks[r.TaskID]
		if task == nil {
			return ErrNotFound
		}
		if r.AdmissionLimits != nil {
			limits = *r.AdmissionLimits
		}
		attempts := 0
		for _, run := range s.runs {
			if run.TaskID == task.ID {
				attempts++
			}
		}
		if attempts >= limits.Attempts || !time.Now().Before(task.CreatedAt.Add(time.Duration(limits.ElapsedMinutes)*time.Minute)) {
			return ErrTenantBudget
		}
		return s.tenantQueueAdmissionLocked(p.Tenant, currentQueueLimit)
	}
	queued := 0
	for _, r := range s.runs {
		if runTenant(r) == p.Tenant && r.Brief.Structured != nil && (r.Status == "queued" || r.Status == "blocked") {
			queued++
		}
	}
	if queued >= limits.Queue {
		return ErrTenantCapacity
	}
	if b.TimeoutMinutes > limits.ElapsedMinutes {
		return ErrTenantBudget
	}
	return nil
}
func (s *Store) tenantCanDispatchLocked(r *Run, active []string) bool {
	limits := s.tenantLimitsLocked(runTenant(r))
	used := 0
	for _, id := range active {
		if other := s.runs[id]; other != nil && runTenant(other) == runTenant(r) {
			used++
		}
	}
	return used < limits.Concurrency
}
func (s *Store) nextTenantScheduler(r *Run) *TenantScheduler {
	state := graphClone(s.tenantScheduler)
	if state.Credits == nil {
		state.Credits = map[string]int{}
	}
	key := tenantPolicyKey(runTenant(r))
	if state.Credits[key] <= 0 {
		state.Credits[key] = s.tenantLimitsLocked(runTenant(r)).Weight
	}
	state.Credits[key]--
	state.Cursor = key
	return &state
}

// Unit-cost deficit round-robin retains each pool's internal ordering while
// selecting tenants by bounded quantum. The credit decrement is journaled in
// the same claim/admission transaction, not spent by a speculative ready read.
func (s *Store) fairReadyRuns(runs []ReadyRun) []ReadyRun {
	byTenant := map[string][]ReadyRun{}
	keys := []string{}
	for _, r := range runs {
		key := tenantPolicyKey(runTenant(&r.Run))
		if _, ok := byTenant[key]; !ok {
			keys = append(keys, key)
		}
		byTenant[key] = append(byTenant[key], r)
	}
	sort.Strings(keys)
	state := graphClone(s.tenantScheduler)
	if state.Credits == nil {
		state.Credits = map[string]int{}
	}
	out := make([]ReadyRun, 0, len(runs))
	for len(out) < len(runs) {
		chosen := ""
		if len(byTenant[state.Cursor]) > 0 && state.Credits[state.Cursor] > 0 {
			chosen = state.Cursor
		} else {
			start := sort.Search(len(keys), func(i int) bool { return keys[i] > state.Cursor })
			for i := 0; i < len(keys); i++ {
				key := keys[(start+i)%len(keys)]
				if len(byTenant[key]) > 0 {
					chosen = key
					break
				}
			}
		}
		if chosen == "" {
			break
		}
		candidate := byTenant[chosen][0]
		byTenant[chosen] = byTenant[chosen][1:]
		if state.Credits[chosen] <= 0 {
			state.Credits[chosen] = s.tenantLimitsLocked(runTenant(&candidate.Run)).Weight
		}
		state.Credits[chosen]--
		state.Cursor = chosen
		out = append(out, candidate)
	}
	return out
}

func (s *Store) structuredTaskDeadline(r *Run) time.Time {
	if r.Brief.Structured == nil {
		return time.Time{}
	}
	limits := s.tenantLimitsLocked(runTenant(r))
	if r.AdmissionLimits != nil {
		limits = *r.AdmissionLimits
	}
	at := r.CreatedAt
	if task := s.tasks[r.TaskID]; task != nil {
		at = task.CreatedAt
	}
	return at.Add(time.Duration(limits.ElapsedMinutes) * time.Minute)
}

func (s *Store) tenantQueueAdmissionLocked(tenant decision.Tenant, limit int) error {
	queued := 0
	for _, r := range s.runs {
		if runTenant(r) == tenant && r.Brief.Structured != nil && (r.Status == "queued" || r.Status == "blocked") {
			queued++
		}
	}
	if queued >= limit {
		return ErrTenantCapacity
	}
	return nil
}
