package studio

import (
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"
	"time"

	"mitos/internal/service"
)

var errHealthRestart = errors.New("service health requested restart")
var errHealthKill = errors.New("service health requested kill")

type ServiceHealthCheck struct {
	Number       int        `json:"number"`
	StartedAt    time.Time  `json:"started_at"`
	FinishedAt   *time.Time `json:"finished_at,omitempty"`
	Outcome      string     `json:"outcome"`
	Detail       string     `json:"detail,omitempty"`
	EvidenceAt   *time.Time `json:"evidence_at,omitempty"`
	OutputSHA256 string     `json:"output_sha256,omitempty"`
	OutputBytes  int        `json:"output_bytes,omitempty"`
}
type ServiceHealthState struct {
	ServiceID         string              `json:"service_id"`
	Revision          int64               `json:"catalog_revision"`
	Config            service.Health      `json:"config"`
	Status            string              `json:"status"`
	MonitorID         string              `json:"monitor_id,omitempty"`
	SegmentID         string              `json:"segment_id,omitempty"`
	StartedAt         time.Time           `json:"started_at"`
	NextAt            *time.Time          `json:"next_at,omitempty"`
	HeartbeatSequence uint64              `json:"heartbeat_sequence"`
	HeartbeatAt       *time.Time          `json:"heartbeat_at,omitempty"`
	Check             *ServiceHealthCheck `json:"check,omitempty"`
	Action            string              `json:"action,omitempty"`
	Detail            string              `json:"detail,omitempty"`
}

