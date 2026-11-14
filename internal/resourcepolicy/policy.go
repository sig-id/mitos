// Package resourcepolicy separates workflow consumption budgets from the
// instantaneous resources available to each agent execution.
package resourcepolicy

import "fmt"

// An omitted limit inherits. Unlimited is explicit and never encoded as zero.
// Amount uses the unit named by its containing field.
type Limit struct {
	Mode   string `json:"mode,omitempty"`
	Amount int64  `json:"amount,omitempty"`
}
type Workflow struct {
	ElapsedMinutes Limit `json:"elapsed_minutes"`
	Attempts       Limit `json:"attempts"`
	Tokens         Limit `json:"tokens"`
}
type Agent struct {
	CPUMillicores Limit `json:"cpu_millicores"`
	MemoryMiB     Limit `json:"memory_mib"`
}
type Policy struct {
	Workflow Workflow `json:"workflow"`
	Agent    Agent    `json:"agent"`
}

// Enforcement describes guarantees, not availability of usage telemetry.
type Enforcement struct {
	Elapsed  bool
	Attempts bool
	Tokens   bool
	CPU      bool
	Memory   bool
}
type Control struct {
	Path           string `json:"path"`
	Label          string `json:"label"`
	Scope          string `json:"scope"`
	Unit           string `json:"unit"`
	Max            int64  `json:"max"`
	Enforced       bool   `json:"enforced"`
	AllowUnlimited bool   `json:"allow_unlimited"`
	Detail         string `json:"detail"`
}

func Capped(amount int64) Limit { return Limit{Mode: "limit", Amount: amount} }
func (l Limit) Inherits() bool  { return l.Mode == "" || l.Mode == "inherit" }
func Defaults() Policy {
	return Policy{Workflow: Workflow{ElapsedMinutes: Capped(120), Attempts: Capped(96), Tokens: Limit{Mode: "unlimited"}}, Agent: Agent{CPUMillicores: Limit{Mode: "unlimited"}, MemoryMiB: Limit{Mode: "unlimited"}}}
}
func Controls(e Enforcement) []Control {
	return []Control{
		{"workflow.elapsed_minutes", "Elapsed time", "workflow", "minutes", 43200, e.Elapsed, false, "Includes waiting and approval time. The workflow is stopped when its deadline is reached."},
		{"workflow.attempts", "Agent attempts", "workflow", "attempts", 2048, e.Attempts, false, "Shared by planner calls, workers, and retries. Used attempts are not refunded."},
		{"workflow.tokens", "Model tokens", "workflow", "tokens", 1000000000000, e.Tokens, true, "A shared input/output token budget needs a metered model gateway with reservations. Native CLI credentials do not provide that guarantee."},
		{"agent.cpu_millicores", "CPU capacity", "agent", "millicores", 1024000, e.CPU, true, "1000 millicores is one CPU core. A quota must cover the agent and all child processes; local process execution cannot enforce it."},
		{"agent.memory_mib", "Memory", "agent", "MiB", 1073741824, e.Memory, true, "A memory cap must cover the agent and all child processes. It requires a provider with an enforced memory boundary."},
	}
}
func (p Policy) limits() []Limit {
	return []Limit{p.Workflow.ElapsedMinutes, p.Workflow.Attempts, p.Workflow.Tokens, p.Agent.CPUMillicores, p.Agent.MemoryMiB}
}
func (p Policy) Validate() error {
	for i, c := range Controls(Enforcement{}) {
		l := p.limits()[i]
		switch l.Mode {
		case "", "inherit":
			if l.Amount != 0 {
				return fmt.Errorf("%s: an inherited limit cannot specify an amount", c.Path)
			}
		case "unlimited":
			if !c.AllowUnlimited {
				return fmt.Errorf("%s: this runtime requires a finite limit", c.Path)
			}
			if l.Amount != 0 {
				return fmt.Errorf("%s: no cap cannot specify an amount", c.Path)
			}
		case "limit":
			if l.Amount < 1 || l.Amount > c.Max {
				return fmt.Errorf("%s: amount must be 1–%d %s; zero does not mean unlimited", c.Path, c.Max, c.Unit)
			}
		default:
			return fmt.Errorf("%s: mode must be inherit, limit, or unlimited", c.Path)
		}
	}
	return nil
}
func (p Policy) Check(e Enforcement) error {
	if err := p.Validate(); err != nil {
		return err
	}
	for i, c := range Controls(e) {
		if p.limits()[i].Mode == "limit" && !c.Enforced {
			return fmt.Errorf("%s: an enforced cap is unavailable with this runtime. %s", c.Path, c.Detail)
		}
	}
	return nil
}
func (a Agent) Check(cpu, memory bool) error {
	return (Policy{Agent: a}).Check(Enforcement{CPU: cpu, Memory: memory})
}
func choose(base, override Limit) Limit {
	if override.Inherits() {
		return base
	}
	return override
}

// Resolve overlays one layer field by field. Call with Defaults as the first
// layer, then personal defaults, local project preferences, and project policy.
func Resolve(base, override Policy) Policy {
	return Policy{Workflow: Workflow{ElapsedMinutes: choose(base.Workflow.ElapsedMinutes, override.Workflow.ElapsedMinutes), Attempts: choose(base.Workflow.Attempts, override.Workflow.Attempts), Tokens: choose(base.Workflow.Tokens, override.Workflow.Tokens)}, Agent: Agent{CPUMillicores: choose(base.Agent.CPUMillicores, override.Agent.CPUMillicores), MemoryMiB: choose(base.Agent.MemoryMiB, override.Agent.MemoryMiB)}}
}
