package studio

import (
	"slices"
	"time"
)

// escalateMember raises one trait of a native worker for its next attempt. Each
// axis answers a different failure: size when the budget ran out, intelligence
// when the work failed on quality. Intelligence moves to the next tier of the
// same harness, so the next attempt resumes the native conversation with a
// stronger model; the harness itself never changes here, because a native
// conversation is tied to its harness. Caller holds the store lock.
func (e *Engine) escalateMember(g *WorkGraph, m *GraphMember, axis, runID string, at time.Time) error {
	if m.Executor == nil || m.Executor.Application.Agent == nil || m.Executor.Application.Agent.Selection == nil || m.Spec.Traits == nil {
		return graphError("only native worker tasks with declared traits can escalate")
	}
	b := m.Executor.Application.Agent
	sel := *b.Selection
	traits := sel.Traits
	var from, to string
	switch axis {
	case "size":
		from = traits.Size
		i := slices.Index(TaskSizes, from)
		if i < 0 || i+1 >= len(TaskSizes) {
			return graphError("task is already at the largest size")
		}
		to = TaskSizes[i+1]
		traits.Size = to
	case "intelligence":
		from = traits.Intelligence
		i := slices.Index(TaskIntelligences, from)
		if i < 0 || i+1 >= len(TaskIntelligences) {
			return graphError("task already requires the highest intelligence")
		}
		to = TaskIntelligences[i+1]
		traits.Intelligence = to
	default:
		return graphError("escalate must be size or intelligence")
	}
	p := graphExecution(g)
	if err := p.validate(); err != nil {
		return err
	}
	h, ok := p.harness(sel.Harness)
	if !ok {
		return graphError("the task's harness is no longer in the frozen policy")
	}
	tier, source := selectTier(p, traits)
	if axis == "intelligence" {
		source = "escalated: " + source
	}
	sel.Traits, sel.Tier, sel.TierSource, sel.Budget = traits, tier, source, p.SizeBudgets[traits.Size]
	if err := e.applySelection(b, h, sel); err != nil {
		return err
	}
	b.Digest = ""
	b.Digest = graphDigest(b)
	m.Executor.Digest = ""
	m.Executor.Digest = graphDigest(m.Executor)
	m.History = append(m.History, m.Spec)
	m.Spec.Traits = &traits
	m.Spec.Brief.TimeoutMinutes = sel.Budget.Minutes
	m.Revision++
	record := GraphEscalation{Axis: axis, From: from, To: to, RunID: runID, At: at}
	m.Escalations = append(m.Escalations, record)
	m.ContinueFrom = runID
	m.Continuation = &GraphContinuation{Reason: "escalation", Escalation: &record}
	return nil
}

// executorContinuity identifies what a native conversation depends on: the
// definition, harness, executable and profile. Model tier, effort and budget
// may change between attempts of one conversation; the harness may not.
func executorContinuity(x GraphExecutor) string {
	x = graphClone(x)
	x.Digest = ""
	if b := x.Application.Agent; b != nil {
		b.Model, b.Effort, b.Digest = "", "", ""
		if b.Selection != nil {
			s := *b.Selection
			b.Selection = &ExecutionSelection{Harness: s.Harness, HarnessName: s.HarnessName, HarnessSource: s.HarnessSource, Traits: TaskTraits{Nature: s.Traits.Nature}}
		}
	}
	return graphDigest(x)
}