func cloneHealth(h *ServiceHealthState) *ServiceHealthState {
	if h == nil {
		return nil
	}
	data, _ := json.Marshal(h)
	var copy ServiceHealthState
	_ = json.Unmarshal(data, &copy)
	return &copy
}
func (s *Store) serviceHealthConfig(id string) *service.Health {
	if s.services != nil {
		for _, u := range s.services.Config.Units {
			if u.ID == id && u.Health != nil {
				h := u.Health.Effective()
				return &h
			}
		}
	}
	return nil
}
func (s *Store) serviceHealthAdmission(id, kind string) *ServiceHealthState {
	if kind != "claims_acquired" && kind != "task_scheduled" {
		return nil
	}
	run := s.runs[id]
	if run == nil || run.Health != nil {
		return nil
	}
	task := s.tasks[run.TaskID]
	if task == nil || !strings.HasPrefix(task.Source, "trigger:") {
		return nil
	}
	serviceID := strings.TrimPrefix(task.Source, "trigger:")
	config := s.serviceHealthConfig(serviceID)
	if config == nil {
		return nil
	}
	return &ServiceHealthState{ServiceID: serviceID, Revision: s.services.Revision, Config: *config, Status: "pending"}
}
func validateServiceHealth(e Event, c change) error {
	h := c.ServiceHealth
	if h == nil {
		return nil
	}
	if e.RunID == "" || !service.ValidID(h.ServiceID) || h.Revision < 1 || h.Revision >= e.Seq || h.Config != h.Config.Effective() || len(h.Detail) > 2000 {
		return errors.New("invalid service health binding")
	}
	if err := h.Config.Validate(); err != nil {
		return err
	}
	if h.Status == "pending" {
		if e.Type != "effect" || (e.Kind != "claims_acquired" && e.Kind != "task_scheduled") || c.Task == nil || c.Task.Source != "trigger:"+h.ServiceID || h.MonitorID != "" {
			return errors.New("invalid health admission")
		}
		return nil
	}
	if h.MonitorID == "" || h.SegmentID == "" || h.StartedAt.IsZero() || (h.Status != "watching" && h.Status != "closed" && h.Status != "superseded") {
		return errors.New("invalid health monitor scope")
	}
	if e.Type != "effect" && !(e.Type == "decision" && (e.Kind == "service_health_check" || e.Kind == "service_health_action")) {
		return errors.New("invalid health event type")
	}
	if h.Action != "" && h.Action != "restart" && h.Action != "kill" {
		return errors.New("invalid health action")
	}
	if h.Check != nil && (h.Check.Number < 1 || h.Check.StartedAt.IsZero() || len(h.Check.Detail) > 2000 || (h.Check.Outcome != "pending" && h.Check.Outcome != "healthy" && h.Check.Outcome != "unhealthy" && h.Check.Outcome != "interrupted" && h.Check.Outcome != "superseded")) {
		return errors.New("invalid health check")
	}
	return nil
}
func applyServiceHealth(e Event, r *Run, c change) error {
	if c.ServiceHealth == nil {
		return nil
	}
	if err := validateServiceHealth(e, c); err != nil {
		return err
	}
	r.Health = cloneHealth(c.ServiceHealth)
	return nil
}
func (s *Store) healthCurrent(run *Run, monitor string) bool {
	if run == nil || run.Status != "running" || run.Health == nil || run.Health.Status != "watching" || run.Health.MonitorID != monitor {
		return false
	}
	task := s.tasks[run.TaskID]
	if task == nil || task.CurrentRunID != run.ID {
		return false
	}
	unit, active := activeServiceUnits(s.services)[run.Health.ServiceID]
	return active && unit.Health != nil && unit.Health.Effective() == run.Health.Config
}
func (s *Store) startHealth(id, segmentID string) (*ServiceHealthState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if run == nil {
		return nil, ErrNotFound
	}
	if run.Health == nil {
		return nil, nil
	}
	unit, active := activeServiceUnits(s.services)[run.Health.ServiceID]
	if !active || unit.Health == nil || unit.Health.Effective() != run.Health.Config {
		return nil, nil
	}
	if run.Status != "running" {
		return nil, ErrConflict
	}
	now := time.Now().UTC()
	next := now.Add(time.Duration(run.Health.Config.Interval) * time.Second)
	h := &ServiceHealthState{ServiceID: run.Health.ServiceID, Revision: run.Health.Revision, Config: run.Health.Config, Status: "watching", MonitorID: newID("health"), SegmentID: segmentID, StartedAt: now, NextAt: &next}
	if err := s.commit(id, "effect", "service_health_started", change{ServiceHealth: h}); err != nil {
		return nil, err
	}
	return cloneHealth(h), nil
}
func (s *Store) healthCheckStart(id, monitor string, now time.Time) (*ServiceHealthState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if !s.healthCurrent(run, monitor) {
		return nil, ErrConflict
	}
	h := cloneHealth(run.Health)
	if h.NextAt == nil || now.Before(*h.NextAt) {
		return nil, nil
	}
	number := 1
	if h.Check != nil {
		number = h.Check.Number + 1
	}
	h.Check = &ServiceHealthCheck{Number: number, StartedAt: now, Outcome: "pending"}
	if err := s.commit(id, "decision", "service_health_check", change{ServiceHealth: h}); err != nil {
		return nil, err
	}
	return cloneHealth(h), nil
}
func (s *Store) healthCheckFinish(id, monitor string, check ServiceHealthCheck) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if run == nil || run.Health == nil || run.Health.MonitorID != monitor || run.Health.Check == nil || run.Health.Check.Number != check.Number {
		return false, ErrConflict
	}
	h := cloneHealth(run.Health)
	valid := s.healthCurrent(run, monitor)
	if !valid {
		check.Outcome = "superseded"
		check.Detail = "The execution or health policy changed before the result was applied."
	} else if h.Config.Type == "heartbeat" {
		// A heartbeat can arrive while the monitor evaluates its earlier snapshot.
		// Fold the current server evidence while holding the heartbeat writer lock.
		evaluateHeartbeat(h, &check, time.Now().UTC())
	}
	now := time.Now().UTC()
	check.FinishedAt = &now
	h.Check = &check
	next := now.Add(time.Duration(h.Config.Interval) * time.Second)
	h.NextAt = &next
	if err := s.commit(id, "effect", "service_health_observed", change{ServiceHealth: h}); err != nil {
		return false, err
	}
	return valid && check.Outcome == "unhealthy" && h.Config.OnUnhealthy != "log", nil
}
func (s *Store) healthAction(id, monitor string) (action string, resume bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if !s.healthCurrent(run, monitor) {
		return "", false, nil
	}
	h := cloneHealth(run.Health)
	if h.Check == nil || h.Check.Outcome != "unhealthy" || h.Config.OnUnhealthy == "log" {
		return "", false, nil
	}
	if h.Config.Type == "heartbeat" {
		check := *h.Check
		evaluateHeartbeat(h, &check, time.Now().UTC())
		if check.Outcome == "healthy" {
			h.Check.Outcome = "superseded"
			h.Check.Detail = "A newer heartbeat withdrew the pending health action."
			return "", true, s.commit(id, "effect", "service_health_observed", change{ServiceHealth: h})
		}
	}
	h.Action = h.Config.OnUnhealthy
	if err := s.commit(id, "decision", "service_health_action", change{ServiceHealth: h}); err != nil {
		return "", false, err
	}
	return h.Action, false, nil
}

