package studio

import (
	"bytes"
	"slices"
	"sort"
	"strings"
	"time"
)

// CalibrationGroup compares what requesters declared with what attempts used.
// Planners' size and intelligence estimates are only useful if they are
// calibrated, so the policy is tuned from this evidence, not assumed.
type CalibrationGroup struct {
	Requester    string `json:"requester"` // operator, orchestrator, or proposal:<role>
	Nature       string `json:"nature"`
	Size         string `json:"size"`
	Intelligence string `json:"intelligence"`
	Harness      string `json:"harness"`
	Tier         string `json:"tier"`
	Attempts     int    `json:"attempts"`
	Finished     int    `json:"finished"`
	Accepted     int    `json:"accepted"`
	// Budget exhaustion signals an underestimated size; escalations to higher
	// intelligence signal an underestimated intelligence requirement.
	BudgetExhausted        int     `json:"budget_exhausted"`
	SizeEscalations        int     `json:"size_escalations"`
	IntelligenceEscalation int     `json:"intelligence_escalations"`
	BudgetMinutes          int     `json:"budget_minutes"`
	BudgetTurns            int     `json:"budget_turns"`
	MedianMinutes          float64 `json:"median_minutes"`
	MedianTurns            float64 `json:"median_turns"`
}

type calibrationKey struct{ requester, nature, size, intelligence, harness, tier string }

// ExecutionCalibration groups a project's settled native worker attempts by
// requester and the traits and selection each attempt ran with.
func (s *Store) ExecutionCalibration(project string) ([]CalibrationGroup, error) {
	type sample struct {
		key      calibrationKey
		budget   SizeBudget
		run      string
		minutes  float64
		finished bool
		accepted bool
		budgeted bool
		axis     string
	}
	samples := []sample{}
	s.mu.RLock()
	for _, g := range s.graphs {
		if g.ProjectID != project {
			continue
		}
		for _, m := range g.Members {
			if m.OrchestratorFor != "" || m.Executor == nil || m.Executor.Application.Agent == nil {
				continue
			}
			requester := m.Requester
			if rec := g.Proposals[m.ProposedBy]; rec != nil {
				if proposer := g.Members[rec.TaskID]; proposer != nil && proposer.Executor != nil && proposer.Executor.Application.Agent != nil {
					requester = "proposal:" + proposer.Executor.Application.Agent.Definition.ID
				}
			}
			for _, id := range m.Attempts {
				r := s.runs[id]
				if r == nil || !r.Terminal() || r.GraphAttempt == nil || r.GraphAttempt.Executor.Application.Agent == nil {
					continue
				}
				sel := r.GraphAttempt.Executor.Application.Agent.Selection
				if sel == nil || sel.Traits.Size == "" {
					continue
				}
				smp := sample{key: calibrationKey{requester, sel.Traits.Nature, sel.Traits.Size, sel.Traits.Intelligence, sel.HarnessName, sel.Tier}, budget: sel.Budget, run: id, finished: r.Status == "done", accepted: m.Accepted != nil && m.Accepted.RunID == id}
				smp.minutes = r.UpdatedAt.Sub(r.CreatedAt).Minutes()
				if start := runStarted(r); !start.IsZero() {
					smp.minutes = r.UpdatedAt.Sub(start).Minutes()
				}
				smp.budgeted = strings.Contains(r.Summary, "time limit was reached") || strings.Contains(r.Summary, "model steps")
				for _, x := range m.Escalations {
					if x.RunID == id {
						smp.axis = x.Axis
					}
				}
				samples = append(samples, smp)
			}
		}
	}
	s.mu.RUnlock()
	groups := map[calibrationKey]*CalibrationGroup{}
	minutes, turns := map[calibrationKey][]float64{}, map[calibrationKey][]float64{}
	for _, smp := range samples {
		k := smp.key
		c := groups[k]
		if c == nil {
			c = &CalibrationGroup{Requester: k.requester, Nature: k.nature, Size: k.size, Intelligence: k.intelligence, Harness: k.harness, Tier: k.tier, BudgetMinutes: smp.budget.Minutes, BudgetTurns: smp.budget.MaxTurns}
			groups[k] = c
		}
		c.Attempts++
		if smp.finished {
			c.Finished++
		}
		if smp.accepted {
			c.Accepted++
		}
		if smp.budgeted {
			c.BudgetExhausted++
		}
		switch smp.axis {
		case "size":
			c.SizeEscalations++
		case "intelligence":
			c.IntelligenceEscalation++
		}
		minutes[k] = append(minutes[k], smp.minutes)
		// Turns are model steps: completed tool actions plus the final answer.
		if activity, err := s.Artifact(smp.run, "activity.jsonl"); err == nil {
			n := float64(bytes.Count(activity, []byte(`"kind":"tool"`)))
			if smp.finished {
				n++
			}
			turns[k] = append(turns[k], n)
		}
	}
	out := []CalibrationGroup{}
	for k, c := range groups {
		c.MedianMinutes, c.MedianTurns = medianOf(minutes[k]), medianOf(turns[k])
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		return strings.Join([]string{a.Requester, a.Nature, a.Size, a.Intelligence, a.Harness, a.Tier}, "\x00") < strings.Join([]string{b.Requester, b.Nature, b.Size, b.Intelligence, b.Harness, b.Tier}, "\x00")
	})
	return out, nil
}

func runStarted(r *Run) time.Time {
	for _, e := range r.Events {
		if e.Kind == "agent_started" {
			return e.At
		}
	}
	return time.Time{}
}

func medianOf(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	v = slices.Clone(v)
	slices.Sort(v)
	if len(v)%2 == 1 {
		return v[len(v)/2]
	}
	return (v[len(v)/2-1] + v[len(v)/2]) / 2
}
