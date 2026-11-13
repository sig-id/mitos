package studio

import (
	"encoding/json"
	"errors"
	"time"

	"mitos/internal/decision"
	"mitos/internal/harness"
)

type AgentSession struct {
	ProjectID     string          `json:"project_id,omitempty"`
	DeskSessionID string          `json:"desk_session_id,omitempty"`
	RunID         string          `json:"run_id,omitempty"`
	EndedThreads  []string        `json:"ended_threads,omitempty"`
	ID            string          `json:"session_id"`
	Adapter       string          `json:"adapter"`
	Title         string          `json:"title"`
	Workspace     string          `json:"workspace"`
	Operator      string          `json:"operator"`
	Tenant        decision.Tenant `json:"tenant"`
	TokenHash     string          `json:"token_hash,omitempty"`
	LeaseUntil    time.Time       `json:"lease_until"`
	Closed        bool            `json:"closed"`
}
type AgentRequest struct {
	SessionID   string          `json:"session_id"`
	Request     harness.Request `json:"request"`
	Digest      string          `json:"digest"`
	Runs        []Run           `json:"runs"`
	Invalidated bool            `json:"invalidated"`
}
type AgentAnswer struct {
	Key    string          `json:"key"`
	Body   json.RawMessage `json:"body"`
	Digest string          `json:"digest"`
	State  string          `json:"state"`
}
type AgentRequestView struct {
	RequestID string          `json:"request_id"`
	RunIDs    []string        `json:"run_ids"`
	State     string          `json:"state"`
	Digest    string          `json:"digest"`
	Body      json.RawMessage `json:"body"`
}

func (s *Store) CreateAgentSession(adapter, title, workspace string, p decision.Principal) (AgentSession, string, error) {
	return s.CreateBoundAgentSession(adapter, title, workspace, "", "", "", p)
}