func evaluateHeartbeat(h *ServiceHealthState, check *ServiceHealthCheck, now time.Time) {
	at := h.StartedAt
	if h.HeartbeatAt != nil {
		at = *h.HeartbeatAt
	}
	check.EvidenceAt = &at
	check.Outcome, check.Detail = "healthy", ""
	if age := now.Sub(at); age < 0 || age > time.Duration(h.Config.MaxAge)*time.Second {
		check.Outcome = "unhealthy"
		check.Detail = "The current monitor has no recent server-recorded heartbeat."
	}
}
func (s *Store) closeHealth(id, monitor, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if run == nil || run.Health == nil || run.Health.MonitorID != monitor || run.Health.Status != "watching" {
		return nil
	}
	h := cloneHealth(run.Health)
	h.Status = "closed"
	h.Detail = reason
	h.NextAt = nil
	if h.Check != nil && h.Check.Outcome == "pending" {
		now := time.Now().UTC()
		h.Check.Outcome = "interrupted"
		h.Check.FinishedAt = &now
		h.Check.Detail = reason
	}
	return s.commit(id, "effect", "service_health_closed", change{ServiceHealth: h})
}

// Heartbeat time comes from the server. A repeated/lower sequence never refreshes
// the clock, and an old monitor cannot keep a replacement segment healthy.
func (s *Store) ServiceHeartbeat(id, monitor string, sequence uint64) (ServiceHealthState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run := s.runs[id]
	if !s.healthCurrent(run, monitor) || run.Health.Config.Type != "heartbeat" || sequence == 0 {
		return ServiceHealthState{}, ErrConflict
	}
	h := cloneHealth(run.Health)
	if sequence < h.HeartbeatSequence {
		return ServiceHealthState{}, ErrConflict
	}
	if sequence == h.HeartbeatSequence {
		return *h, nil
	}
	now := time.Now().UTC()
	h.HeartbeatAt = &now
	h.HeartbeatSequence = sequence
	if err := s.commit(id, "effect", "service_heartbeat", change{ServiceHealth: h}); err != nil {
		return ServiceHealthState{}, err
	}
	return *cloneHealth(h), nil
}
func healthPoliciesEqual(a, b *service.Health) bool { return reflect.DeepEqual(a, b) }

type ServiceHealthView struct {
	RunID string             `json:"run_id"`
	State ServiceHealthState `json:"state"`
}

func (s *Store) serviceHealthViews(id, latest string) []ServiceHealthView {
	out := []ServiceHealthView{}
	for _, task := range s.tasks {
		if task.Source != "trigger:"+id {
			continue
		}
		run := s.runs[task.CurrentRunID]
		if run == nil || run.Health == nil || run.Terminal() && run.ID != latest {
			continue
		}
		out = append(out, ServiceHealthView{RunID: run.ID, State: *cloneHealth(run.Health)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunID < out[j].RunID })
	return out
}
