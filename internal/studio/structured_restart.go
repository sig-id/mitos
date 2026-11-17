package studio

import "errors"

func (s *Store) interruptStructured(id, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return ErrNotFound
	}
	if r.Brief.Structured == nil {
		return ErrConflict
	}
	if r.Terminal() {
		return nil
	}
	pending := true
	return s.commit(id, "effect", "structured_interrupted", change{Status: "failed", Stage: "Interrupted", Summary: reason, StructuredRetryPending: &pending})
}
func (e *Engine) recoverStructured(r Run) error {
	s := e.Store
	if task, err := s.Task(r.TaskID); err != nil || task.CurrentRunID != r.ID {
		return err
	}
	if r.Status == "running" {
		if err := s.interruptStructured(r.ID, "Server restart interrupted structured work; replacement retains the admitted request."); err != nil {
			return err
		}
	}
	_, err := e.retryStructured(r.Brief, r.ID)
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrTenantBudget) && !errors.Is(err, ErrForbidden) {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pending := false
	return s.commit(r.ID, "effect", "structured_recovery_withheld", change{Status: "failed", Stage: "Recovery withheld", Summary: err.Error(), StructuredRetryPending: &pending})
}
