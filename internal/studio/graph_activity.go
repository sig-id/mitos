package studio

import (
	"encoding/json"
	"slices"
	"strings"
	"time"

	"mitos/internal/agentrouting"
)

type GraphPendingRequest struct {
	Resolver      string        `json:"resolver"`
	Reason        string        `json:"reason,omitempty"`
	SetupRequired bool          `json:"setup_required,omitempty"`
	Decision      HumanDecision `json:"decision"`
	Blocking      bool          `json:"blocking"`
	Input         string        `json:"input"`
	Command       string        `json:"command,omitempty"`
	Directory     string        `json:"directory,omitempty"`
}

// Caller holds the store lock. Native requests have their own review runs; join
// them through their bound session instead of treating them as graph attempts.
func (s *Store) graphPendingRequests(runID string) []GraphPendingRequest {
	var pending []GraphPendingRequest
	if runID == "" {
		return pending
	}
	if r := s.runs[runID]; r != nil && r.Decision != nil && !r.Terminal() {
		d := r.Decision
		if d.AppliedAt == nil && d.SupersededAt == nil && !d.Due(time.Now()) {
			pending = append(pending, GraphPendingRequest{Decision: *d, Blocking: true, Input: d.InputProfile(), Resolver: "human"})
		}
	}
	for _, request := range s.agentRequests {
		session := s.agentSessions[request.SessionID]
		if session.RunID != runID || session.Closed || request.Invalidated || time.Now().After(session.LeaseUntil) {
			continue
		}
		for _, review := range request.Runs {
			r := s.runs[review.ID]
			if r == nil || r.Decision == nil {
				continue
			}
			d := r.Decision
			if d.AppliedAt != nil || d.SupersededAt != nil || d.Due(time.Now()) {
				continue
			}
			command, directory, _ := agentrouting.Command(request.Request)
			route, setup := s.requestReview(session, request)
			pending = append(pending, GraphPendingRequest{Decision: *d, Blocking: request.Request.Method != "mitos/asyncQuestion", Input: d.InputProfile(), Command: command, Directory: directory, Resolver: route.Resolver, Reason: route.Reason, SetupRequired: setup})
		}
	}
	slices.SortFunc(pending, func(a, b GraphPendingRequest) int { return a.Decision.RequestedAt.Compare(b.Decision.RequestedAt) })
	return pending
}

// Project current policy immediately, before the independent resolver goroutine
// finishes, so an automatic review never briefly appears as a human approval.
// Caller holds the store lock. This projection does not authorize any operation.
func (s *Store) requestReview(session AgentSession, request AgentRequest) (agentrouting.Route, bool) {
	p := s.agentPolicy(session.ProjectID)
	route := agentrouting.Prepare(p, request.Request, session.Workspace)
	if saved, ok := s.agentRoutes[session.ID+"/"+request.Request.Key]; ok && saved.PolicyRevision == p.Revision {
		route = saved.Route
	}
	_, _, command := agentrouting.Command(request.Request)
	supported := command || len(request.Request.Questions) > 0 && agentrouting.CanChoose(request.Request)
	missing := p.DecisionProvider == nil && p.DecisionApplication == ""
	if missing && supported && (route.Resolver == "decision" || route.Resolver == "classify" || route.Reason == "automatic resolution is disabled" || route.Reason == "decision provider is not configured") {
		route.Resolver = "human"
		route.Reason = "Configure the Decisions API and approval boundaries to review routine requests automatically."
		return route, true
	}
	if route.Resolver == "human" && route.Source == "fallback" && (route.Reason == "credential_unavailable" || route.Reason == "transport_failed" || route.Reason == "decision provider is not configured" || strings.HasPrefix(route.Reason, "http_")) {
		route.Reason = "The Decisions API is unavailable (" + route.Reason + "). Check the API connection in approval settings."
		return route, true
	}
	return route, false
}

type GraphActivityEvent struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind"`
	Summary string    `json:"summary"`
}

type GraphRunActivity struct {
	Status     string                `json:"status"`
	CreatedAt  time.Time             `json:"created_at"`
	StartedAt  *time.Time            `json:"started_at,omitempty"`
	UpdatedAt  time.Time             `json:"updated_at"`
	FinishedAt time.Time             `json:"finished_at"`
	Requests   []GraphPendingRequest `json:"requests"`
	Events     []GraphActivityEvent  `json:"events"`
}

func (s *Store) GraphActivity(graph, task, id string) (GraphRunActivity, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r := s.runs[id]
	if r == nil || r.TaskID != task || r.GraphAttempt == nil || r.GraphAttempt.GraphID != graph {
		return GraphRunActivity{}, ErrNotFound
	}
	v := GraphRunActivity{Status: r.Status, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, FinishedAt: r.FinishedAt, Requests: s.graphPendingRequests(id)}
	for i, event := range r.Events {
		var data struct {
			Status  string `json:"status"`
			Summary string `json:"summary"`
		}
		_ = json.Unmarshal(event.Data, &data)
		if v.StartedAt == nil && data.Status == "running" {
			at := event.At
			v.StartedAt = &at
		}
		if i >= len(r.Events)-40 {
			v.Events = append(v.Events, GraphActivityEvent{At: event.At, Kind: event.Kind, Summary: data.Summary})
		}
	}
	for _, request := range s.agentRequests {
		if s.agentSessions[request.SessionID].RunID != id {
			continue
		}
		for _, review := range request.Runs {
			if saved := s.runs[review.ID]; saved != nil && saved.Decision != nil {
				d := saved.Decision
				v.Events = append(v.Events, GraphActivityEvent{At: d.RequestedAt, Kind: "agent_request_received", Summary: d.Question})
				if d.ResolvedAt != nil {
					v.Events = append(v.Events, GraphActivityEvent{At: *d.ResolvedAt, Kind: "agent_answer_recorded", Summary: "Answer recorded: " + d.Choice})
				}
				if d.AppliedAt != nil {
					v.Events = append(v.Events, GraphActivityEvent{At: *d.AppliedAt, Kind: "agent_answer_delivered", Summary: "The agent received the answer."})
				}
			}
		}
	}
	slices.SortStableFunc(v.Events, func(a, b GraphActivityEvent) int { return a.At.Compare(b.At) })
	if len(v.Events) > 40 {
		v.Events = v.Events[len(v.Events)-40:]
	}
	return graphClone(v), nil
}
