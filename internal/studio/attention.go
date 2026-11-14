package studio

import (
	"mitos/internal/decision"
	"sort"
)

// Attention joins native approval runs back to the work that is waiting on them.
// It is a read projection; resolving a card still uses the canonical decision API.
type Attention struct {
	SetupRequired bool          `json:"setup_required,omitempty"`
	InputProfile  string        `json:"input_profile"`
	Blocking      bool          `json:"blocking"`
	ID            string        `json:"id"`
	Title         string        `json:"title"`
	ProjectID     string        `json:"project_id,omitempty"`
	GraphID       string        `json:"graph_id,omitempty"`
	TaskID        string        `json:"task_id,omitempty"`
	OwnerRunID    string        `json:"owner_run_id,omitempty"`
	Reason        string        `json:"reason"`
	Resolver      string        `json:"resolver"`
	Card          decision.Card `json:"card"`
}

func (s *Store) attention() []Attention {
	out := []Attention{}
	for _, r := range s.runs {
		if r.Sample || r.Decision == nil || r.Decision.ResolvedAt != nil || r.Decision.Choice != "" || r.Status != "parked" {
			continue
		}
		d := r.Decision
		a := Attention{Blocking: true, ID: d.ID, Title: r.Brief.Title, ProjectID: r.Brief.ProjectID, OwnerRunID: r.ID, Reason: d.Context.BlockedBy, Resolver: "human", Card: *d, InputProfile: d.InputProfile()}
		if d.Source != nil {
			session := s.agentSessions[d.Source.SessionID]
			request := s.agentRequests[d.Source.SessionID+"/"+d.Source.RequestID]
			a.Blocking = request.Request.Method != "mitos/asyncQuestion"
			a.ProjectID, a.OwnerRunID = session.ProjectID, session.RunID
			route, setup := s.requestReview(session, request)
			a.Reason, a.Resolver, a.SetupRequired = route.Reason, route.Resolver, setup
		}
		if owner := s.runs[a.OwnerRunID]; owner != nil {
			a.TaskID = owner.TaskID
			if owner.GraphAttempt != nil {
				a.GraphID = owner.GraphAttempt.GraphID
			}
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *Store) Attention() []Attention {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return graphClone(s.attention())
}