func (s *Store) CreateBoundAgentSession(adapter, title, workspace, project, sessionID, runID string, p decision.Principal) (AgentSession, string, error) {
	if !p.Authenticated || !p.Tenant.Valid() || p.ID == "" {
		return AgentSession{}, "", deviceError(403, "forbidden")
	}
	if adapter != "codex" && adapter != "claude" || title == "" || len(title) > 160 || workspace == "" {
		return AgentSession{}, "", deviceError(400, "malformed")
	}
	token, err := newDeviceToken()
	if err != nil {
		return AgentSession{}, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	session := AgentSession{ProjectID: project, DeskSessionID: sessionID, RunID: runID, ID: newID("agent"), Adapter: adapter, Title: title, Workspace: workspace, Operator: p.ID, Tenant: p.Tenant, TokenHash: tokenHash(token), LeaseUntil: time.Now().UTC().Add(30 * time.Second)}
	if err = s.commit("", "effect", "agent_session_started", change{AgentSession: &session}); err != nil {
		return AgentSession{}, "", err
	}
	session.TokenHash = ""
	return session, token, nil
}
func (s *Store) agentSession(id, token string) (AgentSession, error) {
	a, ok := s.agentSessions[id]
	if !ok || a.TokenHash != tokenHash(token) || token == "" {
		return a, deviceError(401, "unauthenticated")
	}
	if a.Closed || !time.Now().Before(a.LeaseUntil) {
		return a, deviceError(410, "session_ended")
	}
	return a, nil
}
func (s *Store) agentSourceActive(source *decision.Source) bool {
	a, ok := s.agentSessions[source.SessionID]
	r, exists := s.agentRequests[source.SessionID+"/"+source.RequestID]
	return ok && exists && !a.Closed && time.Now().Before(a.LeaseUntil) && !r.Invalidated
}
func (s *Store) AgentHeartbeat(id, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.agentSession(id, token)
	if err != nil {
		return err
	}
	a.LeaseUntil = time.Now().UTC().Add(30 * time.Second)
	return s.commit("", "effect", "agent_session_heartbeat", change{AgentSession: &a})
}
func (s *Store) CloseAgentSession(id, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.agentSessions[id]
	if !ok || a.TokenHash != tokenHash(token) {
		return deviceError(401, "unauthenticated")
	}
	if a.Closed {
		return nil
	}
	a.Closed = true
	return s.commit("", "effect", "agent_session_closed", change{AgentSession: &a})
}
func (s *Store) expireAgentSessions() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, a := range s.agentSessions {
		if !a.Closed && !time.Now().Before(a.LeaseUntil) {
			a.Closed = true
			if err := s.commit("", "effect", "agent_session_expired", change{AgentSession: &a}); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) IngestAgentRequest(id, token string, raw json.RawMessage) (AgentRequestView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.agentSession(id, token)
	if err != nil {
		return AgentRequestView{}, err
	}
	request, err := harness.Normalize(a.Adapter, raw)
	if err != nil {
		return AgentRequestView{}, deviceError(422, "native_console_required")
	}
	for _, thread := range a.EndedThreads {
		if thread == request.Thread {
			return AgentRequestView{}, deviceError(410, "session_ended")
		}
	}
	key := id + "/" + request.Key
	if old, ok := s.agentRequests[key]; ok {
		if old.Digest != tokenHash(string(raw)) {
			return AgentRequestView{}, deviceError(409, "source_conflict")
		}
		return s.agentRequestView(key)
	}
	saved := AgentRequest{SessionID: id, Request: request, Digest: tokenHash(string(raw)), Runs: []Run{}}
	now := time.Now().UTC()
	for i, c := range request.Cards {
		c.ID = newID("decision")
		c.RunID = newID("agent_run")
		c.Tenant = a.Tenant
		c.CausedBy = s.seq + 1
		c.RequestedAt = now
		c.DedupeKey = key + "/" + c.ID
		c.Source = &decision.Source{Adapter: a.Adapter, SessionID: id, RequestID: request.Key}
		if c.Validate(false) != nil {
			return AgentRequestView{}, deviceError(422, "native_console_required")
		}
		// Each question has its own durable review run so simultaneous requests and
		// multi-question native calls cannot overwrite another pending card.
		r := Run{ID: c.RunID, ProjectID: a.ProjectID, Tenant: a.Tenant, Brief: Brief{ProjectID: a.ProjectID, Title: a.Title, Workspace: a.Workspace, AppID: "agent-" + a.Adapter, TimeoutMinutes: 1}, AppName: a.Adapter, Status: "parked", Stage: "Waiting for you", Summary: "The agent is waiting for your answer.", CreatedAt: now, UpdatedAt: now, Decision: &c}
		saved.Runs = append(saved.Runs, r)
		saved.Request.Cards[i] = c
	}
	if err = s.commit("", "effect", "agent_approval_requested", change{AgentRequest: &saved}); err != nil {
		return AgentRequestView{}, err
	}
	return s.agentRequestView(key)
}
func (s *Store) agentRequestView(key string) (AgentRequestView, error) {
	request, ok := s.agentRequests[key]
	if !ok {
		return AgentRequestView{}, deviceError(404, "unavailable")
	}
	if request.Invalidated {
		return AgentRequestView{}, deviceError(410, "request_ended")
	}
	view := AgentRequestView{RequestID: request.Request.Key, State: "pending", RunIDs: []string{}, Body: json.RawMessage("null")}
	for _, r := range request.Runs {
		view.RunIDs = append(view.RunIDs, r.ID)
	}
	if answer, ok := s.agentAnswers[key]; ok {
		view.State = answer.State
		view.Body = append(json.RawMessage(nil), answer.Body...)
		view.Digest = answer.Digest
	}
	return view, nil
}
func (s *Store) AgentRequestStatus(id, token, key string) (AgentRequestView, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, err := s.agentSession(id, token); err != nil {
		return AgentRequestView{}, err
	}
	return s.agentRequestView(id + "/" + key)
}

// The last answer in a native multi-question request commits the exact native
// response to an outbox in the same event as that canonical resolution.
func (s *Store) agentOutbox(r *Run, resolution decision.Resolution) (*AgentAnswer, error) {
	if r.Decision.Source == nil {
		return nil, nil
	}
	key := r.Decision.Source.SessionID + "/" + r.Decision.Source.RequestID
	request, ok := s.agentRequests[key]
	if !ok || request.Invalidated {
		return nil, ErrConflict
	}
	answers := []decision.Resolution{}
	for _, run := range request.Runs {
		current := s.runs[run.ID]
		if current == nil || current.Decision == nil {
			return nil, ErrConflict
		}
		if run.ID == r.ID {
			answers = append(answers, resolution)
		} else {
			if current.Decision.Resolution == nil {
				return nil, nil
			}
			answers = append(answers, *current.Decision.Resolution)
		}
	}
	body, err := harness.Response(r.Decision.Source.Adapter, request.Request, answers)
	if err != nil {
		return nil, err
	}
	return &AgentAnswer{Key: key, Body: body, Digest: tokenHash(string(body)), State: "accepted"}, nil
}
func (s *Store) AgentDelivery(id, token, key, digest, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.agentSession(id, token); err != nil {
		return err
	}
	key = id + "/" + key
	request, ok := s.agentRequests[key]
	if !ok || request.Invalidated {
		return deviceError(410, "request_ended")
	}
	answer, ok := s.agentAnswers[key]
	if !ok || answer.Digest != digest {
		return deviceError(409, "answer_conflict")
	}
	if state != "applied" && state != "application_blocked" {
		return deviceError(400, "malformed")
	}
	if answer.State == "applied" || answer.State == state {
		return nil
	}
	answer.State = state
	return s.commit("", "effect", "agent_answer_delivery", change{AgentAnswer: &answer})
}
func (s *Store) InvalidateAgentRequest(id, token, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.agentSession(id, token); err != nil {
		return err
	}
	request, ok := s.agentRequests[id+"/"+key]
	if !ok {
		return deviceError(404, "unavailable")
	}
	if request.Invalidated {
		return nil
	}
	request.Invalidated = true
	return s.commit("", "effect", "agent_request_superseded", change{AgentRequest: &request})
}
func (s *Store) applyAgentEvent(e Event, c change) error {
	if s.agentSessions == nil {
		s.agentSessions = map[string]AgentSession{}
		s.agentRequests = map[string]AgentRequest{}
		s.agentAnswers = map[string]AgentAnswer{}
	}
	if s.deviceHistory == nil {
		s.deviceHistory = map[string]decision.Card{}
	}
	if c.AgentSession != nil {
		a := *c.AgentSession
		s.agentSessions[a.ID] = a
		if a.Closed || len(a.EndedThreads) > 0 {
			for key, request := range s.agentRequests {
				ended := a.Closed
				for _, thread := range a.EndedThreads {
					ended = ended || thread == request.Request.Thread
				}
				if request.SessionID == a.ID && ended {
					s.endAgentRequest(key, e)
				}
			}
		}
	}
	if c.AgentRequest != nil {
		q := *c.AgentRequest
		key := q.SessionID + "/" + q.Request.Key
		if q.Invalidated {
			s.endAgentRequest(key, e)
		} else {
			if _, ok := s.agentRequests[key]; ok {
				return errors.New("duplicate agent source request")
			}
			s.agentRequests[key] = q
			for _, r := range q.Runs {
				copy := cloneRun(&r)
				copy.Events = []Event{e}
				s.runs[r.ID] = &copy
				s.deviceHistory[r.ID+"/"+r.Decision.ID] = *copy.Decision
			}
		}
	}
	if c.AgentAnswer != nil {
		answer := *c.AgentAnswer
		s.agentAnswers[answer.Key] = answer
		if answer.State != "accepted" {
			for _, r := range s.agentRequests[answer.Key].Runs {
				run := s.runs[r.ID]
				if run == nil || run.Decision == nil {
					return ErrNotFound
				}
				if answer.State == "applied" {
					at := e.At
					run.Decision.AppliedAt = &at
					run.Decision.ApplicationError = ""
					run.Decision.ResultRunID = run.ID
					run.Status = "done"
					run.Stage = "Answer delivered"
					run.Summary = "The originating session received the recorded answer."
					run.FinishedAt = e.At
				} else {
					run.Decision.ApplicationError = "The source adapter could not deliver the recorded answer. Retry delivery to the same waiting request."
				}
				run.UpdatedAt = e.At
				run.Events = append(run.Events, e)
				s.deviceHistory[run.ID+"/"+run.Decision.ID] = *run.Decision
			}
		}
	}
	return nil
}
func (s *Store) endAgentRequest(key string, e Event) {
	request, ok := s.agentRequests[key]
	if !ok || request.Invalidated {
		return
	}
	request.Invalidated = true
	s.agentRequests[key] = request
	for _, r := range request.Runs {
		run := s.runs[r.ID]
		if run == nil || run.Decision == nil || run.Decision.AppliedAt != nil {
			continue
		}
		at := e.At
		if run.Decision.Resolution == nil {
			run.Decision.SupersededAt = &at
		} else {
			run.Decision.ApplicationError = "The originating request ended before delivery was acknowledged."
		}
		run.Status = "cancelled"
		run.FinishedAt = e.At
		run.UpdatedAt = e.At
		run.Events = append(run.Events, e)
		s.deviceHistory[run.ID+"/"+run.Decision.ID] = *run.Decision
	}
}

func (s *Store) EndAgentThread(id, token, thread string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, err := s.agentSession(id, token)
	if err != nil {
		return err
	}
	if thread == "" || len(thread) > 256 {
		return deviceError(400, "malformed")
	}
	for _, old := range a.EndedThreads {
		if old == thread {
			return nil
		}
	}
	a.EndedThreads = append(append([]string{}, a.EndedThreads...), thread)
	return s.commit("", "effect", "agent_thread_ended", change{AgentSession: &a})
}

func (s *Store) cancelAgentRun(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil || r.Decision == nil || r.Decision.Source == nil {
		return ErrNotFound
	}
	source := r.Decision.Source
	request := s.agentRequests[source.SessionID+"/"+source.RequestID]
	if request.Invalidated {
		return nil
	}
	request.Invalidated = true
	return s.commit("", "effect", "agent_request_cancelled", change{AgentRequest: &request})
}
