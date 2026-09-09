package studio

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"mitos/internal/service"
)

var errServiceCondition = errors.New("service condition withheld execution")

type ServiceConditionFact struct {
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	Passed bool   `json:"passed"`
}
type ServiceConditionState struct {
	ServiceID    string                 `json:"service_id"`
	Revision     int64                  `json:"catalog_revision"`
	Policy       service.Condition      `json:"policy"`
	Status       string                 `json:"status"`
	CheckID      string                 `json:"check_id,omitempty"`
	StartedAt    *time.Time             `json:"started_at,omitempty"`
	FinishedAt   *time.Time             `json:"finished_at,omitempty"`
	Facts        []ServiceConditionFact `json:"facts,omitempty"`
	OutputSHA256 string                 `json:"output_sha256,omitempty"`
	OutputBytes  int                    `json:"output_bytes,omitempty"`
	Detail       string                 `json:"detail,omitempty"`
}

func cloneCondition(c *ServiceConditionState) *ServiceConditionState {
	if c == nil {
		return nil
	}
	b, _ := json.Marshal(c)
	var out ServiceConditionState
	_ = json.Unmarshal(b, &out)
	return &out
}
func (s *Store) serviceConditionAdmission(id, kind string) *ServiceConditionState {
	if kind != "claims_acquired" && kind != "task_scheduled" {
		return nil
	}
	r := s.runs[id]
	if r == nil || r.Condition != nil || s.services == nil {
		return nil
	}
	t := s.tasks[r.TaskID]
	if t == nil || !strings.HasPrefix(t.Source, "trigger:") {
		return nil
	}
	serviceID := strings.TrimPrefix(t.Source, "trigger:")
	for _, u := range s.services.Config.Units {
		if u.ID == serviceID && u.Condition != nil {
			return cloneCondition(&ServiceConditionState{ServiceID: serviceID, Revision: s.services.Revision, Policy: *u.Condition, Status: "bound"})
		}
	}
	return nil
}
func validateServiceCondition(e Event, c change) error {
	h := c.ServiceCondition
	if h == nil {
		return nil
	}
	if e.RunID == "" || !service.ValidID(h.ServiceID) || h.Revision < 1 || h.Revision >= e.Seq || len(h.Detail) > 2000 || len(h.Facts) > 68 || h.OutputBytes < 0 || h.OutputBytes > 2<<20 {
		return errors.New("invalid service condition binding")
	}
	if err := h.Policy.Validate(); err != nil {
		return err
	}
	if h.OutputSHA256 != "" {
		b, err := hex.DecodeString(h.OutputSHA256)
		if err != nil || len(b) != 32 {
			return errors.New("invalid condition output digest")
		}
	}
	if h.Status == "bound" {
		if e.Type != "effect" || (e.Kind != "claims_acquired" && e.Kind != "task_scheduled") || c.Task == nil || c.Task.Source != "trigger:"+h.ServiceID || h.CheckID != "" || h.StartedAt != nil || h.FinishedAt != nil {
			return errors.New("invalid condition admission")
		}
		return nil
	}
	if h.CheckID == "" || h.StartedAt == nil || h.StartedAt.IsZero() {
		return errors.New("invalid condition check scope")
	}
	switch h.Status {
	case "checking":
		if e.Type != "decision" || e.Kind != "service_condition_check" || h.FinishedAt != nil {
			return errors.New("invalid condition check decision")
		}
	case "passed", "skipped", "error", "interrupted":
		if e.Type != "effect" || e.Kind != "service_condition_observed" || h.FinishedAt == nil || h.FinishedAt.IsZero() {
			return errors.New("invalid condition result")
		}
	default:
		return errors.New("invalid condition state")
	}
	for _, f := range h.Facts {
		switch f.Kind {
		case "file_exists", "file_not_exists", "env_set", "env_equals", "env_not_equals", "command":
		default:
			return errors.New("invalid condition fact")
		}
		if len(f.Name) > 1024 {
			return errors.New("invalid condition fact name")
		}
	}
	return nil
}
func applyServiceCondition(e Event, r *Run, c change) error {
	if c.ServiceCondition == nil {
		return nil
	}
	if err := validateServiceCondition(e, c); err != nil {
		return err
	}
	r.Condition = cloneCondition(c.ServiceCondition)
	return nil
}
func (s *Store) beginServiceCondition(id string) (*ServiceConditionState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return nil, ErrNotFound
	}
	if r.Condition == nil {
		return nil, nil
	}
	t := s.tasks[r.TaskID]
	if r.Status != "running" || t == nil || t.CurrentRunID != id {
		return nil, ErrConflict
	}
	h := cloneCondition(r.Condition)
	now := time.Now().UTC()
	h.CheckID = newID("condition")
	h.Status = "checking"
	h.StartedAt = &now
	h.FinishedAt = nil
	h.Facts = nil
	h.OutputSHA256 = ""
	h.OutputBytes = 0
	h.Detail = ""
	if err := s.commit(id, "decision", "service_condition_check", change{ServiceCondition: h}); err != nil {
		return nil, err
	}
	return cloneCondition(h), nil
}
func (s *Store) finishServiceCondition(id string, h *ServiceConditionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil || r.Condition == nil || r.Condition.CheckID != h.CheckID || r.Condition.Status != "checking" {
		return ErrConflict
	}
	h = cloneCondition(h)
	t := s.tasks[r.TaskID]
	if r.Status != "running" || t == nil || t.CurrentRunID != id {
		h.Status = "interrupted"
		h.Detail = "The execution stopped before its condition result could authorize work."
	}
	now := time.Now().UTC()
	h.FinishedAt = &now
	return s.commit(id, "effect", "service_condition_observed", change{ServiceCondition: h})
}
func (s *Store) interruptServiceCondition(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil || r.Condition == nil || r.Condition.Status != "checking" {
		return nil
	}
	h := cloneCondition(r.Condition)
	h.Status = "interrupted"
	h.Detail = "Server restart interrupted the condition check; its command was not replayed."
	now := time.Now().UTC()
	h.FinishedAt = &now
	return s.commit(id, "effect", "service_condition_observed", change{ServiceCondition: h})
}
